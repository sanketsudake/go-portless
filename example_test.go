package portless_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// Example shows the core flow: register a named route, dial it by name.
// Here the backend is an in-memory listener, so no TCP port is used at all.
func Example() {
	reg := portless.New()
	defer reg.Close()

	b, l := backend.Mem()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from "+r.Host)
	})}
	go srv.Serve(l)
	defer srv.Close()

	reg.Add(context.Background(), "web", b)

	// DefaultClient is the registry's shared pooled client — safe to call
	// from helpers and loops; reg.Close() drops its idle connections.
	resp, err := reg.DefaultClient().Get(portless.URL("web", 0, "/healthz"))
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
	// Output: hello from web
}

// ExampleBackend_future shows the port-free pattern: the OS assigns the port,
// and dials to the name block until the server hands its listener over — no
// port is picked, hardcoded, or raced for.
func ExampleFutureBackend() {
	reg := portless.New()
	defer reg.Close()

	f := backend.Future()
	reg.Add(context.Background(), "web", f)

	l, _ := net.Listen("tcp", "127.0.0.1:0") // OS assigns the port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ready")
	})}
	go srv.Serve(l)
	defer srv.Close()
	f.SetListener(l) // dials to "web" now succeed

	client := reg.HTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://web/")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
	// Output: ready
}

// ExampleURL builds route URLs without string surgery.
func ExampleURL() {
	fmt.Println(portless.URL("web", 0, "/healthz"))
	fmt.Println(portless.URL("web", 8888, "/fn"))
	fmt.Println(portless.WSURL("web", 0, "/stream"))
	// Output:
	// http://web/healthz
	// http://web:8888/fn
	// ws://web/stream
}
