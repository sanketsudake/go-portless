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

// HealthChecker is an optional Backend capability: liveness beyond
// "accepts TCP". Used by Route.Ready and status/doctor tooling.
type HealthChecker interface {
	Healthy(ctx context.Context) error
}

// ContextDialer is the consumer-facing dial abstraction. *Registry implements
// it; the proxy, HTTP sugar, and third-party integrations depend on this
// interface rather than on *Registry.
type ContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
