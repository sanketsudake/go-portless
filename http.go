package portless

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TransportOption customizes the http.Transport built by Transport/HTTPClient.
type TransportOption func(*http.Transport)

// initHTTP builds the shared transport/client on first use, under httpMu so
// concurrent DefaultTransport/DefaultClient/Close calls are safe.
func (r *Registry) initHTTP() {
	r.httpMu.Lock()
	defer r.httpMu.Unlock()
	if r.defaultTransport == nil {
		r.defaultTransport = r.Transport()
		r.defaultClient = &http.Client{Transport: r.WrapRoundTripper(r.defaultTransport)}
	}
}

// HostRewrite maps a request's URL host ("name" or "name:port") to the Host
// header it should carry, honoring the route's RouteWithHostRewrite and
// preserving the port. ok is false when no rewrite applies. It is the single
// seam behind the registry-built clients and the forward proxy (which
// consults it through a small optional interface).
func (r *Registry) HostRewrite(urlHost string) (string, bool) {
	if !r.hasRewrites.Load() {
		return "", false
	}
	name, port := urlHost, ""
	if h, p, err := net.SplitHostPort(urlHost); err == nil {
		name, port = h, p
	}
	rt, ok := r.Lookup(name)
	if !ok {
		return "", false
	}
	rewrite, ok := rt.HostRewrite()
	if !ok {
		return "", false
	}
	if port != "" {
		// Through the forward proxy the URL is attacker-supplied and
		// SplitHostPort does not require a numeric port — refuse to join
		// arbitrary text onto the pinned rewrite.
		if !allDigits(port) {
			return "", false
		}
		rewrite = net.JoinHostPort(rewrite, port)
	}
	return rewrite, true
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// validateHostRewrite enforces RouteWithHostRewrite's contract at Add time:
// a bare host (hostname, IPv4, or unbracketed IPv6 literal) with no port and
// no bytes that could smuggle structure into an outgoing Host header. The
// request's port is preserved by HostRewrite via JoinHostPort, which also
// brackets IPv6.
func validateHostRewrite(h string) error {
	if h == "" {
		return nil // no rewrite configured
	}
	for _, c := range h {
		if c <= ' ' || c == 0x7f || strings.ContainsRune("/?#@\\", c) {
			return fmt.Errorf("host rewrite %q contains invalid host byte %q", h, c)
		}
	}
	if strings.Contains(h, ":") && net.ParseIP(h) == nil {
		return fmt.Errorf("host rewrite %q must be a bare host without a port (IPv6 literals unbracketed); the request's port is preserved automatically", h)
	}
	return nil
}

// WrapRoundTripper wraps next so requests to routes that declare a Host
// rewrite (RouteWithHostRewrite) carry the rewritten Host header.
// DefaultClient and HTTPClient are wrapped already; use this only when
// building your own http.Transport over DialContext.
func (r *Registry) WrapRoundTripper(next http.RoundTripper) http.RoundTripper {
	return &hostRewriteRT{next: next, reg: r}
}

type hostRewriteRT struct {
	next http.RoundTripper
	reg  *Registry
}

func (h *hostRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	rewrite, ok := h.reg.HostRewrite(req.URL.Host)
	if !ok {
		return h.next.RoundTrip(req)
	}
	// RoundTrippers must not mutate the caller's request.
	out := req.Clone(req.Context())
	out.Host = rewrite
	return h.next.RoundTrip(out)
}

// CloseIdleConnections delegates to the wrapped transport, so
// http.Client.CloseIdleConnections keeps working through the wrapper.
func (h *hostRewriteRT) CloseIdleConnections() {
	if c, ok := h.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// DefaultTransport returns the registry's shared http.Transport, built once
// and reused across calls so connections pool properly. Close drops its idle
// connections. For a private transport with custom options, use Transport.
func (r *Registry) DefaultTransport() *http.Transport {
	r.initHTTP()
	return r.defaultTransport
}

// DefaultClient returns the registry's shared http.Client over
// DefaultTransport. Call it freely — helpers and retry loops share one
// connection pool. Like HTTPClient it sets no Client.Timeout; use per-request
// contexts.
func (r *Registry) DefaultClient() *http.Client {
	r.initHTTP()
	return r.defaultClient
}

// Transport builds a NEW http.Transport that resolves route names via this
// registry — each call owns a private connection pool, so prefer
// DefaultTransport unless you need per-transport options. IdleConnTimeout is
// kept short (30s) so pooled connections to restarted backends age out
// quickly.
func (r *Registry) Transport(opts ...TransportOption) *http.Transport {
	t := &http.Transport{
		DialContext:         r.DialContext,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// HTTPClient builds a NEW http.Client over a NEW Transport — prefer
// DefaultClient for the shared pooled client. It deliberately sets no
// Client.Timeout: readiness waits happen inside the dial and are bounded by
// the route's ready timeout — use per-request contexts for request deadlines.
func (r *Registry) HTTPClient(opts ...TransportOption) *http.Client {
	return &http.Client{Transport: r.WrapRoundTripper(r.Transport(opts...))}
}

// CloseIdleOnUnhealthy returns an event handler that drops t's pooled
// connections whenever a backend reports unhealthy, so the next request
// redials instead of failing once on a stale connection. Wire it with
// WithEventHandler.
func CloseIdleOnUnhealthy(t *http.Transport) func(Event) {
	return func(e Event) {
		if e.Type == EventBackendUnhealthy {
			t.CloseIdleConnections()
		}
	}
}

// URL builds an http URL for a route: URL("web", 8888, "/fn")
// → "http://web:8888/fn". Port 0 or 80 is elided.
func URL(name string, port int, pathAndQuery string) string {
	return buildURL("http", name, port, 80, pathAndQuery)
}

// WSURL builds a ws URL for a route, replacing the http→ws string surgery
// tests otherwise do. Port 0 or 80 is elided.
func WSURL(name string, port int, pathAndQuery string) string {
	return buildURL("ws", name, port, 80, pathAndQuery)
}

func buildURL(scheme, name string, port, defaultPort int, pathAndQuery string) string {
	hostport := name
	if port != 0 && port != defaultPort {
		hostport = net.JoinHostPort(name, strconv.Itoa(port))
	}
	if pathAndQuery != "" && !strings.HasPrefix(pathAndQuery, "/") {
		pathAndQuery = "/" + pathAndQuery
	}
	return scheme + "://" + hostport + pathAndQuery
}
