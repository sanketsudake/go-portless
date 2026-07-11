package portless_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"testing"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

func TestURLHelpers(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{portless.URL("router.fission", 0, "/healthz"), "http://router.fission/healthz"},
		{portless.URL("router.fission", 80, "/"), "http://router.fission/"},
		{portless.URL("router.fission", 8888, "/fn?x=1"), "http://router.fission:8888/fn?x=1"},
		{portless.URL("router.fission", 8888, ""), "http://router.fission:8888"},
		{portless.WSURL("router.fission", 0, "/stream"), "ws://router.fission/stream"},
		{portless.WSURL("router.fission", 8889, "/stream"), "ws://router.fission:8889/stream"},
		{portless.URL("router.fission", 0, "healthz"), "http://router.fission/healthz"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %q, want %q", i, c.got, c.want)
		}
	}
}

func TestHTTPClientAndTransport(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "sugar")
	})}
	go srv.Serve(l)
	defer srv.Close()

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "sugar.test", backend.Listener(l)); err != nil {
		t.Fatal(err)
	}

	client := r.HTTPClient()
	if client.Timeout != 0 {
		t.Fatal("HTTPClient must not set a global timeout (readiness blocks in dial)")
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get(portless.URL("sugar.test", 0, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}

	tr := r.Transport()
	if tr.DialContext == nil {
		t.Fatal("Transport must have DialContext set")
	}
}

func TestDefaultClientSharesOnePool(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "pooled")
	})}
	go srv.Serve(l)
	defer srv.Close()

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "pool.test", backend.Listener(l)); err != nil {
		t.Fatal(err)
	}

	c1, c2 := r.DefaultClient(), r.DefaultClient()
	t1, t2 := r.DefaultTransport(), r.DefaultTransport()
	if c1 != c2 || t1 != t2 {
		t.Fatal("DefaultClient/DefaultTransport must return the same instance across calls")
	}

	var reused bool
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}

	// Two requests through two separate DefaultClient() calls: the second
	// must reuse the first's pooled connection.
	for i := range 2 {
		req, err := http.NewRequestWithContext(
			httptrace.WithClientTrace(context.Background(), trace),
			http.MethodGet, portless.URL("pool.test", 0, "/"), nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := r.DefaultClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if i == 1 && !reused {
			t.Fatal("second request did not reuse the pooled connection; DefaultClient is not sharing a transport")
		}
	}
}
