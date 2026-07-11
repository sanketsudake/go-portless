package proxy_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	"github.com/sanketsudake/go-portless/proxy"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// startProxy runs a proxy over a strict registry and returns a client that
// uses it, plus the registry.
func startProxy(t *testing.T) (*portless.Registry, *http.Client, string) {
	t.Helper()
	reg := portless.New(portless.WithReadyTimeout(2 * time.Second))
	t.Cleanup(func() { reg.Close() })

	p := proxy.New(reg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr, err := p.Start(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })

	proxyURL, _ := url.Parse("http://" + addr)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	t.Cleanup(client.CloseIdleConnections)
	return reg, client, addr
}

func TestAbsoluteFormHTTP(t *testing.T) {
	reg, client, _ := startProxy(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "host=%s path=%s", r.Host, r.URL.Path)
	}))
	defer srv.Close()
	if _, err := reg.Add(context.Background(), "web.test", backend.TCP(srv.Listener.Addr().String())); err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get("http://web.test/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "host=web.test path=/hello" {
		t.Fatalf("body = %q", body)
	}
}

func TestConnectTunnelTLS(t *testing.T) {
	reg, client, _ := startProxy(t)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secure")
	}))
	defer srv.Close()
	if _, err := reg.Add(context.Background(), "secure.test", backend.TCP(srv.Listener.Addr().String())); err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get("https://secure.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secure" {
		t.Fatalf("body = %q", body)
	}
}

func TestAbsoluteFormAppliesHostRewrite(t *testing.T) {
	reg, client, _ := startProxy(t)

	// Guarded server: 403 unless Host is loopback (DNS-rebinding heuristic).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "localhost" && host != "127.0.0.1" {
			http.Error(w, "rebind guard", http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "rewritten")
	}))
	defer srv.Close()

	if _, err := reg.Add(context.Background(), "guarded.test", backend.TCP(srv.Listener.Addr().String())); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(context.Background(), "rewritten.test", backend.TCP(srv.Listener.Addr().String()),
		portless.RouteWithHostRewrite("127.0.0.1")); err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get("http://guarded.test/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unrewritten: status = %d, want 403", resp.StatusCode)
	}

	resp, err = client.Get("http://rewritten.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "rewritten" {
		t.Fatalf("rewritten: status = %d body = %q, want 200 %q", resp.StatusCode, body, "rewritten")
	}
}

func TestUnknownRoute502(t *testing.T) {
	_, client, _ := startProxy(t)
	resp, err := client.Get("http://nope.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "route not found") {
		t.Fatalf("502 body should carry the dial error, got %q", body)
	}
}

func TestConnectUnknownRoute502(t *testing.T) {
	_, _, addr := startProxy(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT nope.test:443 HTTP/1.1\r\nHost: nope.test:443\r\n\r\n")
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "502") {
		t.Fatalf("CONNECT to unknown route should 502, got %q", buf[:n])
	}
}

func TestNonAbsoluteRequest400(t *testing.T) {
	_, _, addr := startProxy(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /origin-form HTTP/1.1\r\nHost: whatever\r\n\r\n")
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "400") {
		t.Fatalf("origin-form request should 400, got %q", buf[:n])
	}
}

func TestCurlStyleReadinessBlocking(t *testing.T) {
	reg, client, _ := startProxy(t)

	f := backend.Future()
	if _, err := reg.Add(context.Background(), "slowstart.test", f); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "up")
	}))
	defer srv.Close()

	go func() {
		time.Sleep(150 * time.Millisecond)
		f.Set(srv.Listener.Addr().String())
	}()
	start := time.Now()
	resp, err := client.Get("http://slowstart.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("proxy request should have blocked until backend ready")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "up" {
		t.Fatalf("body = %q", body)
	}
}
