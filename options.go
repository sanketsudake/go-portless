package portless

import (
	"log/slog"
	"time"
)

// Option configures a Registry.
type Option func(*config)

// RouteOption configures a single route.
type RouteOption func(*routeConfig)

type config struct {
	fallback     ContextDialer
	strict       bool
	readyTimeout time.Duration
	logger       *slog.Logger
}

type routeConfig struct {
	readyTimeout time.Duration
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

// RouteWithReadyTimeout overrides the registry-level ready timeout for one route.
func RouteWithReadyTimeout(d time.Duration) RouteOption {
	return func(c *routeConfig) { c.readyTimeout = d }
}
