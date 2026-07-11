package backend_test

import (
	"errors"
	"testing"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

func TestListenAndAdd(t *testing.T) {
	t.Parallel()
	reg := portless.New()
	defer reg.Close()

	l, err := backend.ListenAndAdd(t.Context(), reg, "svc")
	if err != nil {
		t.Fatalf("ListenAndAdd: %v", err)
	}
	defer l.Close()

	// The route is registered and its Addr matches the listener.
	rt, ok := reg.Lookup("svc")
	if !ok {
		t.Fatal("route not registered")
	}
	addr, ok := rt.Addr()
	if !ok || addr.String() != l.Addr().String() {
		t.Fatalf("route addr = %v, want %v", addr, l.Addr())
	}

	// Dial-ready at bind time (accept backlog), before anything Accepts.
	conn, err := reg.DialContext(t.Context(), "tcp", "svc:0")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
}

func TestListenAndAddDuplicateName(t *testing.T) {
	t.Parallel()
	reg := portless.New()
	defer reg.Close()
	l1, err := backend.ListenAndAdd(t.Context(), reg, "svc")
	if err != nil {
		t.Fatal(err)
	}
	defer l1.Close()
	if _, err := backend.ListenAndAdd(t.Context(), reg, "svc"); !errors.Is(err, portless.ErrRouteExists) {
		t.Fatalf("err = %v, want ErrRouteExists", err)
	}
	// The route still points at l1 (the failed call must not disturb it).
	rt, _ := reg.Lookup("svc")
	if addr, ok := rt.Addr(); !ok || addr.String() != l1.Addr().String() {
		t.Fatalf("route addr = %v, want %v", addr, l1.Addr())
	}
}
