package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	portless "github.com/sanketsudake/go-portless"
)

// Version is stamped by the CLI build; the library default is "dev".
var Version = "dev"

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithProxyAddr supplies the forward proxy's address for status responses.
func WithProxyAddr(f func() string) ServerOption {
	return func(s *Server) { s.proxyAddr = f }
}

// Server exposes a Registry over the control API.
type Server struct {
	reg       *portless.Registry
	proxyAddr func() string
	started   time.Time
	http      *http.Server

	mu    sync.Mutex
	specs map[string]RouteSpec // routes added via the API, for listing
	l     net.Listener
}

// NewServer creates a control server managing reg.
func NewServer(reg *portless.Registry, opts ...ServerOption) *Server {
	s := &Server{
		reg:       reg,
		proxyAddr: func() string { return "" },
		started:   time.Now(),
		specs:     map[string]RouteSpec{},
	}
	for _, o := range opts {
		o(s)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/routes", s.handleListRoutes)
	mux.HandleFunc("POST /v1/routes", s.handleAddRoute)
	mux.HandleFunc("DELETE /v1/routes/{name}", s.handleRemoveRoute)
	mux.HandleFunc("GET /v1/routes/{name}/ready", s.handleReady)
	s.http = &http.Server{Handler: mux}
	return s
}

// Serve serves the control API on l (typically a unix socket listener),
// blocking until Close.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	s.l = l
	s.mu.Unlock()
	err := s.http.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the control server.
func (s *Server) Close() error { return s.http.Close() }

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Status{
		PID:        os.Getpid(),
		Version:    Version,
		ProxyAddr:  s.proxyAddr(),
		RouteCount: len(s.reg.Routes()),
		UptimeSec:  int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	routes := s.reg.Routes()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RouteInfo, 0, len(routes))
	for _, rt := range routes {
		info := RouteInfo{Name: rt.Name(), Type: "custom"}
		if spec, ok := s.specs[strings.ToLower(rt.Name())]; ok {
			info.Type, info.Config = spec.Type, spec.Config
		}
		if addr, ok := rt.Addr(); ok {
			info.Addr = addr.String()
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddRoute(w http.ResponseWriter, r *http.Request) {
	var spec RouteSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		httpError(w, http.StatusBadRequest, codeBadRequest, fmt.Errorf("decode spec: %w", err))
		return
	}
	if spec.Name == "" || spec.Type == "" {
		httpError(w, http.StatusBadRequest, codeBadRequest, errors.New("spec requires name and type"))
		return
	}
	factory, ok := lookupFactory(spec.Type)
	if !ok {
		httpError(w, http.StatusBadRequest, codeBadRequest, fmt.Errorf("unknown backend type %q", spec.Type))
		return
	}
	b, err := factory(spec.Config)
	if err != nil {
		httpError(w, http.StatusBadRequest, codeBadRequest, err)
		return
	}
	if _, err := s.reg.Add(r.Context(), spec.Name, b); err != nil {
		if errors.Is(err, portless.ErrRouteExists) {
			httpError(w, http.StatusConflict, codeRouteExists, err)
		} else {
			httpError(w, http.StatusInternalServerError, codeInternal, err)
		}
		return
	}
	s.mu.Lock()
	s.specs[strings.ToLower(spec.Name)] = spec
	s.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleRemoveRoute(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.reg.Remove(r.Context(), name); err != nil {
		if errors.Is(err, portless.ErrRouteNotFound) {
			httpError(w, http.StatusNotFound, codeRouteNotFound, err)
		} else {
			httpError(w, http.StatusInternalServerError, codeInternal, err)
		}
		return
	}
	s.mu.Lock()
	delete(s.specs, strings.ToLower(name))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rt, ok := s.reg.Lookup(name)
	if !ok {
		httpError(w, http.StatusNotFound, codeRouteNotFound, fmt.Errorf("route %q: %w", name, portless.ErrRouteNotFound))
		return
	}
	ctx := r.Context()
	if t := r.URL.Query().Get("timeout"); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			httpError(w, http.StatusBadRequest, codeBadRequest, fmt.Errorf("bad timeout: %w", err))
			return
		}
		var cancel func()
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	if err := rt.Ready(ctx); err != nil {
		// A deadline reached means "still not ready"; anything else (registry
		// closed, terminal backend/config error) is not a timeout.
		if errors.Is(err, context.DeadlineExceeded) {
			httpError(w, http.StatusGatewayTimeout, codeNotReady, err)
		} else {
			httpError(w, http.StatusBadGateway, codeNotReady, err)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// Machine-readable error codes carried in error responses, so the client can
// map failures back to sentinel errors without matching on message text.
const (
	codeRouteExists   = "route_exists"
	codeRouteNotFound = "route_not_found"
	codeNotReady      = "not_ready"
	codeBadRequest    = "bad_request"
	codeInternal      = "internal"
)

func httpError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": code})
}
