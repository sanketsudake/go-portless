package backend_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

func serveHTTP(t *testing.T, l net.Listener, body string) {
	t.Helper()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
}

func get(t *testing.T, r *portless.Registry, url, want string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: r.DialContext}}
	defer client.CloseIdleConnections()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("GET %s = %q, want %q", url, b, want)
	}
}

func TestListenerBackend(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serveHTTP(t, l, "from-listener")

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "lst.test", backend.Listener(l)); err != nil {
		t.Fatal(err)
	}
	get(t, r, "http://lst.test/", "from-listener")
}

func TestMemBackendZeroTCP(t *testing.T) {
	b, l := backend.Mem()
	serveHTTP(t, l, "from-mem")

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "mem.test", b); err != nil {
		t.Fatal(err)
	}
	get(t, r, "http://mem.test/", "from-mem")
}

func TestMemBackendClosedListener(t *testing.T) {
	b, l := backend.Mem()
	l.Close()
	_, err := b.DialContext(context.Background(), "tcp", "mem.test:80")
	if err == nil {
		t.Fatal("dial after listener close must fail")
	}
}

func TestFutureBackendBlocksUntilSetListener(t *testing.T) {
	f := backend.Future()
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "fut.test", f); err != nil {
		t.Fatal(err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serveHTTP(t, l, "from-future")

	go func() {
		time.Sleep(150 * time.Millisecond)
		f.SetListener(l)
	}()
	start := time.Now()
	get(t, r, "http://fut.test/", "from-future")
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("dial should have blocked until SetListener")
	}
}

func TestFutureBackendSetAddr(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serveHTTP(t, l, "hi")

	f := backend.Future()
	f.Set(l.Addr().String())
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "fa.test", f); err != nil {
		t.Fatal(err)
	}
	get(t, r, "http://fa.test/", "hi")
}

func TestFutureBackendUnsetIsRetryable(t *testing.T) {
	f := backend.Future()
	_, err := f.DialContext(context.Background(), "tcp", "x:80")
	if err == nil || !portless.IsRetryable(err) {
		t.Fatalf("unset Future dial err = %v, want retryable", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("unexpected ctx error")
	}
}
