package portless_test

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// startEchoOnce starts a server that reads until client EOF, echoes
// everything back, then closes — a connection-close-delimited response.
func startEchoOnce(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				b, _ := io.ReadAll(c) // returns at client half-close (EOF)
				_, _ = c.Write(b)
			}(c)
		}
	}()
	return l
}

func TestListenLocalBridgesAndHalfCloses(t *testing.T) {
	t.Parallel()
	upstream := startEchoOnce(t)
	reg := portless.New(portless.WithStrict())
	defer reg.Close()
	if _, err := reg.Add(t.Context(), "svc", backend.Listener(upstream)); err != nil {
		t.Fatal(err)
	}

	bridge, err := reg.ListenLocal("svc")
	if err != nil {
		t.Fatalf("ListenLocal: %v", err)
	}

	conn, err := net.Dial("tcp", bridge.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Client half-close must propagate to the server (server's ReadAll
	// returns), and the server's close must propagate back as EOF on the
	// client (the connection-close-delimited-response bug): ReadAll below
	// hangs forever if either direction's half-close is missing.
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q, want %q", got, "ping")
	}
}

func TestListenLocalUnknownRoute(t *testing.T) {
	t.Parallel()
	reg := portless.New(portless.WithStrict())
	defer reg.Close()
	if _, err := reg.ListenLocal("nope"); !errors.Is(err, portless.ErrRouteNotFound) {
		t.Fatalf("err = %v, want ErrRouteNotFound", err)
	}
}

func TestListenLocalDialFailureSurfacesEvent(t *testing.T) {
	t.Parallel()
	events := make(chan portless.Event, 16)
	reg := portless.New(portless.WithStrict(),
		portless.WithEventHandler(func(e portless.Event) {
			select {
			case events <- e:
			default:
			}
		}))
	defer reg.Close()

	// A backend that is never ready: the bridge's per-conn dial must fail
	// (after the short ready timeout) and surface EventDialError — not
	// vanish into debug logs.
	_, err := reg.Add(t.Context(), "dead", &notReadyBackend{},
		portless.RouteWithReadyTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := reg.ListenLocal("dead")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", bridge.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The bridge closes the client conn on dial failure.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected conn to be closed after dial failure")
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == portless.EventDialError && e.Route == "dead" {
				return
			}
		case <-deadline:
			t.Fatal("no EventDialError observed for bridge dial failure")
		}
	}
}

func TestListenLocalFollowsRemoveAndReAdd(t *testing.T) {
	t.Parallel()
	upstream := startEchoOnce(t)
	reg := portless.New(portless.WithStrict())
	defer reg.Close()
	if _, err := reg.Add(t.Context(), "svc", backend.Listener(upstream)); err != nil {
		t.Fatal(err)
	}
	bridge, err := reg.ListenLocal("svc")
	if err != nil {
		t.Fatal(err)
	}

	// After Remove, a new bridge connection must fail (closed by the
	// bridge), not dial the stopped backend.
	if err := reg.Remove(t.Context(), "svc"); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", bridge.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected conn closed after route removal")
	}
	_ = conn.Close()

	// A re-Add under the same name is picked up on the next connection.
	upstream2 := startEchoOnce(t)
	if _, err := reg.Add(t.Context(), "svc", backend.Listener(upstream2)); err != nil {
		t.Fatal(err)
	}
	conn2, err := net.Dial("tcp", bridge.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	if _, err := conn2.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if err := conn2.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn2)
	if err != nil {
		t.Fatalf("read echo after re-add: %v", err)
	}
	if string(got) != "pong" {
		t.Fatalf("echo = %q, want %q", got, "pong")
	}
}

func TestListenLocalClosedByRegistryClose(t *testing.T) {
	t.Parallel()
	upstream := startEchoOnce(t)
	reg := portless.New(portless.WithStrict())
	if _, err := reg.Add(t.Context(), "svc", backend.Listener(upstream)); err != nil {
		t.Fatal(err)
	}
	bridge, err := reg.ListenLocal("svc")
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Close()
	if _, err := net.Dial("tcp", bridge.Addr().String()); err == nil {
		t.Fatal("bridge listener should be closed after registry Close")
	}
}
