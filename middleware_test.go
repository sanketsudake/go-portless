package portless_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// orderMW appends tag to log when the dial passes through it.
func orderMW(mu *sync.Mutex, log *[]string, tag string) portless.Middleware {
	return func(next portless.DialFunc) portless.DialFunc {
		return func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			*log = append(*log, tag)
			mu.Unlock()
			return next(ctx, network, address)
		}
	}
}

func TestMiddlewareOrder(t *testing.T) {
	l := echoListener(t)
	var mu sync.Mutex
	var log []string

	r := portless.New(portless.WithMiddleware(orderMW(&mu, &log, "registry")))
	defer r.Close()
	_, err := r.Add(context.Background(), "mw.test", backend.TCP(l.Addr().String()),
		portless.RouteWithMiddleware(orderMW(&mu, &log, "route")))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, r, "mw.test:80")

	mu.Lock()
	defer mu.Unlock()
	if len(log) != 2 || log[0] != "registry" || log[1] != "route" {
		t.Fatalf("middleware order = %v, want [registry route]", log)
	}
}

func TestMiddlewareNotAppliedToFallback(t *testing.T) {
	l := echoListener(t)
	var mu sync.Mutex
	var log []string
	r := portless.New(portless.WithMiddleware(orderMW(&mu, &log, "registry")))
	defer r.Close()
	roundTrip(t, r, l.Addr().String()) // fallback dial, no route
	mu.Lock()
	defer mu.Unlock()
	if len(log) != 0 {
		t.Fatalf("middleware ran on fallback dial: %v", log)
	}
}

func TestConnWrapper(t *testing.T) {
	l := echoListener(t)
	var mu sync.Mutex
	var wrappedRoutes []string

	r := portless.New(portless.WithMiddleware(
		portless.ConnWrapper(func(name string, c net.Conn) net.Conn {
			mu.Lock()
			wrappedRoutes = append(wrappedRoutes, name)
			mu.Unlock()
			return c
		})))
	defer r.Close()
	if _, err := r.Add(context.Background(), "wrap.test", backend.TCP(l.Addr().String())); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, r, "wrap.test:80")

	mu.Lock()
	defer mu.Unlock()
	if len(wrappedRoutes) != 1 || wrappedRoutes[0] != "wrap.test" {
		t.Fatalf("wrapped = %v, want [wrap.test]", wrappedRoutes)
	}
}

// faultMW simulates fault injection: fail the first n dials terminally.
func TestMiddlewareCanInjectFaults(t *testing.T) {
	l := echoListener(t)
	boom := errors.New("injected fault")
	r := portless.New()
	defer r.Close()
	_, err := r.Add(context.Background(), "fault.test", backend.TCP(l.Addr().String()),
		portless.RouteWithMiddleware(func(next portless.DialFunc) portless.DialFunc {
			return func(ctx context.Context, network, address string) (net.Conn, error) {
				return nil, boom
			}
		}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.DialContext(context.Background(), "tcp", "fault.test:80")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected fault", err)
	}
}

func TestRouteWithPortMap(t *testing.T) {
	l := echoListener(t)
	_, realPort, _ := net.SplitHostPort(l.Addr().String())

	var mu sync.Mutex
	var addrs []string
	rec := backendFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		addrs = append(addrs, address)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+realPort)
	})

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "pm.test", rec, portless.RouteWithPortMap(map[int]int{80: 9999})); err != nil {
		t.Fatal(err)
	}
	// mapped port rewrites the address handed to the backend
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := r.DialContext(ctx, "tcp", "pm.test:80")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	mu.Lock()
	if len(addrs) == 0 || addrs[len(addrs)-1] != "pm.test:9999" {
		t.Fatalf("backend saw %v, want last = pm.test:9999", addrs)
	}
	mu.Unlock()

	// unmapped port fails loudly, non-retryably
	_, err = r.DialContext(ctx, "tcp", "pm.test:81")
	if err == nil {
		t.Fatal("unmapped port must error")
	}
}
