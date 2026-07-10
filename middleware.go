package portless

import (
	"context"
	"net"
)

// DialFunc dials an address. It is the unit the dial path is composed of.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Middleware wraps the dial path of a route. Fault injection, latency,
// metrics, and logging are all middleware: return a DialFunc that delays,
// errors, or wraps the resulting net.Conn.
//
// Chain order: registry middleware (outermost) → route middleware → backend.
type Middleware func(next DialFunc) DialFunc

// ConnWrapper adapts the common "just wrap the connection" case into a
// Middleware. name is the route host extracted from the dialed address.
func ConnWrapper(wrap func(name string, c net.Conn) net.Conn) Middleware {
	return func(next DialFunc) DialFunc {
		return func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := next(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return wrap(hostOf(address), conn), nil
		}
	}
}

// chain composes middleware around base; mws[0] is outermost.
func chain(base DialFunc, mws ...Middleware) DialFunc {
	for i := len(mws) - 1; i >= 0; i-- {
		base = mws[i](base)
	}
	return base
}

func hostOf(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}
