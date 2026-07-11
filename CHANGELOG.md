# Changelog

## v0.3.0 — 2026-07-11

Second adoption-feedback round: every item was field-proven in fission's CLI and e2e harness on v0.2.0 (fission/fission#3562 and the in-process e2e work).

### Added

- `Registry.ListenLocal(name)` — loopback TCP bridge for consumers that cannot take a custom dialer (bare HTTP call sites, SDKs, subprocesses, printed URLs).
  Correct bidirectional half-close (connection-close-delimited responses no longer hang); per-connection dial failures surface as `EventDialError` plus a Warn log; the route name is resolved per connection so `Remove`/re-`Add` are honored; listeners are closed by `Close`.
- `Registry.AddReady(ctx, name, backend, opts...)` — transactional register-and-wait; removes the route on readiness failure so the name is immediately reusable instead of poisoned with `ErrRouteExists`.
- `backend.ListenAndAdd(ctx, reg, name, opts...)` — the embedded-service recipe (bind `127.0.0.1:0`, register, return the listener) as a one-liner.
- `backend.ReservePorts(n)` — n distinct free TCP ports, reserved atomically by holding all n listeners open before closing any.
- k8s: `TargetPort` is now optional with `Pod`/`LabelSelector` when the resolved pod declares exactly one container port (mirrors the `Service` single-port rule); an explicitly-set zero port still errors.
- k8s: a `Service` whose single port has no explicit `targetPort` now defaults to the port value (Kubernetes semantics) instead of erroring `target port is unset`.

### Docs

- `backend.Listener`: documented accept-backlog readiness (dial-ready at bind, before the service serves; pair TLS servers with `RouteWithTLSHealth`).
- `k8s.LabelSelector`: documented first-ready-pod-wins within a namespace.

## v0.2.0 — 2026-07-11

Adoption-feedback release: every change below was surfaced by the first real adoption of v0.1.0 in an OSS project's test harnesses and CI.

### Breaking

- **`HTTPClient()`'s (and `DefaultClient()`'s) `Transport` is no longer a bare `*http.Transport`** — it is wrapped for per-route Host rewriting, so `client.Transport.(*http.Transport)` assertions now fail.
  Configure the transport via `Transport(opts...)` / `TransportOption` instead, or wrap your own transport with `Registry.WrapRoundTripper`.
  `client.CloseIdleConnections()` still works through the wrapper.
- **Registries are strict by default.**
  `New()` now fails dials to unregistered names with `ErrRouteNotFound` instead of silently falling back to a real network dial.
  The fallback was most dangerous in the flagship scenario — route names that mirror resolvable DNS names, where a typo bypasses readiness and dials the real network.
  **Migration:** pass `portless.WithFallback(nil)` to restore the old behavior (nil means a plain `net.Dialer`), or drop your `WithStrict()` calls.
  `WithFallbackDialer` is deprecated and aliases `WithFallback`; deprecated `WithStrict` still overrides any fallback option (v0.1 precedence), so code combining the two stays strict.

### Added

- `Registry.DefaultTransport()` / `Registry.DefaultClient()` — memoized, shared HTTP plumbing; call freely from helpers and retry loops without losing connection pooling.
  `Transport()`/`HTTPClient()` remain explicit constructors and their docs now say they build a NEW transport per call.
- `RouteWithHostRewrite(host)` + `Registry.WrapRoundTripper` — per-route Host header override, applied by the registry-built clients and the forward proxy's absolute-form path.
  Defeats DNS-rebinding heuristics that 403 forwarded traffic ("loopback peer + non-loopback Host"); see the new docs section.
- `backend.ParseTCP(s)` — safe parser for user-supplied override addresses: rejects `https://` (no silent plaintext downgrade), paths/queries/userinfo, bad ports; handles IPv6 literals; bare hosts default to port 80.
  The control `"tcp"` type and `portless alias` / `route add --tcp` validate through it.
- `Addresser` (optional backend capability) and `Route.Addr()` — TCP/Listener (and set Futures) expose their concrete address, so consumers stop keeping a parallel name→addr map.
  `GET /v1/routes` and `portless route list` now include the address.
- `RouteWithTLSHealth(port, cfg)` — readiness gated on a TLS handshake, not just TCP accept; a nil config defaults to `InsecureSkipVerify`, correct for a liveness probe and centralized in one audited place.
- `Registry.Ready(ctx, names...)` — wait on several routes concurrently (all routes when none named).
- `k8s.ErrTargetNotFound` — typed (still retryable) error for absent Services/pods/selector matches, detectable with `errors.Is` through the registry's wait error; skip-fast callers no longer burn the full ready timeout.

### Fixed

- Readiness-wait errors now wrap the last backend error with `%w`, so `errors.Is`/`errors.As` reach typed backend errors through a timed-out dial.
- `go` directives lowered from `1.26.5` to `1.26.0` — patch-level directives force-bumped every consumer's `go.mod` on `go get`.

### Docs

- "Servers with DNS-rebinding protection" section (README + writing-backends): symptom, cause, both fixes.
- coder/websocket example beside gorilla's (`DialOptions{HTTPClient: reg.DefaultClient()}`).
- The never-`Close` pattern for process-lifetime registries.

## v0.1.0 — 2026-07-10

Initial release: name-based L4 dial routing with readiness built into the dial, middleware/events extension hooks, TCP/Listener/Mem/Future backends, HTTP forward proxy (CONNECT + absolute-form, optional TLS termination with a local CA), unix-socket control plane, Kubernetes stream-per-dial port-forward backend (separate module), and the `portless` CLI (separate module).
