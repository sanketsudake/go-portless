package portless_test

import (
	"context"
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
