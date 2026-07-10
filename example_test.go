package portless_test

import (
	"context"
	"fmt"
	"io"
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

	reg.Add(context.Background(), "router.fission", b)

	client := reg.HTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Get(portless.URL("router.fission", 0, "/healthz"))
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
	// Output: hello from router.fission
}
