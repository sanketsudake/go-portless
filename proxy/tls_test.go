package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	"github.com/sanketsudake/go-portless/ca"
	"github.com/sanketsudake/go-portless/proxy"
)

// TestTLSTerminationVerifiesWithCA drives https://web through the proxy with
// TLS termination on, verifying the presented cert against the local CA. The
// backend is plain HTTP; the proxy adds the TLS layer.
func TestTLSTerminationVerifiesWithCA(t *testing.T) {
	reg := portless.New(portless.WithReadyTimeout(2 * time.Second))
	t.Cleanup(func() { reg.Close() })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s", r.Host)
	}))
	defer srv.Close()
	if _, err := reg.Add(context.Background(), "web", backend.TCP(srv.Listener.Addr().String())); err != nil {
		t.Fatal(err)
	}

	authority, err := ca.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	p := proxy.New(reg, proxy.WithTLS(authority))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr, err := p.Start(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	// Client trusts the CA and routes through the proxy.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(authority.CertPEM())
	proxyURL, _ := url.Parse("http://" + addr)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://web/hello")
	if err != nil {
		t.Fatalf("https GET through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello web" {
		t.Fatalf("body = %q, want 'hello web'", body)
	}
}
