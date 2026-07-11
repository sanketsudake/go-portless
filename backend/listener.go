package backend

import (
	"context"
	"fmt"
	"net"

	portless "github.com/sanketsudake/go-portless"
)

// Listener returns a Backend that dials l's address. The caller keeps
// ownership of l: removing the route does not close it.
//
// Readiness note: the route is dial-ready as soon as the socket is BOUND —
// connections land in the kernel's accept backlog — which can be earlier
// than the service behind l actually serving. For TLS servers, pair the
// route with RouteWithTLSHealth so readiness means "handshake succeeds",
// not just "socket exists".
func Listener(l net.Listener) portless.Backend {
	return TCP(l.Addr().String())
}

// ListenAndAdd binds a kernel-assigned loopback listener, registers it under
// name, and returns it — the embedded-service recipe as a one-liner:
//
//	l, err := backend.ListenAndAdd(ctx, reg, "router")
//	// hand l to the service's start options; consumers needing a real
//	// URL use Route.Addr() — no pre-picked ports, no port races.
//
// On registration failure the listener is closed. The same readiness note
// as Listener applies.
func ListenAndAdd(ctx context.Context, reg *portless.Registry, name string, opts ...portless.RouteOption) (net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("backend: listen for %q: %w", name, err)
	}
	if _, err := reg.Add(ctx, name, Listener(l), opts...); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}
