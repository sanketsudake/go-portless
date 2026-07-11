# go-portless — improvement backlog from the first real adoption

> **Status: EXECUTED** as [PR #2](https://github.com/sanketsudake/go-portless/pull/2), released as [v0.2.0](https://github.com/sanketsudake/go-portless/releases/tag/v0.2.0) (2026-07-11).
> All 8 items landed; item 6 shipped as its preferred option (strict default flipped, `WithFallback` opt-in).
> See `CHANGELOG.md` for the release-facing summary and migration notes.

This is a work order for an implementation session running **inside the go-portless repo**.
Every item below was surfaced by adopting `v0.1.0` in a real OSS project (fission/fission#3561: two test harnesses + CI, ~20 files), then confirmed against this repo's source.
The adopter details are evidence, not requirements: **every feature here must stay generic** — shaped for any project routing test/ops traffic by name, never for one consumer.

Items are priority-ordered.
Each is independently shippable; 1–2 are the highest-leverage.
Pre-1.0 semver: breaking changes are acceptable where flagged, but prefer additive.

## 1. Stop constructing a transport per `Transport()`/`HTTPClient()` call

**Problem.** `Registry.Transport()` and `Registry.HTTPClient()` (`http.go:17,32`) build a fresh `*http.Transport` on every call.
They read like accessors, so adopters call them inside helpers and retry loops — each call gets a private connection pool, cross-call keep-alive reuse disappears, and every abandoned transport parks up to `MaxIdleConnsPerHost` idle conns for the 30s `IdleConnTimeout`.

**Evidence.** The adopter's code review found a per-call `Transport()` inside a helper invoked from 134+ call sites and inside `Eventually` retry loops; the fix (caching one transport + one client on their framework singleton) is boilerplate every adopter will need.

**Proposal.**
- Add memoized accessors: `Registry.DefaultTransport() *http.Transport` and `Registry.DefaultClient() *http.Client`, built once (respecting no options — options imply a fresh build) and living until `Close`.
- Keep `Transport(opts...)`/`HTTPClient(opts...)` as explicit constructors, but rename-or-document them so the constructor semantics are unmissable (doc comment: "builds a NEW transport; for the shared pooled client use DefaultClient").
- `Close()` should `CloseIdleConnections()` on the memoized transport.

**Acceptance.** A test proving two `DefaultClient()` calls share one pool (connection reuse observable via `httptrace`), and a doc example replacing `HTTPClient()` in a loop.

## 2. Host-header control per route + a documented DNS-rebinding gotcha

**Problem.** Dialing by route name means HTTP requests carry the route name as `Host`.
Meanwhile, forwarded traffic (SPDY port-forward, SSH tunnel, any localhost relay) arrives at the server on a **loopback local address**.
Many servers treat "loopback local addr + non-loopback Host" as a DNS-rebinding attack and reject with 403 — the MCP go-sdk does (`StreamableHTTPHandler` localhost protection), and similar heuristics exist elsewhere.
So the library's two flagship features (name-based dialing + port-forward backends) combine into a 403 on servers the adopter may not control.

**Evidence.** The adopter's only production-code change was flipping a server-side `DisableLocalhostProtection` flag after `initialize: Forbidden` — debuggable only because they owned the server.
Against a third-party server they would have been stuck.

**Proposal.**
- `RouteWithHostRewrite(host string) RouteOption` — but note the registry is L4, so this cannot live in the dial path.
  Implement at the HTTP layer instead: the registry-built transport wraps its `RoundTrip` to set `req.Host = rewrite` for routes that declare one (the route is identifiable from `req.URL.Host`).
  Typical use: `RouteWithHostRewrite("127.0.0.1")` so forwarded backends see a loopback Host.
- Whatever the API outcome, add a **"Servers with DNS-rebinding protection"** section to the README and `docs/writing-backends.md`: symptom (403/Forbidden on the first real request while a bare health endpoint works), cause, and the two fixes (host rewrite client-side, protection flag server-side).

**Acceptance.** An httptest server that rejects non-loopback Hosts passes with the rewrite option and fails without; docs section exists.

## 3. `backend.ParseTCP`: the override parser every adopter re-writes

**Problem.** Any project wiring env-var overrides ("use this fixed address instead of the managed backend") must turn a user-supplied string into a TCP backend.
The naive version — `strings.TrimPrefix(v, "http://")` and friends — is what this repo's own adoption doc suggested, and it is buggy: it silently downgrades `https://` URLs to plaintext dials, leaves URL paths inside the dial address, and breaks IPv6 literals.

**Evidence.** The adopter shipped exactly that bug, and their code review flagged it as the top correctness finding; the replacement (scheme allowlist via `net/url`, path/query rejection, `net.JoinHostPort` for IPv6, loud errors) is ~25 lines every adopter will need.

**Proposal.**
- `backend.ParseTCP(s string) (portless.Backend, error)` accepting `host`, `host:port`, `[v6]:port`, and `http://host[:port]` (default port 80).
- Reject with descriptive errors: `https://` (a TCP backend cannot terminate TLS — silent plaintext downgrade otherwise), any path/query, empty host.
- Document the intended pairing: `if v := os.Getenv(...); v != "" { b, err = backend.ParseTCP(v) }`.

**Acceptance.** Table-driven tests covering the accept/reject matrix above, including the IPv6 and downgrade cases.

## 4. Expose the backend address through the route

**Problem.** `backend.TCP` and `backend.Listener` know their address but don't expose it, and `Route` has no accessor.
Adopters whose *other* consumers need a real dialable URL (env vars handed to subprocesses, plain HTTP clients outside the registry) must keep a parallel `name → addr` map, duplicating every registration.

**Evidence.** The adopter's e2e framework carries a `ports map[string]int` beside the registry purely for this; their quality review flagged the dual bookkeeping as drift-prone.

**Proposal.**
- An optional interface in the backend contract: `type Addresser interface { Addr() net.Addr }` (or `string`).
- Implement on `tcpBackend` and the listener backend (delegate to `l.Addr()`); k8s/future backends simply don't implement it.
- `Route.Addr() (net.Addr, bool)` surfaces it.

**Acceptance.** `Listener`/`TCP` routes report their address; a doc note that registry-external consumers can be pointed at `Route.Addr()` instead of a side table.

## 5. `RouteWithTLSHealth` beside `RouteWithHTTPHealth`

**Problem.** The only canned health check is an HTTP GET.
For TLS servers, "TCP accept" and "able to serve TLS" genuinely differ (bad cert material, TLS config regressions), and adopters must hand-roll a handshake check — including the `InsecureSkipVerify` that then trips *their* repo's security scanners (the adopter had to dismiss a CodeQL alert for it).

**Proposal.**
- `RouteWithTLSHealth(cfg *tls.Config) RouteOption`: dial via the backend, `tls.Client(...).HandshakeContext(ctx)`, close.
  `nil` cfg defaults to `InsecureSkipVerify: true` with a doc comment explaining why that is correct for a readiness probe (liveness of TLS serving, not identity) — centralizing the scanner-suppression in one audited place.

**Acceptance.** Test against `httptest.NewTLSServer`'s listener: not-ready until TLS serves; a plain-TCP listener never becomes ready under this check.

## 6. Make strict mode the recommended (or default) posture

**Problem.** The default fallback dialer is most dangerous in the library's flagship scenario: route names that mirror real DNS (`router.<namespace>` **is** a resolvable in-cluster shortname).
A typo'd or unregistered name silently falls through to a real network dial that can even succeed — bypassing readiness and masking exactly the failure class the registry owns.

**Evidence.** Two independent reviews of the adoption flagged this; the adopter now passes `WithStrict()` at every construction site.

**Proposal (pick one, in order of preference).**
1. Flip the default pre-1.0: `New()` is strict; add `WithFallback()` (taking the dialer, subsuming `WithFallbackDialer`) as the opt-*in*.
   Breaking, but v0.x is the moment.
2. If not: add `NewStrict()` and use it in **every** README/doc example, with a warning box on the fallback behavior.

**Acceptance.** Examples and `cli.md`/`architecture.md` consistent with the chosen default; a changelog entry calling out the behavior change if (1).

## 7. Typed "target does not exist" error from the k8s backend

**Problem.** The k8s backend treats everything as retryable, which is right for self-healing but makes an *absent* target (Service/pod not created at all) indistinguishable from a *warming* one.
Callers with skip-fast semantics — "this optional subsystem isn't installed, skip the test now" — must burn the full ready timeout to learn the Service will never appear.

**Evidence.** The adopter's optional-subsystem test bounds the dial at 30s purely to cap this; with a typed error it could skip in one resolve round-trip.

**Proposal.**
- Export `k8s.ErrTargetNotFound` (wrapping the apierrors NotFound from Service/pod resolution), still marked `Retryable` so late-created targets keep self-healing.
- Callers distinguish via `errors.Is(err, k8s.ErrTargetNotFound)` on the dial error (the registry already preserves the last backend error in `dialWaitError` — ensure it wraps, not just `.Error()` strings, so `errors.Is` works through it; today `dialWaitError` flattens `lastErr` to a string, which this item must fix).

**Acceptance.** Fake-clientset test: missing Service → dial error `errors.Is` ErrTargetNotFound through the registry; creating the Service mid-wait still recovers.

## 8. Smaller items

- **Lower the `go` directives.** All three modules declare the latest patch release (`go 1.26.5`), which force-bumps every consumer's directive on `go get` (it broke a consumer's sub-module CI leg with "go mod tidy needed").
  Declare the minimum language version actually required (e.g. `go 1.26.0` or lower if features allow) and let CI test the latest toolchain separately.
- **`Registry.Ready(ctx, names ...string) error`** — doctor-as-a-function.
  Adopters currently write `Lookup` + `rt.Ready(ctx)` boilerplate; waiting on several routes concurrently is the common bootstrap shape.
- **coder/websocket example.** The docs show gorilla's `NetDialContext` injection; coder/websocket (increasingly the default choice) has no net-dialer hook and needs `DialOptions{HTTPClient: reg.DefaultClient()}` instead.
  Add it to the README websocket section (it works because Go's HTTP/1.1 101-upgrade response bodies are writable through a custom transport — worth one sentence so users don't have to verify it themselves).
- **Document the never-`Close` pattern.** Process-lifetime registries (test-framework singletons) legitimately never call `Close()`; say so, and note that the transport's `DialContext` keeps the registry reachable so storing the `*Registry` isn't required once clients are built.

## Out of scope (deliberately)

- Anything consumer-specific (HMAC signing wrappers, framework helpers): those belong in adopters; the library's job is the dial plane and its ergonomics.
- An HTTP reverse-proxy listener in library mode: the CLI daemon already covers the "plain client outside the process" case.

## Candidates for a next round (post-v0.2.0)

> **Status update (2026-07-11):** shipped in v0.3.0 — `backend.ReservePorts`, `Registry.ListenLocal`, the TargetPort-inference symmetry fix, `backend.ListenAndAdd` (the embedded-services recipe), and `AddReady` (the transactional half of the GetOrAdd item).
> `GetOrAdd` itself is deliberately deferred: the factory-callback API needs more design, and `AddReady` fixes the incident that motivated it.
> The `LabelSelector` cross-namespace item is withdrawn as factually wrong (see its entry).

- **`backend.ReservePorts(n) ([]int, error)` — atomic multi-port reservation for port-int components. Shipped in v0.3.0.**
  The adopter's CI caught the classic free-port race in the wild: two sequential listen-`:0`-then-close calls returned the SAME port for two listeners of one component, which hard-failed at startup.
  Components that take port ints (rather than listeners) force adopters to pre-pick ports; the safe pattern — hold all n listeners open before closing any, so the kernel cannot hand out duplicates — is small but non-obvious, and pairs naturally with `backend.TCP`/`portless.URL` wiring.
  (The adopter now carries this as `utils.FindFreePorts`; a library home would let the next project skip the incident.)
- **`Registry.ListenLocal(name string) (net.Listener, error)` — a local TCP bridge for plain-URL consumers. PRIORITY UP: field-proven in fission/fission#3562, with bugs to show for it. Shipped in v0.3.0.**
  Adopters repeatedly need to hand a *dialable local URL* to code that cannot take a custom client: bare `http.Post` call sites, third-party SDKs, subprocesses, and URLs printed for humans to copy.
  The `proxy` package (HTTP forward proxy) doesn't cover this shape — it needs env-proxy configuration on every client.
  fission's CLI shipped exactly this pattern by hand (`pkg/fission-cli/util/portless.go`, `bridgeToRoute`), and adversarial review found **two real bugs in the first draft** that a library implementation would have owned:
  (1) the client connection was never half-closed when the upstream stream ended first — a connection-close-delimited response hung the consumer forever (fix: `CloseWrite` toward the client after the upstream→client copy, mirroring the client-EOF direction);
  (2) per-connection dial failures vanished into debug-verbosity logs, so a dead backend surfaced as a silent connection reset.
  A library `ListenLocal` should get the bidirectional half-close right once, route per-conn dial errors through the existing Event stream, and document process-lifetime ownership.
  The reviewed fission implementation is a reference to lift from.
- **~~`k8s.LabelSelector` with an empty namespace silently picks the first ready pod across ALL namespaces.~~ Withdrawn: the claim is wrong.**
  `PortForward` rejects an empty namespace (`k8s: namespace is required`) and resolution lists pods only within the configured namespace.
  What was true: within a namespace, the first ready matching pod wins with no ambiguity error — now documented on `LabelSelector` (v0.3.0).
- **`k8s.LabelSelector`/`k8s.Pod` require an explicit `TargetPort` while `k8s.Service` infers a single port — asymmetric. Shipped in v0.3.0 (single-container-port inference).**
  Infer when the resolved pod exposes exactly one container port (mirroring the Service rule), or document the asymmetry where the options are defined.
- **No `GetOrAdd`/upsert on `Registry` — and partial-setup failure poisons the name. Partially shipped in v0.3.0: `AddReady` covers the transactional-setup half; `GetOrAdd` deferred.**
  `Add` returns `ErrRouteExists`, so lazy on-demand registration (register a route the first time a command needs it) forces caller-side memoization.
  Worse, the natural setup sequence `Add → Ready → bind local resources` leaves the route registered when a later step fails: the adopter must remember `Remove` on every error path, or all retries of that name fail `ErrRouteExists` forever (fission's CLI review caught exactly this in the first draft).
  Two shapes that fix both: `GetOrAdd(ctx, name, backendFactory)` returning the existing route or registering atomically, and/or `AddReady(ctx, name, backend)` that registers, waits for readiness, and deregisters on failure — transactional setup.
- **A recipe (or helper) for embedded services with injectable listeners. Shipped in v0.3.0 as `backend.ListenAndAdd` plus the accept-backlog doc note.**
  fission's in-process e2e harness converged on: `l, _ := net.Listen("tcp", "127.0.0.1:0")` → hand `l` to the service's start options → `reg.Add(name, backend.Listener(l))` — kernel-assigned ports (no pre-picking, no races), with `Route.Addr()` feeding consumers that need real URLs.
  This is likely THE pattern for any test harness embedding its services; a `portless.ListenAndAdd(ctx, reg, name) (net.Listener, error)` one-liner or a documented recipe would make it the obvious path.
  Docs nuance discovered on the way: a `backend.Listener` route is dial-ready as soon as the socket is **bound** (kernel accept backlog), which is earlier than "the service behind it is serving" — for TLS servers pair it with `RouteWithTLSHealth`, and state the accept-backlog semantics where `backend.Listener` is documented.
