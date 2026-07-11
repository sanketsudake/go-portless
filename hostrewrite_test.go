package portless_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// rebindGuardServer mimics servers with DNS-rebinding protection: it rejects
// requests whose Host is not a loopback name with 403 (as e.g. the MCP go-sdk
// does for port-forwarded traffic).
func rebindGuardServer(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			http.Error(w, "possible DNS-rebinding attack", http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "welcome "+r.Host)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return l
}

func TestHostRewriteDefeatsRebindingGuard(t *testing.T) {
	l := rebindGuardServer(t)

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "guarded.test", backend.Listener(l)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(context.Background(), "rewritten.test", backend.Listener(l),
		portless.RouteWithHostRewrite("127.0.0.1")); err != nil {
		t.Fatal(err)
	}

	client := r.DefaultClient()

	// Without a rewrite the route name reaches the server as Host → 403.
	resp, err := client.Get(portless.URL("guarded.test", 0, "/"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unrewritten route: status = %d, want 403 (guard should trip)", resp.StatusCode)
	}

	// With the rewrite the server sees a loopback Host → 200.
	resp, err = client.Get(portless.URL("rewritten.test", 0, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rewritten route: status = %d, want 200", resp.StatusCode)
	}
}

func TestHostRewritePreservesPort(t *testing.T) {
	l := rebindGuardServer(t)

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "ported.test", backend.Listener(l),
		portless.RouteWithHostRewrite("127.0.0.1")); err != nil {
		t.Fatal(err)
	}

	resp, err := r.DefaultClient().Get(portless.URL("ported.test", 8443, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body [64]byte
	n, _ := resp.Body.Read(body[:])
	if got := string(body[:n]); !strings.Contains(got, "127.0.0.1:8443") {
		t.Fatalf("server saw Host %q, want the rewritten host with the original port", got)
	}
}

func TestHostRewriteValidation(t *testing.T) {
	r := portless.New()
	defer r.Close()
	l := echoListener(t)

	bad := []string{
		"127.0.0.1:8443", // port not allowed (request port is preserved)
		"evil/../path",   // path bytes
		"host\r\nX: y",   // header smuggling
		"a b",            // space
		"user@host",      // userinfo
	}
	for _, h := range bad {
		if _, err := r.Add(context.Background(), "bad.test", backend.Listener(l),
			portless.RouteWithHostRewrite(h)); err == nil {
			t.Errorf("Add accepted invalid host rewrite %q", h)
			_ = r.Remove(context.Background(), "bad.test")
		}
	}

	for i, h := range []string{"127.0.0.1", "localhost", "::1", "my-host.internal"} {
		name := fmt.Sprintf("good%d.test", i)
		if _, err := r.Add(context.Background(), name, backend.Listener(l),
			portless.RouteWithHostRewrite(h)); err != nil {
			t.Errorf("Add rejected valid host rewrite %q: %v", h, err)
		}
	}
}

func TestHostRewriteRefusesNonNumericPort(t *testing.T) {
	r := portless.New()
	defer r.Close()
	l := echoListener(t)
	if _, err := r.Add(context.Background(), "pinned.test", backend.Listener(l),
		portless.RouteWithHostRewrite("127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	// A proxy client controls the URL; SplitHostPort accepts non-numeric
	// "ports", which must not be joined onto the pinned rewrite.
	if got, ok := r.HostRewrite("pinned.test:evil.example"); ok {
		t.Fatalf("HostRewrite joined attacker text onto the pinned host: %q", got)
	}
	if got, ok := r.HostRewrite("pinned.test:8443"); !ok || got != "127.0.0.1:8443" {
		t.Fatalf("HostRewrite(numeric port) = %q, %v; want 127.0.0.1:8443, true", got, ok)
	}
}

func TestStrictOptionStillOverridesFallback(t *testing.T) {
	// v0.1 semantics: WithStrict wins over WithFallbackDialer in either
	// order. A v0.2 upgrade must not silently open the fallback path.
	r := portless.New(portless.WithStrict(), portless.WithFallbackDialer(nil))
	defer r.Close()
	if _, err := r.DialContext(context.Background(), "tcp", "unregistered.test:80"); !errors.Is(err, portless.ErrRouteNotFound) {
		t.Fatalf("err = %v, want ErrRouteNotFound (WithStrict must override the fallback)", err)
	}
}

func TestHostRewriteDoesNotMutateCallerRequest(t *testing.T) {
	l := rebindGuardServer(t)

	r := portless.New()
	defer r.Close()
	if _, err := r.Add(context.Background(), "immutable.test", backend.Listener(l),
		portless.RouteWithHostRewrite("127.0.0.1")); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodGet, portless.URL("immutable.test", 0, "/"), nil)
	if err != nil {
		t.Fatal(err)
	}
	before := req.Host // NewRequest seeds Host from the URL
	resp, err := r.DefaultClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Host != before {
		t.Fatalf("caller's request was mutated: Host = %q, want %q", req.Host, before)
	}
}
