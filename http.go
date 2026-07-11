package portless

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TransportOption customizes the http.Transport built by Transport/HTTPClient.
type TransportOption func(*http.Transport)

// initHTTP builds the shared transport/client exactly once. sync.Once makes
// it safe against concurrent DefaultTransport/DefaultClient/Close calls.
func (r *Registry) initHTTP() {
	r.httpOnce.Do(func() {
		r.defaultTransport = r.Transport()
		r.defaultClient = &http.Client{Transport: r.defaultTransport}
	})
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
	return &http.Client{Transport: r.Transport(opts...)}
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
