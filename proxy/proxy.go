// Package proxy provides a standard HTTP forward proxy (CONNECT and
// absolute-form) over a portless ContextDialer, so non-Go processes reach
// named routes via HTTP_PROXY/HTTPS_PROXY — no /etc/hosts, no root.
//
// Because readiness lives in the dial, a curl through this proxy blocks
// until the backend is up instead of needing retry loops.
//
// Security: the proxy reaches whatever its dialer reaches. Front it with a
// strict Registry (portless.WithStrict) so only registered routes are
// reachable; a non-strict Registry falls back to real network dials, turning
// the proxy into an open forward proxy (an SSRF pivot for any local process).
//
// Note: CONNECT tunnels are passthrough — TLS clients see the backend's own
// certificate, which won't match the route name unless the backend serves
// one for it. Use plain HTTP or --insecure for test backends.
package proxy

import (
	"context"
	"crypto/tls"
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

// CertIssuer supplies TLS certificates for route names, keyed by SNI.
// *ca.CA implements it.
type CertIssuer interface {
	// ServerTLSConfig returns a config whose GetCertificate issues a leaf
	// certificate for the requested SNI name.
	ServerTLSConfig() *tls.Config
}

// WithTLS makes the proxy terminate TLS: a CONNECT to name:443 is answered
// with a certificate for name (from issuer) and the decrypted HTTP is
// forwarded to the backend. Without it, CONNECT is passthrough. Clients must
// trust the issuer's CA (see `portless ca install`).
func WithTLS(issuer CertIssuer) Option {
	return func(p *Proxy) { p.tls = issuer }
}

// Proxy is an HTTP forward proxy that dials through a ContextDialer.
type Proxy struct {
	dialer    portless.ContextDialer
	logger    *slog.Logger
	transport *http.Transport
	server    *http.Server // built in New; Close is always able to reach it
	tls       CertIssuer   // non-nil enables TLS termination on CONNECT

	closeOnce sync.Once
	done      chan struct{} // closed by Close; stops the Start watcher goroutine
}

// New creates a Proxy that routes through d.
func New(d portless.ContextDialer, opts ...Option) *Proxy {
	p := &Proxy{
		dialer: d,
		logger: slog.Default(),
		transport: &http.Transport{
			DialContext: d.DialContext,
			Proxy:       nil, // the proxy must not recurse through another proxy
		},
		done: make(chan struct{}),
	}
	for _, o := range opts {
		o(p)
	}
	p.server = &http.Server{Handler: p}
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
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-p.done:
		}
	}()
	go p.Serve(l) //nolint:errcheck // Serve returns nil on Close
	return l.Addr().String(), nil
}

// Serve serves proxy traffic on l, blocking until Close.
func (p *Proxy) Serve(l net.Listener) error {
	err := p.server.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the proxy listener and idle connections. Idempotent.
func (p *Proxy) Close() error {
	var err error
	p.closeOnce.Do(func() {
		close(p.done)
		p.transport.CloseIdleConnections()
		err = p.server.Close()
	})
	return err
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
