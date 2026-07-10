// Package proxy provides a standard HTTP forward proxy (CONNECT and
// absolute-form) over a portless ContextDialer, so non-Go processes reach
// named routes via HTTP_PROXY/HTTPS_PROXY — no /etc/hosts, no root.
//
// Because readiness lives in the dial, a curl through this proxy blocks
// until the backend is up instead of needing retry loops.
//
// Note: CONNECT tunnels are passthrough — TLS clients see the backend's own
// certificate, which won't match the route name unless the backend serves
// one for it. Use plain HTTP or --insecure for test backends.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	portless "github.com/sanketsudake/go-portless"
)

// Option configures a Proxy.
type Option func(*Proxy)

// WithLogger sets the proxy's logger.
func WithLogger(l *slog.Logger) Option {
	return func(p *Proxy) { p.logger = l }
}

// Proxy is an HTTP forward proxy that dials through a ContextDialer.
type Proxy struct {
	dialer    portless.ContextDialer
	logger    *slog.Logger
	transport *http.Transport

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	addr     string
}

// New creates a Proxy that routes through d.
func New(d portless.ContextDialer, opts ...Option) *Proxy {
	p := &Proxy{
		dialer: d,
		logger: slog.Default(),
		transport: &http.Transport{
			DialContext:       d.DialContext,
			DisableKeepAlives: false,
			// The proxy must not recurse through another proxy.
			Proxy: nil,
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Start listens on listenAddr (use "127.0.0.1:0" for an ephemeral port) and
// serves in a background goroutine until ctx is canceled or Close is called.
// It returns the bound address.
func (p *Proxy) Start(ctx context.Context, listenAddr string) (string, error) {
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", fmt.Errorf("proxy: listen %s: %w", listenAddr, err)
	}
	go func() {
		<-ctx.Done()
		p.Close()
	}()
	go p.Serve(l) //nolint:errcheck // Serve returns ErrServerClosed on Close
	return l.Addr().String(), nil
}

// Serve serves proxy traffic on l, blocking until Close.
func (p *Proxy) Serve(l net.Listener) error {
	srv := &http.Server{Handler: p}
	p.mu.Lock()
	p.server = srv
	p.listener = l
	p.addr = l.Addr().String()
	p.mu.Unlock()
	err := srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Addr returns the bound address, or "" before Serve/Start.
func (p *Proxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addr
}

// Close stops the proxy listener and idle connections. Hijacked CONNECT
// tunnels close with their client connections.
func (p *Proxy) Close() error {
	p.mu.Lock()
	srv := p.server
	p.mu.Unlock()
	p.transport.CloseIdleConnections()
	if srv == nil {
		return nil
	}
	return srv.Close()
}

// ServeHTTP dispatches CONNECT tunnels and absolute-form requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		p.handleConnect(w, r)
	case r.URL.IsAbs():
		p.handleAbsolute(w, r)
	default:
		http.Error(w, "portless proxy: request must be absolute-form or CONNECT", http.StatusBadRequest)
	}
}
