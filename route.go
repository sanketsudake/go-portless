package portless

import "context"

// Route is a named routing entry in a Registry.
type Route struct {
	name     string
	backend  Backend
	cfg      routeConfig
	registry *Registry
}

// Name returns the route's registered name.
func (rt *Route) Name() string { return rt.name }

// Backend returns the route's backend.
func (rt *Route) Backend() Backend { return rt.backend }

// Ready dials the route once (blocking through the readiness loop) and
// discards the connection. It reports nil when the backend accepts
// connections. Used by status/doctor tooling and CI wait steps.
func (rt *Route) Ready(ctx context.Context) error {
	conn, err := rt.registry.dialRoute(ctx, rt, "tcp", rt.name+":0")
	if err != nil {
		return err
	}
	return conn.Close()
}
