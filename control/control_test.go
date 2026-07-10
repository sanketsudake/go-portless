package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	"github.com/sanketsudake/go-portless/control"
)

// startServer runs a control server over a fresh registry on a temp unix
// socket and returns a client for it.
func startServer(t *testing.T) (*portless.Registry, *control.Client) {
	t.Helper()
	reg := portless.New(portless.WithStrict(), portless.WithReadyTimeout(time.Second))
	t.Cleanup(func() { reg.Close() })

	sock := filepath.Join(t.TempDir(), "portless.sock")
	srv := control.NewServer(reg, control.WithProxyAddr(func() string { return "127.0.0.1:9999" }))
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })

	return reg, control.NewClient(sock)
}

func TestStatus(t *testing.T) {
	_, c := startServer(t)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.ProxyAddr != "127.0.0.1:9999" {
		t.Fatalf("proxyAddr = %q", st.ProxyAddr)
	}
	if st.PID == 0 {
		t.Fatal("pid must be set")
	}
	if st.RouteCount != 0 {
		t.Fatalf("routeCount = %d", st.RouteCount)
	}
}

func TestRouteCRUD(t *testing.T) {
	_, c := startServer(t)
	ctx := context.Background()

	spec := control.RouteSpec{
		Name:   "db.test",
		Type:   "tcp",
		Config: json.RawMessage(`{"address":"127.0.0.1:5432"}`),
	}
	if err := c.AddRoute(ctx, spec); err != nil {
		t.Fatal(err)
	}

	routes, err := c.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Name != "db.test" || routes[0].Type != "tcp" {
		t.Fatalf("routes = %+v", routes)
	}

	// duplicate → 409 mapped to ErrRouteExists
	if err := c.AddRoute(ctx, spec); !errors.Is(err, portless.ErrRouteExists) {
		t.Fatalf("dup add err = %v, want ErrRouteExists", err)
	}

	if err := c.RemoveRoute(ctx, "db.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveRoute(ctx, "db.test"); !errors.Is(err, portless.ErrRouteNotFound) {
		t.Fatalf("rm missing err = %v, want ErrRouteNotFound", err)
	}
}

func TestAddRouteUnknownType(t *testing.T) {
	_, c := startServer(t)
	err := c.AddRoute(context.Background(), control.RouteSpec{Name: "x", Type: "warp-drive"})
	if err == nil {
		t.Fatal("unknown type must fail")
	}
}

func TestAddRouteActuallyRoutes(t *testing.T) {
	reg, c := startServer(t)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "routed")
	})}
	go srv.Serve(l)
	defer srv.Close()

	cfg, _ := json.Marshal(map[string]string{"address": l.Addr().String()})
	if err := c.AddRoute(context.Background(), control.RouteSpec{Name: "app.test", Type: "tcp", Config: cfg}); err != nil {
		t.Fatal(err)
	}

	client := reg.HTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://app.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestWaitReady(t *testing.T) {
	reg, c := startServer(t)

	f := backend.Future()
	if _, err := reg.Add("boot.test", f); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go acceptAndClose(l)

	go func() {
		time.Sleep(150 * time.Millisecond)
		f.SetListener(l)
	}()
	start := time.Now()
	if err := c.WaitReady(context.Background(), "boot.test", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("WaitReady should have blocked")
	}

	// unknown route errors
	if err := c.WaitReady(context.Background(), "ghost.test", time.Second); err == nil {
		t.Fatal("WaitReady on unknown route must fail")
	}
}

func acceptAndClose(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

func TestDefaultSocketPath(t *testing.T) {
	p := control.DefaultSocketPath()
	if p == "" {
		t.Fatal("DefaultSocketPath must not be empty")
	}
	t.Setenv("PORTLESS_SOCKET", "/tmp/custom.sock")
	if got := control.DefaultSocketPath(); got != "/tmp/custom.sock" {
		t.Fatalf("PORTLESS_SOCKET override ignored: %q", got)
	}
}
