// Package portless routes services by name for Go tests and CI.
//
// Tests dial a name ("web") instead of an address ("127.0.0.1:8888"), and
// readiness is built into the dial: dialing a name blocks — bounded by the
// context and the route's ready timeout — until the backend actually accepts
// a connection, so a test can dial a service that is still starting instead
// of polling for it. Backends self-heal across restarts.
//
// Ports drop out of the test's vocabulary because the port-free backends
// never surface one: an OS-assigned listener, an in-memory pipe, or a
// Kubernetes pod. You bind ":0" (or name a pod) and hand it over; no number
// is ever picked, hardcoded, or raced for.
//
//	reg := portless.New()
//	defer reg.Close()
//
//	f := backend.Future()             // address supplied once the server is up
//	reg.Add(ctx, "web", f)
//
//	l, _ := net.Listen("tcp", ":0")   // the OS assigns the port
//	go serve(l)
//	f.SetListener(l)                  // dials to "web" now succeed
//
//	resp, err := reg.HTTPClient().Get("http://web/healthz")
//
// The core mechanism is Registry.DialContext, which has the same shape as
// net.Dialer.DialContext and therefore drops into http.Transport,
// grpc.WithContextDialer, and websocket.Dialer.NetDialContext.
//
// Backends implement the one-method Backend interface; optional capabilities
// (Starter, Stopper) are detected by type assertion. Built-in backends live
// in the backend subpackage; a Kubernetes port-forward backend lives in the
// separate module github.com/sanketsudake/go-portless/k8s.
package portless
