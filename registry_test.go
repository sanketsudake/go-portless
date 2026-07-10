package portless_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// echoListener starts a TCP server on 127.0.0.1:0 that echoes one line back.
func echoListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()
	return l
}

// roundTrip dials name:80 through the registry and verifies echo works.
func roundTrip(t *testing.T, r *portless.Registry, addr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := r.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("DialContext(%q): %v", addr, err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo mismatch: %q", buf)
	}
}

func TestAddLookupRemove(t *testing.T) {
	r := portless.New()
	defer r.Close()

	rt, err := r.Add(context.Background(), "router.fission", backend.TCP("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	if rt.Name() != "router.fission" {
		t.Fatalf("Name() = %q", rt.Name())
	}
	if _, ok := r.Lookup("router.fission"); !ok {
		t.Fatal("Lookup miss after Add")
	}
	if _, ok := r.Lookup("ROUTER.Fission"); !ok {
		t.Fatal("Lookup should be case-insensitive")
	}
	if got := len(r.Routes()); got != 1 {
		t.Fatalf("Routes() len = %d", got)
	}

	if _, err := r.Add(context.Background(), "router.fission", backend.TCP("127.0.0.1:1")); !errors.Is(err, portless.ErrRouteExists) {
		t.Fatalf("duplicate Add err = %v, want ErrRouteExists", err)
	}
	if _, err := r.Add(context.Background(), "", backend.TCP("127.0.0.1:1")); err == nil {
		t.Fatal("empty name should be rejected")
	}

	if err := r.Remove(context.Background(), "router.fission"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("router.fission"); ok {
		t.Fatal("Lookup hit after Remove")
	}
	if err := r.Remove(context.Background(), "router.fission"); !errors.Is(err, portless.ErrRouteNotFound) {
		t.Fatalf("Remove missing err = %v, want ErrRouteNotFound", err)
	}
}

func TestDialRegisteredName(t *testing.T) {
	l := echoListener(t)
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "echo.test", backend.TCP(l.Addr().String())); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, r, "echo.test:80")
}

func TestDialUnknownNameFallsBack(t *testing.T) {
	l := echoListener(t)
	r := portless.New()
	defer r.Close()
	// no route registered; address is a real TCP addr → fallback net.Dialer
	roundTrip(t, r, l.Addr().String())
}

func TestDialUnknownNameStrict(t *testing.T) {
	r := portless.New(portless.WithStrict())
	defer r.Close()
	_, err := r.DialContext(context.Background(), "tcp", "nope.test:80")
	if !errors.Is(err, portless.ErrRouteNotFound) {
		t.Fatalf("err = %v, want ErrRouteNotFound", err)
	}
}

func TestDialClosedRegistry(t *testing.T) {
	r := portless.New()
	r.Close()
	if _, err := r.DialContext(context.Background(), "tcp", "x.test:80"); !errors.Is(err, portless.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if _, err := r.Add(context.Background(), "x.test", backend.TCP("127.0.0.1:1")); !errors.Is(err, portless.ErrClosed) {
		t.Fatalf("Add err = %v, want ErrClosed", err)
	}
}

// notReadyBackend fails with a Retryable error until ready is set, then dials real.
type notReadyBackend struct {
	mu    sync.Mutex
	addr  string
	tries int
}

func (b *notReadyBackend) setAddr(addr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addr = addr
}

func (b *notReadyBackend) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	b.mu.Lock()
	addr := b.addr
	b.tries++
	b.mu.Unlock()
	if addr == "" {
		return nil, portless.Retryable(errors.New("backend starting"))
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func TestDialBlocksUntilReady(t *testing.T) {
	b := &notReadyBackend{}
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "slow.test", b); err != nil {
		t.Fatal(err)
	}

	l := echoListener(t)
	go func() {
		time.Sleep(150 * time.Millisecond)
		b.setAddr(l.Addr().String())
	}()

	start := time.Now()
	roundTrip(t, r, "slow.test:80")
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("dial returned too early (%v); readiness wait not applied", elapsed)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tries < 2 {
		t.Fatalf("backend tries = %d, want >= 2 (retry loop)", b.tries)
	}
}

func TestDialReadyTimeout(t *testing.T) {
	b := &notReadyBackend{} // never becomes ready
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "never.test", b, portless.RouteWithReadyTimeout(120*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := r.DialContext(context.Background(), "tcp", "never.test:80")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "never.test") || !strings.Contains(err.Error(), "backend starting") {
		t.Fatalf("error should name the route and the last backend error, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("timeout not respected: %v", elapsed)
	}
}

func TestDialCtxCancel(t *testing.T) {
	b := &notReadyBackend{}
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "never.test", b); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := r.DialContext(ctx, "tcp", "never.test:80")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in chain", err)
	}
}

func TestDialNonRetryableErrorFailsFast(t *testing.T) {
	fatal := errors.New("bad config")
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "bad.test", backendFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, fatal
	})); err != nil {
		t.Fatal(err)
	}
	_, err := r.DialContext(context.Background(), "tcp", "bad.test:80")
	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want wrapped %v", err, fatal)
	}
}

type backendFunc func(ctx context.Context, network, address string) (net.Conn, error)

func (f backendFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

// lifecycleBackend records Start/Stop calls.
type lifecycleBackend struct {
	backendFunc
	mu               sync.Mutex
	started, stopped int
}

func (b *lifecycleBackend) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.started++
	return nil
}

func (b *lifecycleBackend) Stop(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped++
	return nil
}

func TestBackendLifecycle(t *testing.T) {
	b := &lifecycleBackend{backendFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("unused")
	}}
	r := portless.New()
	if _, err := r.Add(context.Background(), "lc.test", b); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	if b.started != 1 {
		t.Fatalf("started = %d, want 1", b.started)
	}
	b.mu.Unlock()

	if err := r.Remove(context.Background(), "lc.test"); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	if b.stopped != 1 {
		t.Fatalf("stopped after Remove = %d, want 1", b.stopped)
	}
	b.mu.Unlock()

	// Close stops remaining backends exactly once.
	b2 := &lifecycleBackend{backendFunc: b.backendFunc}
	if _, err := r.Add(context.Background(), "lc2.test", b2); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
	b2.mu.Lock()
	if b2.stopped != 1 {
		t.Fatalf("stopped after Close = %d, want 1", b2.stopped)
	}
	b2.mu.Unlock()
}

func TestRouteReady(t *testing.T) {
	l := echoListener(t)
	r := portless.New()
	defer r.Close()
	rt, err := r.Add(context.Background(), "ready.test", backend.TCP(l.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

func TestSelfHealAfterListenerRestart(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	go acceptEcho(l)

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "heal.test", backend.TCP(addr)); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, r, "heal.test:80")

	l.Close()
	// restart on the same address after a delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		l2, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		go acceptEcho(l2)
	}()
	roundTrip(t, r, "heal.test:80") // must block through the down window and succeed
}

func acceptEcho(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			io.Copy(c, c)
		}(c)
	}
}
