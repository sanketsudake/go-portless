// Package portless provides port-free service routing for Go tests and CI.
//
// Instead of hardcoding "127.0.0.1:8888" or racing to find free ports, tests
// register named routes ("router.fission") against backends and dial them by
// name. Readiness is built into the dial: dialing a name blocks — bounded by
// the context and the route's ready timeout — until the backend actually
// accepts a connection, and backends self-heal across restarts.
//
// The core mechanism is Registry.DialContext, which has the same shape as
// net.Dialer.DialContext and therefore drops into http.Transport,
// grpc.WithContextDialer, and websocket.Dialer.NetDialContext:
//
//	reg := portless.New()
//	defer reg.Close()
//	reg.Add("echo.test", backend.TCP("127.0.0.1:9000"))
//
//	client := &http.Client{Transport: &http.Transport{DialContext: reg.DialContext}}
//	resp, err := client.Get("http://echo.test/healthz")
//
// Backends implement the one-method Backend interface; optional capabilities
// (Starter, Stopper, HealthChecker) are detected by type assertion. Built-in
// backends live in the backend subpackage; a Kubernetes port-forward backend
// lives in the separate module github.com/sanketsudake/go-portless/k8s.
package portless
