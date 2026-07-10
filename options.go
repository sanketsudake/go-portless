package portless

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Option configures a Registry.
type Option func(*config)

// RouteOption configures a single route.
type RouteOption func(*routeConfig)

// HealthCheck verifies a route's endpoint is actually serving, beyond
// accepting TCP. dial is the route's own dial path; the host part of the
// address handed to dial is ignored by backends (only the port matters).
// A non-nil return keeps the readiness loop waiting.
type HealthCheck func(ctx context.Context, dial DialFunc) error

type config struct {
	fallback     ContextDialer
	strict       bool
	readyTimeout time.Duration
	logger       *slog.Logger
	middleware   []Middleware
	handlers     []func(Event)
}

type routeConfig struct {
	readyTimeout time.Duration
	middleware   []Middleware
	health       HealthCheck
	portMap      map[int]int
}

// WithFallbackDialer sets the dialer used for addresses whose host does not
// match any registered route. Default: a plain net.Dialer.
func WithFallbackDialer(d ContextDialer) Option {
	return func(c *config) { c.fallback = d }
}

// WithStrict makes dials to unregistered names fail with ErrRouteNotFound
// instead of falling back to a real network dial.
func WithStrict() Option {
	return func(c *config) { c.strict = true }
}

// WithReadyTimeout caps how long a dial waits for a route's backend to become
// ready. Default: 60s. Per-route override: RouteWithReadyTimeout.
func WithReadyTimeout(d time.Duration) Option {
	return func(c *config) { c.readyTimeout = d }
}

// WithLogger sets the logger for registry internals. Default: slog.Default
// at debug level only (the registry is quiet by default).
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithMiddleware appends registry-level dial middleware, applied to every
// route (outermost, before route middleware). Fallback dials are not wrapped.
func WithMiddleware(mw ...Middleware) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw...) }
}

// WithEventHandler registers a handler for registry events. May be given
// multiple times; handlers run synchronously and must not block.
func WithEventHandler(h func(Event)) Option {
	return func(c *config) { c.handlers = append(c.handlers, h) }
}

// RouteWithReadyTimeout overrides the registry-level ready timeout for one route.
func RouteWithReadyTimeout(d time.Duration) RouteOption {
	return func(c *routeConfig) { c.readyTimeout = d }
}

// RouteWithMiddleware appends dial middleware for one route (inside registry
// middleware, around the backend).
func RouteWithMiddleware(mw ...Middleware) RouteOption {
	return func(c *routeConfig) { c.middleware = append(c.middleware, mw...) }
}

// RouteWithHealthCheck gates the route's readiness on hc: a dial is handed
// out only after hc returns nil. hc runs on every dial.
func RouteWithHealthCheck(hc HealthCheck) RouteOption {
	return func(c *routeConfig) { c.health = hc }
}

// RouteWithHTTPHealth gates readiness on an HTTP GET to path on the given
// backend port returning a 2xx status.
func RouteWithHTTPHealth(port int, path string) RouteOption {
	return RouteWithHealthCheck(func(ctx context.Context, dial DialFunc) error {
		client := &http.Client{Transport: &http.Transport{
			DialContext:       dial,
			DisableKeepAlives: true,
		}}
		defer client.CloseIdleConnections()
		url := fmt.Sprintf("http://%s%s", net.JoinHostPort("localhost", fmt.Sprint(port)), path)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("health %s: status %s", path, resp.Status)
		}
		return nil
	})
}

// RouteWithPortMap maps requested ports to backend ports: a dial to
// "name:req" is handed to the backend as "name:mapped". When a map is set,
// dialing an unmapped port fails loudly (non-retryably).
func RouteWithPortMap(m map[int]int) RouteOption {
	return func(c *routeConfig) { c.portMap = m }
}
