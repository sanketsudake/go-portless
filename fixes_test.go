package portless_test

import (
	"context"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// TestRouteReadyWithPortMap guards the fix for Ready dialing ":0", which is
// never in a port map.
func TestRouteReadyWithPortMap(t *testing.T) {
	l := echoListener(t)
	r := portless.New()
	defer r.Close()
	rt, err := r.Add(context.Background(), "pm.test", backend.TCP(l.Addr().String()),
		portless.RouteWithPortMap(map[int]int{8080: 33112}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Ready(ctx); err != nil {
		t.Fatalf("Ready on port-mapped route: %v", err)
	}
}

// TestExplicitDeadlineOverridesReadyTimeout guards the fix where a caller's
// deadline (e.g. control /ready?timeout=) is capped by the route's
// readyTimeout.
func TestExplicitDeadlineOverridesReadyTimeout(t *testing.T) {
	b := &notReadyBackend{}
	r := portless.New()
	defer r.Close()
	// Route ready timeout is short; the caller asks for longer and the
	// backend comes up after the short timeout but before the caller's.
	if _, err := r.Add(context.Background(), "slow.test", b,
		portless.RouteWithReadyTimeout(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	l := echoListener(t)
	go func() {
		time.Sleep(300 * time.Millisecond) // past the route's 100ms cap
		b.setAddr(l.Addr().String())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := r.DialContext(ctx, "tcp", "slow.test:80")
	if err != nil {
		t.Fatalf("explicit 3s deadline should outlast the 100ms ready timeout: %v", err)
	}
	conn.Close()
}

// TestUnboundedDialUsesReadyTimeout confirms the safety cap still applies
// when the caller passes no deadline.
func TestUnboundedDialUsesReadyTimeout(t *testing.T) {
	b := &notReadyBackend{} // never ready
	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "never.test", b,
		portless.RouteWithReadyTimeout(120*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := r.DialContext(context.Background(), "tcp", "never.test:80"); err == nil {
		t.Fatal("expected timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ready timeout not applied to unbounded dial: %v", elapsed)
	}
}

// TestStrictProxyModeRejectsUnknown documents that a strict registry (which
// the daemon uses) never dials unregistered hosts — closing the open-proxy
// SSRF pivot.
func TestStrictRegistryNeverDialsUnknown(t *testing.T) {
	r := portless.New()
	defer r.Close()
	// 203.0.113.1 is TEST-NET-3 (RFC 5737) — if strict mode leaked to the
	// fallback dialer this would attempt a real connection.
	_, err := r.DialContext(context.Background(), "tcp", "203.0.113.1:80")
	if err == nil {
		t.Fatal("strict registry must not dial unregistered hosts")
	}
}
