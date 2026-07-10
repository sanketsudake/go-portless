package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sanketsudake/go-portless/control"
)

// TestHelperProcess is re-executed as the child of `portless run`. It binds
// $PORT, serves "ok" on /, and exits after the first request so the parent
// run completes.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_PORTLESS_HELPER") != "1" {
		return
	}
	port := os.Getenv("PORT")
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper listen:", err)
		os.Exit(1)
	}
	done := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
		close(done)
	})}
	go srv.Serve(l)
	<-done
	srv.Close()
	os.Exit(0)
}

// startRealDaemon runs a full daemon (control API + real forward proxy) so
// run/alias have something to register into and reach through.
func startRealDaemon(t *testing.T) (socket, proxyAddr string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "plrun")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket = filepath.Join(dir, "p.sock")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go runServe(ctx, serveOptions{socket: socket, proxyAddr: "127.0.0.1:0"}, io.Discard, io.Discard)

	c := control.NewClient(socket)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := c.Status(context.Background())
		if err == nil {
			return socket, st.ProxyAddr
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never came up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func proxyClient(t *testing.T, proxyAddr string) *http.Client {
	t.Helper()
	pu, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestAliasRegistersStaticRoute(t *testing.T) {
	socket, proxyAddr := startRealDaemon(t)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "aliased")
	})}
	go srv.Serve(l)
	defer srv.Close()

	out, code := runCLI(t, "alias", "web", l.Addr().String(), "--socket", socket)
	if code != 0 {
		t.Fatalf("alias exited %d: %s", code, out)
	}

	resp, err := proxyClient(t, proxyAddr).Get("http://web/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "aliased" {
		t.Fatalf("body = %q, want aliased", body)
	}
}

func TestRunSpawnsRegistersAndCleansUp(t *testing.T) {
	socket, proxyAddr := startRealDaemon(t)

	// The spawned child (this test binary in helper mode) inherits the
	// process env, so flag it into helper mode here.
	os.Setenv("GO_PORTLESS_HELPER", "1")
	t.Cleanup(func() { os.Unsetenv("GO_PORTLESS_HELPER") })

	// The child binds $PORT and exits after one request.
	helper := []string{os.Args[0], "-test.run=TestHelperProcess"}
	runArgs := append([]string{"run", "web", "--socket", socket, "--"}, helper...)

	// run blocks until the child exits, so drive it in a goroutine.
	done := make(chan int, 1)
	outCh := make(chan string, 1)
	go func() {
		out, code := runCLI(t, runArgs...)
		outCh <- out
		done <- code
	}()

	// The route should become reachable through the proxy (proves run picked a
	// port, set $PORT, spawned the child, and registered the name).
	client := proxyClient(t, proxyAddr)
	var got string
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get("http://web/")
		if err == nil && resp.StatusCode == http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			got = string(b)
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			select {
			case o := <-outCh:
				t.Fatalf("route never reachable (last err=%v); run output: %s", err, o)
			default:
				t.Fatalf("route never reachable (last err=%v); run still running", err)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != "ok" {
		t.Fatalf("body = %q, want ok", got)
	}

	// The helper exits after that request; run should return and deregister.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after child exit")
	}

	routes, err := control.NewClient(socket).Routes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Name == "web" {
			t.Fatal("run did not deregister the route on child exit")
		}
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	socket, _ := startRealDaemon(t)
	// `sh -c 'exit 3'` never binds a port, but run should still spawn it and
	// surface its exit code.
	_, code := runCLI(t, "run", "failer", "--socket", socket, "--", "sh", "-c", "exit 3")
	if code != 3 {
		t.Fatalf("run exit code = %d, want 3 (child's code)", code)
	}
	// route cleaned up even on nonzero exit
	routes, _ := control.NewClient(socket).Routes(context.Background())
	for _, r := range routes {
		if r.Name == "failer" {
			t.Fatal("route not cleaned up after failing child")
		}
	}
}

func TestFreePortReturnsUsable(t *testing.T) {
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if p <= 0 || p > 65535 {
		t.Fatalf("freePort = %d", p)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		t.Fatalf("freePort returned an unusable port %d: %v", p, err)
	}
	l.Close()
}
