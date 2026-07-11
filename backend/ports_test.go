package backend_test

import (
	"testing"

	"github.com/sanketsudake/go-portless/backend"
)

func TestReservePortsDistinct(t *testing.T) {
	t.Parallel()
	ports, err := backend.ReservePorts(10)
	if err != nil {
		t.Fatalf("ReservePorts: %v", err)
	}
	if len(ports) != 10 {
		t.Fatalf("got %d ports, want 10", len(ports))
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if p <= 0 || p > 65535 {
			t.Fatalf("invalid port %d", p)
		}
		if seen[p] {
			t.Fatalf("duplicate port %d in %v", p, ports)
		}
		seen[p] = true
	}
}

func TestReservePortsRejectsNonPositive(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, -1} {
		if _, err := backend.ReservePorts(n); err == nil {
			t.Fatalf("ReservePorts(%d) should error", n)
		}
	}
}
