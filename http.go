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

// Transport returns an http.Transport that resolves route names via this
// registry. IdleConnTimeout is kept short (30s) so pooled connections to
// restarted backends age out quickly.
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

// HTTPClient returns an http.Client over Transport. It deliberately sets no
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

// URL builds an http URL for a route: URL("router.fission", 8888, "/fn")
// → "http://router.fission:8888/fn". Port 0 or 80 is elided.
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
