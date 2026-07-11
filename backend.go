package portless

import (
	"context"
	"net"
)

// Backend resolves dials for a route. address is the full "name:port" the
// caller asked for; backends that serve a single endpoint may ignore it,
// multi-port backends use the port.
//
// DialContext must respect ctx and should return quickly with a Retryable
// error when the endpoint is not accepting connections yet — the Registry
// owns the wait/retry loop.
type Backend interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Starter is an optional Backend capability: begin background work (watches,
// cached connections). Called once by Registry.Add.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is an optional Backend capability: release goroutines and
// connections. Called by Registry.Remove and Registry.Close.
type Stopper interface {
	Stop(ctx context.Context) error
}

// Addresser is an optional Backend capability: expose the concrete address
// the backend dials. Backends with a fixed or already-resolved endpoint
// (TCP, Listener, a set Future) implement it; backends without a dialable
// local address (k8s port-forward) do not. A nil return means "no address
// yet" (e.g. an unset Future). Mem reports a placeholder address on the
// "mem" network, which is not dialable. See Route.Addr.
type Addresser interface {
	Addr() net.Addr
}

// ContextDialer is the consumer-facing dial abstraction. *Registry implements
// it; the proxy, HTTP sugar, and third-party integrations depend on this
// interface rather than on *Registry.
type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
