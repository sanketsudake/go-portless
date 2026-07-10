# go-portless

Port-free service routing for Go tests and CI.

Instead of hardcoding `127.0.0.1:8888`, racing to find a free port, or babysitting `kubectl port-forward` reconnect loops, register named routes and dial them by name.
Readiness is built into the dial: dialing a name blocks — bounded by the context and the route's ready timeout — until the backend actually accepts a connection, and backends self-heal across restarts.

Inspired by [portless.sh](https://portless.sh), but built for test infrastructure rather than human dev servers: zero-root, no `/etc/hosts`, no TLS CA, no daemon required for in-process use.

## Why

A single mechanism, `Registry.DialContext`, has the same shape as `net.Dialer.DialContext`, so it drops into `http.Transport`, `grpc.WithContextDialer`, and `websocket.Dialer.NetDialContext`.
Names resolve at the L4 dial layer, so HTTP, WebSockets, gRPC, and raw TCP all work through one path — no more rewriting `http://` into `ws://` by hand.

## Install

```sh
go get github.com/sanketsudake/go-portless
```

The core module depends only on the standard library.
The Kubernetes port-forward backend lives in a separate module (`github.com/sanketsudake/go-portless/k8s`) so client-go never enters non-k8s builds.

## Library usage

```go
reg := portless.New()
defer reg.Close()

// Register a name against a backend.
reg.Add(ctx, "router.fission", backend.TCP("127.0.0.1:34917"))

// Dial it by name through any net/http, gRPC, or websocket client.
client := reg.HTTPClient()
resp, err := client.Get(portless.URL("router.fission", 0, "/healthz"))
```

The dial to `router.fission` blocks until the backend is accepting connections, so tests don't need `Eventually` wrappers just to survive startup races.

### Replace find-a-free-port

`backend.Future` removes the "guess a free port, then hope nothing else grabbed it" pattern.
Bind `:0` yourself, start the component on that listener, then hand the address over:

```go
f := backend.Future()
reg.Add(ctx, "router.fission", f)

l, _ := net.Listen("tcp", "127.0.0.1:0") // the OS picks the port
go startRouter(l)
f.SetListener(l) // dials to router.fission now succeed
```

### WebSockets and gRPC

```go
// WebSocket — no http→ws string surgery.
d := websocket.Dialer{NetDialContext: reg.DialContext}
conn, _, err := d.Dial(portless.WSURL("router.fission", 0, "/stream"), nil)

// gRPC
cc, err := grpc.NewClient("router.fission:80", grpc.WithContextDialer(reg.DialContext))
```

### Extensibility

Middleware wraps the dial path (registry-wide or per-route) — the seam for fault injection, latency, and traffic metrics without changing the core:

```go
reg := portless.New(portless.WithMiddleware(
    portless.ConnWrapper(func(name string, c net.Conn) net.Conn {
        return meteredConn(name, c) // count bytes, log, inject latency…
    }),
))
```

Route lifecycle and dial events (`EventDialRetry`, `EventBackendUnhealthy`, …) are delivered to handlers registered with `WithEventHandler`.

## Kubernetes backend

The `k8s` module forwards each dial as its own SPDY stream to a ready pod — no local listener, no reconnect loop.
Pod restarts self-heal: the next dial re-resolves a ready pod, and the readiness loop absorbs the gap.

```go
b, _ := k8s.PortForward(restConfig, k8s.Service("fission", "router"))
reg.Add(ctx, "router.fission", b)
```

## CLI and the forward proxy

For shell scripts, CI, and non-Go processes, run the daemon and point tools at it via `HTTP_PROXY`:

```sh
portless serve &                                  # forward proxy + control API
portless route add router.fission --k8s-service fission/router
portless doctor router.fission                    # wait once until ready
eval "$(portless env)"                            # export HTTP_PROXY / HTTPS_PROXY / NO_PROXY
curl http://router.fission/healthz                # reaches the pod through the proxy
```

The daemon fronts the proxy with a strict registry, so it only reaches registered routes — never a fallback network dial.
In GitHub Actions, `portless env --shell github >> "$GITHUB_ENV"` exports the proxy for the whole job.

## Modules

| Module | Path | Depends on |
|--------|------|------------|
| core | `github.com/sanketsudake/go-portless` | stdlib only |
| k8s backend | `github.com/sanketsudake/go-portless/k8s` | core + client-go |
| CLI | `github.com/sanketsudake/go-portless/cmd/portless` | core + k8s |

During development the sub-modules resolve the core via a `replace` directive; these are removed and pinned to a tagged version at release.

## License

See [LICENSE](LICENSE).
