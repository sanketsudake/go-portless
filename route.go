package portless

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strconv"
)

// Route is a named routing entry in a Registry.
type Route struct {
	name     string
	backend  Backend
	cfg      routeConfig
	registry *Registry
	dial     DialFunc // compiled chain: registry mw → route mw → portmap → backend
}

// buildDial compiles the route's dial chain.
func (rt *Route) buildDial(registryMW []Middleware) {
	base := rt.backend.DialContext
	if len(rt.cfg.portMap) > 0 {
		portMap, backendDial := rt.cfg.portMap, base
		base = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("portless: route %q: address %q has no port to map", rt.name, address)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return nil, fmt.Errorf("portless: route %q: bad port in %q: %w", rt.name, address, err)
			}
			mapped, ok := portMap[port]
			if !ok {
				return nil, fmt.Errorf("portless: route %q: port %d not in port map", rt.name, port)
			}
			return backendDial(ctx, network, net.JoinHostPort(host, strconv.Itoa(mapped)))
		}
	}
	mws := make([]Middleware, 0, len(registryMW)+len(rt.cfg.middleware))
	mws = append(mws, registryMW...)
	mws = append(mws, rt.cfg.middleware...)
	rt.dial = chain(base, mws...)
}

// Name returns the route's registered name.
func (rt *Route) Name() string { return rt.name }

// Backend returns the route's backend.
func (rt *Route) Backend() Backend { return rt.backend }

// HostRewrite returns the Host header override configured with
// RouteWithHostRewrite, if any.
func (rt *Route) HostRewrite() (string, bool) {
	return rt.cfg.hostRewrite, rt.cfg.hostRewrite != ""
}

// Addr returns the concrete address the route's backend dials, when the
// backend exposes one (see Addresser). It lets registry-external consumers —
// env vars handed to subprocesses, plain clients outside the registry — be
// pointed at the real address without keeping a parallel name→addr map.
// ok is false when the backend has no address (yet).
func (rt *Route) Addr() (net.Addr, bool) {
	a, ok := rt.backend.(Addresser)
	if !ok {
		return nil, false
	}
	addr := a.Addr()
	if addr == nil || isTypedNil(addr) {
		return nil, false
	}
	return addr, true
}

// isTypedNil catches Addresser implementations that return a nil pointer
// inside a non-nil net.Addr interface value — without this, ok would be true
// and addr.String() would panic in callers like the control server.
func isTypedNil(addr net.Addr) bool {
	v := reflect.ValueOf(addr)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// Ready dials the route once (blocking through the readiness loop) and
// discards the connection. It reports nil when the backend accepts
// connections. Used by status/doctor tooling and CI wait steps.
func (rt *Route) Ready(ctx context.Context) error {
	// Dial a port the route accepts: a mapped requested port if the route
	// has a port map (":0" would never match one), else 0.
	port := 0
	for reqPort := range rt.cfg.portMap {
		port = reqPort
		break
	}
	conn, err := rt.registry.dialRoute(ctx, rt, "tcp", net.JoinHostPort(rt.name, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return conn.Close()
}
