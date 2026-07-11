package portless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
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
	if _, err := rt.bridgeDialPort(); err != nil {
		return nil, fmt.Errorf("portless: listen local %q: %w", name, err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("portless: listen local %q: %w", name, err)
	}
	if !r.bridges.add(l) {
		_ = l.Close()
		return nil, ErrClosed
	}
	go r.bridgeAccept(l, name)
	return l, nil
}

// closeWriter is the half-close capability of TCP conns and SPDY streams.
type closeWriter interface{ CloseWrite() error }

// bridgeSet tracks a registry's ListenLocal listeners under its own lock, so
// bridge lifecycle never contends with the route map's mutex.
type bridgeSet struct {
	mu     sync.Mutex
	ls     []net.Listener
	closed bool
}

// add registers l; it reports false once closeAll has run.
func (s *bridgeSet) add(l net.Listener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.ls = append(s.ls, l)
	return true
}

// remove drops l, so early-closed bridges do not accumulate on
// never-Closed registries.
func (s *bridgeSet) remove(l net.Listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, b := range s.ls {
		if b == l {
			s.ls = append(s.ls[:i], s.ls[i+1:]...)
			return
		}
	}
}

// closeAll closes every tracked listener and rejects future adds.
func (s *bridgeSet) closeAll() {
	s.mu.Lock()
	ls := s.ls
	s.ls, s.closed = nil, true
	s.mu.Unlock()
	for _, l := range ls {
		_ = l.Close()
	}
}

// bridgeAccept runs the accept loop. Transient Accept errors (e.g. fd
// exhaustion) are retried with backoff, like net/http.Server, instead of
// leaving a bound listener nobody accepts on.
func (r *Registry) bridgeAccept(l net.Listener, name string) {
	defer r.bridges.remove(l)
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

// bridgeConn pipes one accepted local connection to a fresh route dial,
// resolving name at connection time so Remove/re-Add are honored.
func (r *Registry) bridgeConn(conn net.Conn, name string) {
	defer conn.Close()

	// A dead backend must not surface as a silent connection reset.
	warn := func(err error) {
		r.cfg.logger.Warn("portless: local bridge dial failed", "route", name, "err", err)
	}
	fail := func(err error) {
		r.emit(Event{Type: EventDialError, Route: name, Err: err})
		warn(err)
	}
	rt, ok := r.Lookup(name)
	if !ok {
		fail(fmt.Errorf("portless: bridge %q: %w", name, ErrRouteNotFound))
		return
	}
	port, err := rt.bridgeDialPort()
	if err != nil {
		fail(fmt.Errorf("portless: bridge %q: %w", name, err))
		return
	}
	upstream, err := r.dialRoute(context.Background(), rt, "tcp",
		net.JoinHostPort(rt.name, strconv.Itoa(port)))
	if err != nil {
		warn(err) // dialRoute already emitted EventDialError
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
		if cw, ok := upstream.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(conn, upstream)
	// Tell the client the response stream ended; without this a
	// connection-close-delimited response hangs the consumer forever. The
	// client side is our own *net.TCPConn, so CloseWrite is always there;
	// the full-close fallback only guards exotic wrappers.
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	} else {
		_ = conn.Close()
	}
	<-done
}
