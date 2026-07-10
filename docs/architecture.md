# Architecture

go-portless routes named services at the connection layer.
A consumer dials a name (`web:80`); the library resolves that name to a backend and returns a live `net.Conn`.
Because resolution happens at `DialContext`, every TCP-based protocol — HTTP, WebSocket, gRPC, raw TCP — works through the same mechanism.

## Modules

The code is split into three Go modules so that dependency weight lands only where it is used.

| Module | Import path | Depends on |
|--------|-------------|------------|
| core | `github.com/sanketsudake/go-portless` | standard library only |
| k8s backend | `github.com/sanketsudake/go-portless/k8s` | core + `k8s.io/client-go` |
| CLI | `github.com/sanketsudake/go-portless/cmd/portless` | core + k8s |

A CI check enforces that the core module's non-test packages import nothing outside the standard library.
During development the sub-modules resolve the core through a `replace` directive; those are removed and pinned to a tagged version before release.

## The dial path

`Registry.DialContext(ctx, network, address)` has the same signature as `net.Dialer.DialContext`, so it drops into `http.Transport.DialContext`, `grpc.WithContextDialer`, and `websocket.Dialer.NetDialContext`.

Resolution:

1. Split `address` into host and port.
   The host is the route name.
2. Look up the route by name (case-insensitive).
3. On a miss: dial through the fallback dialer (a plain `net.Dialer` by default), or return `ErrRouteNotFound` if the registry is strict (`WithStrict`).
4. On a hit: run the route's readiness loop.

```
DialContext
  └─ route lookup ──miss──▶ fallback dialer (or ErrRouteNotFound if strict)
        │hit
        ▼
   readiness loop ──▶ middleware chain ──▶ port map ──▶ Backend.DialContext
```

## Readiness is part of the dial

A dial to a route does not fail immediately when the backend is not up.
The readiness loop retries the backend while it returns a *retryable* error, with exponential backoff (25ms→500ms, jittered), until one of:

- the backend returns a connection (optionally gated by a health check),
- the backend returns a non-retryable error (fails fast),
- the caller's context is cancelled,
- the ready timeout elapses (default 60s; a safety cap only for dials whose context has no deadline — an explicit caller deadline wins),
- the registry is closed.

An error is retryable when it is wrapped with `portless.Retryable`, or is a connection-refused/reset or a network timeout.
This is what lets a test dial a service that is still starting and simply block, instead of wrapping the dial in an `Eventually` retry loop, and what lets backends self-heal across restarts.

## Backends

A backend is a single method:

```go
type Backend interface {
    DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
```

Optional capabilities are detected by type assertion, so a backend implements only what it needs:

- `Starter` — `Start(ctx)` is called by `Registry.Add` before the route becomes dialable.
- `Stopper` — `Stop(ctx)` is called by `Registry.Remove` and `Registry.Close`.
- `EventSinkSetter` — receives a sink to emit `EventBackendUnhealthy`/`EventBackendRecovered`.

Built-in backends live in the `backend` package: `TCP` (static address), `Listener` (an existing `net.Listener`), `Mem` (an in-memory `net.Pipe` listener — serve HTTP with zero TCP sockets), and `Future` (address supplied later; dials block until then).

`Future` replaces the find-a-free-port pattern: bind `:0` yourself, start the component on that listener, then hand the address over — no port is ever guessed, so there is no time-of-check/time-of-use race.

See [writing-backends.md](writing-backends.md) for implementing custom backends.

## Extensibility seams

Two seams let features be added without changing the core:

- **Middleware** wraps the dial path.
  `Middleware` is `func(next DialFunc) DialFunc`.
  Registry-level middleware is outermost, then route-level, then the port map, then the backend.
  Fault injection, latency, and per-route metrics are all middleware; `ConnWrapper` adapts the common "just wrap the returned conn" case.
- **Events** are delivered synchronously to handlers registered with `WithEventHandler`: route add/remove, dial start/retry/success/error, and backend health transitions.
  Handlers must not block.
  `CloseIdleOnUnhealthy` is a ready-made handler that drops an `http.Transport`'s pooled connections when a backend reports unhealthy, so the next request redials instead of failing once on a stale connection.

## Port maps

`RouteWithPortMap` rewrites requested ports to backend ports (for multi-port services): a dial to `name:req` is handed to the backend as `name:mapped`.
When a map is set, dialing an unmapped port fails loudly (non-retryably) rather than silently reaching the wrong port.
Health checks bypass the port map — they dial the backend directly — so a probe port never collides with the map.

## Forward proxy

The `proxy` package is a standard HTTP forward proxy over a `ContextDialer`, so non-Go processes (curl, browsers, other tools) reach named routes via `HTTP_PROXY`/`HTTPS_PROXY` — no `/etc/hosts`, no root.

- **CONNECT**: dials the target through the registry (blocking on readiness, so a curl through the proxy waits for a starting backend instead of needing a retry loop), then tunnels bytes with TCP half-close.
  TLS is passthrough — the client sees the backend's own certificate, so this works for TLS backends but the cert will not match the route name unless the backend serves one for it.
- **Absolute-form HTTP**: forwards through a registry-dialing transport with hop-by-hop headers stripped.

The proxy reaches whatever its dialer reaches, so it must be fronted with a strict registry; the CLI daemon does this so only registered routes are reachable, never a fallback network dial.

## Control plane

The `control` package exposes a registry over an HTTP/JSON API on a unix socket (authentication is filesystem permissions: a 0700 parent directory and a 0600 socket).
It backs the CLI: status, route CRUD, and `GET /v1/routes/{name}/ready?timeout=` — the wait-once primitive that blocks until a backend accepts, used by `portless doctor` and CI.

`RegisterBackendType` lets a module plug in backend construction (the k8s module registers `"k8s"`) without the core importing it.
Error responses carry a machine-readable `code` so the client maps failures back to sentinel errors (`ErrRouteExists`, `ErrRouteNotFound`) without matching on message text.

## Kubernetes backend

`k8s.PortForward` forwards each dial as its own SPDY stream to a ready pod — one stream per dial, no local listener.
This is the key difference from `kubectl port-forward` and client-go's `PortForwarder`, both of which bind a local port and need a reconnect loop.

Per dial:

1. Ensure a live SPDY connection to a ready pod, resolving lazily.
   Resolution takes a `Service` (via its selector and target port), a label selector, or a pod name, and resolves the target container port (numeric or named).
2. Open a data + error stream pair over that connection and return the data stream as a `net.Conn`.
   The error stream is drained in the background; a message there (the pod went away) closes the conn.

Self-heal is connection-level: a dead cached connection (pod restart) is dropped and the next dial re-resolves a ready pod.
A not-ready pod returns a retryable error, so the registry's readiness loop absorbs the restart window.
The SPDY transport is hidden behind an internal interface so it can be swapped (e.g. for the WebSocket transport, KEP-4006) without an API change.

## Concurrency and lifecycle

- The registry guards its route map with a `sync.RWMutex`.
  A route is reserved under lock before `Starter.Start` runs and published only after it succeeds, so a concurrent `Add` cannot double-start and a `Close` racing `Start` is handled.
- Background goroutines are anchored to a registry/route/proxy lifetime and observed in the readiness backoff wait, so `Close` cancels in-flight dials.
  The proxy builds its `http.Server` at construction time so `Close` can always stop it, and its context watcher exits on `Close`.
- Every package with goroutines runs its tests under goleak to catch leaks.

## Threat model

go-portless targets developer machines and CI, not multi-tenant hosts.

- The forward proxy and control socket bind to localhost / a same-user socket.
- The proxy must front a strict registry; a non-strict registry behind it is an open forward proxy (an SSRF pivot for any local process).
  The CLI daemon enforces strict.
- The control socket's security is filesystem permissions; the daemon creates it 0600 inside a 0700 directory.
- No TLS CA, no `/etc/hosts` mutation, no root — all deliberately out of scope for v1.
