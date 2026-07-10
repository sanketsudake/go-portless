package portless_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

func TestRouteWithHealthCheckGatesDial(t *testing.T) {
	l := echoListener(t)
	var healthy atomic.Bool

	r := portless.New()
	defer r.Close()
	_, err := r.Add("hc.test", backend.TCP(l.Addr().String()),
		portless.RouteWithReadyTimeout(2*time.Second),
		portless.RouteWithHealthCheck(func(ctx context.Context, dial portless.DialFunc) error {
			if !healthy.Load() {
				return fmt.Errorf("app not warmed up")
			}
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}

	// TCP accepts but health says no → dial must block, then succeed once healthy.
	go func() { time.Sleep(150 * time.Millisecond); healthy.Store(true) }()
	start := time.Now()
	roundTrip(t, r, "hc.test:80")
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("dial returned before health check passed")
	}
}

func TestRouteWithHealthCheckTimeoutNamesCause(t *testing.T) {
	l := echoListener(t)
	r := portless.New()
	defer r.Close()
	_, err := r.Add("hcfail.test", backend.TCP(l.Addr().String()),
		portless.RouteWithReadyTimeout(150*time.Millisecond),
		portless.RouteWithHealthCheck(func(ctx context.Context, dial portless.DialFunc) error {
			return fmt.Errorf("still migrating db")
		}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.DialContext(context.Background(), "tcp", "hcfail.test:80")
	if err == nil || !strings.Contains(err.Error(), "still migrating db") {
		t.Fatalf("timeout error should carry health failure, got: %v", err)
	}
}

func TestRouteWithHTTPHealth(t *testing.T) {
	var ready atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/healthz" && !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	var portNum int
	fmt.Sscanf(port, "%d", &portNum)

	r := portless.New()
	defer r.Close()
	_, err := r.Add("web.test", backend.TCP(addr),
		portless.RouteWithReadyTimeout(3*time.Second),
		portless.RouteWithHTTPHealth(portNum, "/healthz"))
	if err != nil {
		t.Fatal(err)
	}

	go func() { time.Sleep(150 * time.Millisecond); ready.Store(true) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := r.DialContext(ctx, "tcp", "web.test:80")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("dial returned before HTTP health passed")
	}
}
