package portless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// ListenLocal binds a kernel-assigned loopback listener and bridges every
// accepted connection to the named route. It is the escape hatch for
// consumers that cannot take a custom dialer or *http.Client — bare HTTP
// call sites, third-party SDKs, subprocesses, and URLs printed for humans —
// giving them a plain dialable 127.0.0.1:<port> address that still rides the
// route's readiness loop and self-healing on every connection.
//
// Each accepted connection dials the route independently (blocking through
// readiness, bounded by the route's ready timeout) and is piped
// bidirectionally. The client side is always half-closed so
// connection-close-delimited responses terminate correctly; the upstream
// side is half-closed when its conn supports CloseWrite and left open
// otherwise (a full close there would truncate an in-flight response).
// Per-connection dial failures are emitted as EventDialError and logged at
// Warn.
//
// A route with a multi-entry port map cannot be bridged: the bridge exposes
// one local address, so it cannot know which mapped port the consumer
// wants — bridge one route per port instead.
//
// The bridge is bound to the NAME, not the route object: every connection
// looks the route up afresh, so a Remove followed by a re-Add under the same
// name is picked up on the next connection, and connections accepted after a
// bare Remove are closed instead of dialing a stopped backend.
//
// The listener and its in-flight connections are closed by Registry.Close;
// callers may close the listener earlier. Process-lifetime registries that
// never call Close own the listener for the life of the process, which is
// the intended shape for test-framework and CLI singletons.
func (r *Registry) ListenLocal(name string) (net.Listener, error) {
	rt, ok := r.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("portless: listen local %q: %w", name, ErrRouteNotFound)
	}
	if _, err := bridgeDialPort(rt); err != nil {
		return nil, fmt.Errorf("portless: listen local %q: %w", name, err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("portless: listen local %q: %w", name, err)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = l.Close()
		return nil, ErrClosed
	}
	r.bridges = append(r.bridges, l)
	r.mu.Unlock()

	go r.bridgeAccept(l, name)
	return l, nil
}

// bridgeDialPort picks the requested port a bridge dials for rt: 0 when the
// route has no port map, the single mapped port when it has one entry, and
// an error when the map is ambiguous.
func bridgeDialPort(rt *Route) (int, error) {
	switch len(rt.cfg.portMap) {
	case 0:
		return 0, nil
	case 1:
		for p := range rt.cfg.portMap {
			return p, nil
		}
	}
	return 0, fmt.Errorf("route has %d mapped ports; bridge one route per port", len(rt.cfg.portMap))
}

// bridgeAccept runs the accept loop. Transient Accept errors (e.g. fd
// exhaustion) are retried with backoff, like net/http.Server, instead of
// leaving a bound listener nobody accepts on. On exit the listener is
// dropped from the registry's bridge list so early-closed bridges do not
// accumulate on never-Closed registries.
func (r *Registry) bridgeAccept(l net.Listener, name string) {
	defer r.removeBridge(l)
	delay := 5 * time.Millisecond
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // closed by caller or Registry.Close
			}
			r.cfg.logger.Warn("portless: local bridge accept failed, retrying",
				"route", name, "err", err)
			timer := time.NewTimer(delay)
			select {
			case <-r.done:
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay *= 2; delay > time.Second {
				delay = time.Second
			}
			continue
		}
		delay = 5 * time.Millisecond
		go r.bridgeConn(conn, name)
	}
}

// removeBridge drops l from the registry's bridge list.
func (r *Registry) removeBridge(l net.Listener) {
	r.mu.Lock()
	for i, b := range r.bridges {
		if b == l {
			r.bridges = append(r.bridges[:i], r.bridges[i+1:]...)
			break
		}
	}
	r.mu.Unlock()
}

// bridgeConn pipes one accepted local connection to a fresh route dial,
// resolving name at connection time so Remove/re-Add are honored.
func (r *Registry) bridgeConn(conn net.Conn, name string) {
	defer conn.Close()

	fail := func(err error) {
		r.emit(Event{Type: EventDialError, Route: name, Err: err})
		r.cfg.logger.Warn("portless: local bridge dial failed", "route", name, "err", err)
	}
	rt, ok := r.Lookup(name)
	if !ok {
		fail(fmt.Errorf("portless: bridge %q: %w", name, ErrRouteNotFound))
		return
	}
	port, err := bridgeDialPort(rt)
	if err != nil {
		fail(fmt.Errorf("portless: bridge %q: %w", name, err))
		return
	}
	upstream, err := r.dialRoute(context.Background(), rt, "tcp",
		net.JoinHostPort(rt.name, strconv.Itoa(port)))
	if err != nil {
		// dialRoute already emitted EventDialError; a dead backend must not
		// surface as a silent connection reset, so also log loudly.
		r.cfg.logger.Warn("portless: local bridge dial failed",
			"route", rt.name, "err", err)
		return
	}
	defer upstream.Close()

	// Tear the pipe down when the registry closes, so bridged connections
	// do not outlive Close.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-r.done:
			_ = conn.Close()
			_ = upstream.Close()
		case <-stop:
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(upstream, conn)
		// Propagate the client's EOF when the upstream supports half-close.
		// Without CloseWrite (k8s port-forward streams, net.Pipe) the
		// upstream stays open: a full close here would truncate an in-flight
		// response. Servers that need the request EOF to respond then wait
		// until the client closes.
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(conn, upstream)
	// Tell the client the response stream ended; without this a
	// connection-close-delimited response hangs the consumer forever.
	closeWrite(conn)
	<-done
}

// closeWrite half-closes the write side when the conn supports it, else
// fully closes. Used on the client side of a bridge, which is always a
// *net.TCPConn in practice.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
