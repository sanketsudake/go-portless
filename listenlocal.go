package portless

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
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
// bidirectionally with half-close in both directions, so EOF-delimited
// protocols terminate correctly. Per-connection dial failures are emitted as
// EventDialError and logged at Warn.
//
// The bridge is bound to the NAME, not the route object: every connection
// looks the route up afresh, so a Remove followed by a re-Add under the same
// name is picked up on the next connection, and connections accepted after a
// bare Remove are closed instead of dialing a stopped backend.
//
// The listener is closed by Registry.Close; callers may close it earlier.
// Process-lifetime registries that never call Close own the listener for the
// life of the process, which is the intended shape for test-framework and
// CLI singletons.
func (r *Registry) ListenLocal(name string) (net.Listener, error) {
	if _, ok := r.Lookup(name); !ok {
		return nil, fmt.Errorf("portless: listen local %q: %w", name, ErrRouteNotFound)
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

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed (by caller or Registry.Close)
			}
			go r.bridgeConn(conn, name)
		}
	}()
	return l, nil
}

// bridgeConn pipes one accepted local connection to a fresh route dial,
// resolving name at connection time so Remove/re-Add are honored.
func (r *Registry) bridgeConn(conn net.Conn, name string) {
	defer conn.Close()

	rt, ok := r.Lookup(name)
	if !ok {
		r.cfg.logger.Warn("portless: local bridge dial failed",
			"route", name, "err", ErrRouteNotFound)
		return
	}
	// Dial a port the route accepts: a mapped requested port if the route
	// has a port map, else 0 (mirrors Route.Ready).
	port := 0
	for reqPort := range rt.cfg.portMap {
		port = reqPort
		break
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

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(upstream, conn)
		// Propagate the client's EOF so EOF-delimited requests terminate.
		closeWrite(upstream)
	}()
	_, _ = io.Copy(conn, upstream)
	// Tell the client the response stream ended; without this a
	// connection-close-delimited response hangs the consumer forever.
	closeWrite(conn)
	<-done
}

// closeWrite half-closes the write side when the conn supports it (TCP,
// SPDY streams), else fully closes.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
