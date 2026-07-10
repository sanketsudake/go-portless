package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
	"github.com/sanketsudake/go-portless/control"
)

// startControl runs a control server with a fixed proxy address on a temp
// unix socket.
func startControl(t *testing.T, proxyAddr string) (string, *portless.Registry) {
	t.Helper()
	reg := portless.New(portless.WithReadyTimeout(2 * time.Second))
	t.Cleanup(func() { reg.Close() })
	sock := filepath.Join(t.TempDir(), "portless.sock")
	srv := control.NewServer(reg, control.WithProxyAddr(func() string { return proxyAddr }))
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return sock, reg
}

func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return out.String() + errOut.String(), code
}

func TestEnvOutput(t *testing.T) {
	sock, _ := startControl(t, "127.0.0.1:54321")

	out, code := runCLI(t, "env", "--socket", sock)
	if code != 0 {
		t.Fatalf("env exited %d: %s", code, out)
	}
	for _, want := range []string{
		"export HTTP_PROXY=http://127.0.0.1:54321",
		"export HTTPS_PROXY=http://127.0.0.1:54321",
		"export NO_PROXY=localhost,127.0.0.1,::1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	out, code = runCLI(t, "env", "--socket", sock, "--shell", "github")
	if code != 0 {
		t.Fatalf("env --shell github exited %d: %s", code, out)
	}
	if !strings.Contains(out, "HTTP_PROXY=http://127.0.0.1:54321") || strings.Contains(out, "export ") {
		t.Errorf("github format wrong:\n%s", out)
	}
}

func TestEnvNoProxyConfigured(t *testing.T) {
	sock, _ := startControl(t, "")
	out, code := runCLI(t, "env", "--socket", sock)
	if code == 0 {
		t.Fatalf("env should fail when daemon has no proxy, got: %s", out)
	}
}

func TestRouteAddListRm(t *testing.T) {
	sock, reg := startControl(t, "")

	out, code := runCLI(t, "route", "add", "db.test", "--tcp", "127.0.0.1:5432", "--socket", sock)
	if code != 0 {
		t.Fatalf("route add exited %d: %s", code, out)
	}
	if _, ok := reg.Lookup("db.test"); !ok {
		t.Fatal("route not registered")
	}

	out, code = runCLI(t, "route", "list", "--socket", sock)
	if code != 0 || !strings.Contains(out, "db.test") || !strings.Contains(out, "tcp") {
		t.Fatalf("route list output:\n%s (exit %d)", out, code)
	}

	out, code = runCLI(t, "route", "list", "--json", "--socket", sock)
	if code != 0 {
		t.Fatalf("route list --json exited %d: %s", code, out)
	}
	var routes []control.RouteInfo
	if err := json.Unmarshal([]byte(out), &routes); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}

	if out, code = runCLI(t, "route", "rm", "db.test", "--socket", sock); code != 0 {
		t.Fatalf("route rm exited %d: %s", code, out)
	}
	if _, ok := reg.Lookup("db.test"); ok {
		t.Fatal("route still registered after rm")
	}
}

func TestStatus(t *testing.T) {
	sock, _ := startControl(t, "127.0.0.1:1")
	out, code := runCLI(t, "status", "--socket", sock)
	if code != 0 || !strings.Contains(out, "127.0.0.1:1") {
		t.Fatalf("status output:\n%s (exit %d)", out, code)
	}
	out, code = runCLI(t, "status", "--json", "--socket", sock)
	if code != 0 {
		t.Fatalf("status --json exited %d: %s", code, out)
	}
	var st control.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
}

func TestDoctor(t *testing.T) {
	sock, reg := startControl(t, "")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := reg.Add("up.test", backend.TCP(l.Addr().String())); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, "doctor", "--socket", sock, "--timeout", "2s")
	if code != 0 || !strings.Contains(out, "up.test") || !strings.Contains(out, "ready") {
		t.Fatalf("doctor output:\n%s (exit %d)", out, code)
	}
}

func TestServeEndToEnd(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "portless.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		var out bytes.Buffer
		done <- runServe(ctx, serveOptions{socket: sock, proxyAddr: "127.0.0.1:0"}, &out, &out)
	}()

	c := control.NewClient(sock)
	var st control.Status
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		st, err = c.Status(context.Background())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never came up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.ProxyAddr == "" {
		t.Fatal("daemon should report a proxy address")
	}

	// register a backend and reach it via curl-style absolute-form proxying
	web := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "via-daemon")
	})}
	wl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wl.Close()
	go web.Serve(wl)
	defer web.Close()

	cfg, _ := json.Marshal(map[string]string{"address": wl.Addr().String()})
	if err := c.AddRoute(context.Background(), control.RouteSpec{Name: "web.test", Type: "tcp", Config: cfg}); err != nil {
		t.Fatal(err)
	}

	proxyURL, err := url.Parse("http://" + st.ProxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://web.test/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body) //nolint:errcheck
	if buf.String() != "via-daemon" {
		t.Fatalf("body = %q", buf.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not shut down on ctx cancel")
	}
}
