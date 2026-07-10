// Package control implements the daemon control plane: an HTTP/JSON API
// served over a unix socket for managing routes, plus the client the CLI
// uses. Authentication is filesystem permissions on the socket.
package control

import (
	"encoding/json"
	"fmt"
	"sync"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// RouteSpec is the wire form of a route: a name, a backend type, and
// type-specific configuration.
type RouteSpec struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// RouteInfo describes a registered route in list responses.
type RouteInfo struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Status is the daemon status response.
type Status struct {
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	ProxyAddr  string `json:"proxyAddr,omitempty"`
	RouteCount int    `json:"routeCount"`
	UptimeSec  int64  `json:"uptimeSec"`
}

// BackendFactory builds a Backend from a RouteSpec's Config.
type BackendFactory func(cfg json.RawMessage) (portless.Backend, error)

var (
	factoriesMu sync.RWMutex
	factories   = map[string]BackendFactory{}
)

// RegisterBackendType makes a backend type available to control servers.
// The k8s module registers "k8s" via its Register function; "tcp" is
// built in. Registering a duplicate type panics.
func RegisterBackendType(typ string, f BackendFactory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	if _, dup := factories[typ]; dup {
		panic("control: backend type already registered: " + typ)
	}
	factories[typ] = f
}

func lookupFactory(typ string) (BackendFactory, bool) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	f, ok := factories[typ]
	return f, ok
}

type tcpConfig struct {
	Address string `json:"address"`
}

func init() {
	RegisterBackendType("tcp", func(cfg json.RawMessage) (portless.Backend, error) {
		var c tcpConfig
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, fmt.Errorf("tcp config: %w", err)
		}
		if c.Address == "" {
			return nil, fmt.Errorf("tcp config: address is required")
		}
		return backend.TCP(c.Address), nil
	})
}
