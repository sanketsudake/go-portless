package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	"github.com/sanketsudake/go-portless/proxy"
)

// TestStartCloseRaceDoesNotLeakServer verifies Close after Start with an
// already-canceled context does not leave the proxy serving.
func TestStartCloseRaceDoesNotLeakServer(t *testing.T) {
	reg := portless.New()
	defer reg.Close()

	for range 50 {
		p := proxy.New(reg)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // canceled before Start
		addr, err := p.Start(ctx, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
		c := &http.Client{Timeout: 200 * time.Millisecond}
		resp, err := c.Get("http://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			p.Close()
			t.Fatal("proxy kept serving after canceled-ctx Start")
		}
	}
}

// TestHopByHopCommaSeparated verifies headers nominated hop-by-hop in a
// single comma-separated Connection line are stripped.
func TestHopByHopCommaSeparated(t *testing.T) {
	reg, client, _ := startProxy(t)

	var sawInternal string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawInternal = r.Header.Get("X-Internal-Auth")
	}))
	defer srv.Close()
	if _, err := reg.Add(context.Background(), "hh.test", backend.TCP(srv.Listener.Addr().String())); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://hh.test/", nil)
	req.Header.Set("Connection", "close, X-Internal-Auth")
	req.Header.Set("X-Internal-Auth", "secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sawInternal != "" {
		t.Fatalf("hop-by-hop header leaked to backend: %q", sawInternal)
	}
}

func TestProxyClosesCleanly(t *testing.T) {
	// goleak (TestMain) asserts no goroutine leak after this returns.
	reg := portless.New()
	defer reg.Close()
	p := proxy.New(reg)
	ctx, cancel := context.WithCancel(context.Background())
	addr, err := p.Start(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("addr = %q", addr)
	}
	cancel() // ctx-cancel path must stop the proxy and its watcher goroutine
	time.Sleep(20 * time.Millisecond)
}
