package portless

import (
	"context"
	"crypto/tls"
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
	// fallback dials addresses whose host matches no route; nil means strict
	// (unknown names fail with ErrRouteNotFound).
	fallback     ContextDialer
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
	hostRewrite  string
}

// WithFallback makes dials to unregistered names fall back to d instead of
// failing with ErrRouteNotFound. A nil d falls back to a plain net.Dialer.
// The default (no fallback) is strict: a typo'd or unregistered name fails
// loudly instead of silently dialing the real network — important when route
// names mirror resolvable DNS names.
func WithFallback(d ContextDialer) Option {
	return func(c *config) {
		if d == nil {
			d = &net.Dialer{}
		}
		c.fallback = d
	}
}

// WithFallbackDialer sets the dialer used for addresses whose host does not
// match any registered route.
//
// Deprecated: use WithFallback. Since v0.2.0 registries are strict by
// default, so this option now also enables the fallback path.
func WithFallbackDialer(d ContextDialer) Option {
	return WithFallback(d)
}

// WithStrict makes dials to unregistered names fail with ErrRouteNotFound.
//
// Deprecated: strict is the default since v0.2.0; this option is a no-op.
// Use WithFallback to opt back in to fallback dials.
func WithStrict() Option {
	return func(*config) {}
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
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("health %s: status %s", path, resp.Status)
		}
		return nil
	})
}

// RouteWithTLSHealth gates readiness on a TLS handshake succeeding on the
// given backend port: "accepts TCP" and "able to serve TLS" genuinely differ
// (bad cert material, TLS config regressions), so TLS routes should probe the
// handshake, not the accept.
//
// A nil cfg defaults to InsecureSkipVerify — correct for a readiness probe,
// which checks liveness of TLS serving, not peer identity; verification
// against a not-yet-trusted test CA would keep the route not-ready forever.
// Pass an explicit cfg to verify identity as part of readiness.
func RouteWithTLSHealth(port int, cfg *tls.Config) RouteOption {
	if cfg == nil {
		cfg = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- liveness probe, not identity check (see doc comment)
	}
	return RouteWithHealthCheck(func(ctx context.Context, dial DialFunc) error {
		conn, err := dial(ctx, "tcp", net.JoinHostPort("localhost", fmt.Sprint(port)))
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		tc := tls.Client(conn, cfg)
		defer func() { _ = tc.Close() }()
		return tc.HandshakeContext(ctx)
	})
}

// RouteWithHostRewrite sets the Host header HTTP requests to this route carry
// instead of the route name. Forwarded backends (port-forwards, SSH tunnels,
// localhost relays) deliver traffic from a loopback local address, and many
// servers treat "loopback peer + non-loopback Host" as a DNS-rebinding attack
// and reject with 403 — RouteWithHostRewrite("127.0.0.1") makes such servers
// see a loopback Host.
//
// The registry itself is L4 and never touches HTTP: the rewrite is applied by
// DefaultClient/HTTPClient (and the CLI daemon's forward proxy). If you build
// your own transport from Transport or DialContext, wrap it with
// [Registry.WrapRoundTripper]. Raw TLS through CONNECT tunnels cannot be
// rewritten.
func RouteWithHostRewrite(host string) RouteOption {
	return func(c *routeConfig) { c.hostRewrite = host }
}

// RouteWithPortMap maps requested ports to backend ports: a dial to
// "name:req" is handed to the backend as "name:mapped". When a map is set,
// dialing an unmapped port fails loudly (non-retryably).
func RouteWithPortMap(m map[int]int) RouteOption {
	return func(c *routeConfig) { c.portMap = m }
}
