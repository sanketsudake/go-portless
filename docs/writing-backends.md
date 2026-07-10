# Writing backends and middleware

A backend maps a route name to a live connection.
Middleware wraps the dial path.
Both are the extension points for custom routing, fault injection, and observability.

## Implementing a backend

The required interface is one method:

```go
type Backend interface {
    DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
```

`address` is the full `name:port` the caller dialed.
A backend that serves a single endpoint may ignore it; a multi-port backend uses the port.

### The retryable-error contract

The registry owns the wait-and-retry loop.
A backend does not sleep or retry itself — it returns quickly, and signals "not ready yet, keep waiting" by returning a *retryable* error:

```go
func (b *myBackend) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
    addr := b.currentAddress()
    if addr == "" {
        return nil, portless.Retryable(errors.New("backend not ready"))
    }
    return (&net.Dialer{}).DialContext(ctx, network, addr)
}
```

`portless.Retryable(err)` marks an error retryable while preserving its chain (`errors.Is`/`errors.As` still work).
Connection-refused/reset and network timeouts are treated as retryable automatically, so a backend that just forwards a real dial error usually needs no wrapping.
Return a plain (non-retryable) error for terminal conditions — a bad configuration, an unmapped port — so the dial fails fast instead of burning the ready timeout.

### Optional capabilities

Implement only what the backend needs; the registry detects each by type assertion.

```go
// Called once by Registry.Add before the route is dialable. A Start error
// fails the Add. ctx bounds only this call.
func (b *myBackend) Start(ctx context.Context) error { /* begin watches, warm caches */ }

// Called by Registry.Remove and Registry.Close. Release goroutines and conns.
func (b *myBackend) Stop(ctx context.Context) error { /* ... */ }
```

### Emitting events

A backend that can observe its own health implements `EventSinkSetter`; the registry injects a sink at `Add` time.
Emit `EventBackendUnhealthy` when the endpoint fails and `EventBackendRecovered` when it comes back, so consumers (e.g. `CloseIdleOnUnhealthy`) can react.

```go
func (b *myBackend) SetEventSink(sink func(portless.Event)) { b.sink = sink }

// later, when a cached connection dies:
if b.sink != nil {
    b.sink(portless.Event{Type: portless.EventBackendUnhealthy, Err: err})
}
```

Do not call the sink while holding a lock that the emit path might re-acquire — capture the sink reference, release the lock, then call it.

### Self-heal pattern

A backend that caches a connection (like the k8s port-forward backend) self-heals by dropping the cached connection on failure and re-resolving on the next dial:

```go
conn, err := b.ensureConn(ctx)          // lazily resolve + cache
if err != nil {
    return nil, portless.Retryable(err) // let the registry wait
}
c, err := conn.open()
if err != nil {
    b.dropCachedConn(conn)              // dead (e.g. pod restart)
    b.emit(portless.Event{Type: portless.EventBackendUnhealthy, Err: err})
    return nil, portless.Retryable(err) // next dial re-resolves
}
return c, nil
```

## Writing middleware

Middleware wraps the dial path.
It is the seam for fault injection, latency, request logging, and per-route metrics — none of which touch the core.

```go
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)
type Middleware func(next DialFunc) DialFunc
```

Apply middleware registry-wide (`WithMiddleware`) or per route (`RouteWithMiddleware`).
The chain order is registry middleware (outermost) → route middleware → port map → backend.

### Inject latency or faults

```go
func chaos(p float64, delay time.Duration) portless.Middleware {
    return func(next portless.DialFunc) portless.DialFunc {
        return func(ctx context.Context, network, address string) (net.Conn, error) {
            time.Sleep(delay)
            if rand.Float64() < p {
                return nil, fmt.Errorf("injected fault dialing %s", address)
            }
            return next(ctx, network, address)
        }
    }
}

reg := portless.New(portless.WithMiddleware(chaos(0.1, 20*time.Millisecond)))
```

Return a non-retryable error to make the fault terminal, or `portless.Retryable(...)` to make the dial retry as if the backend were briefly unavailable.

### Wrap the connection

For the common case of counting bytes or logging, `ConnWrapper` adapts a conn-wrapping function into middleware:

```go
reg := portless.New(portless.WithMiddleware(
    portless.ConnWrapper(func(name string, c net.Conn) net.Conn {
        return &countingConn{Conn: c, route: name} // embed net.Conn
    }),
))
```

Embed `net.Conn` in the wrapper so methods like `CloseWrite` (used by the forward proxy's half-close) promote through — otherwise a wrapped connection loses TCP half-close.

## Observing events

Register handlers with `WithEventHandler` (may be given more than once; handlers fan out).
Handlers run synchronously on the emitting goroutine and must be fast and non-blocking — buffer to a channel if you need to do real work.

```go
reg := portless.New(portless.WithEventHandler(func(e portless.Event) {
    switch e.Type {
    case portless.EventDialRetry:
        log.Printf("route %s not ready, attempt %d: %v", e.Route, e.Attempt, e.Err)
    case portless.EventDialSuccess:
        metrics.Observe(e.Route, e.Elapsed) // total readiness wait
    }
}))
```

## Registering a backend type with the daemon

To make a custom backend available through the CLI/daemon, register a factory that builds it from JSON config, then have the CLI call your registration function at startup:

```go
func Register() {
    control.RegisterBackendType("mytype", func(cfg json.RawMessage) (portless.Backend, error) {
        var c MyConfig
        if err := json.Unmarshal(cfg, &c); err != nil {
            return nil, err
        }
        return NewMyBackend(c)
    })
}
```

`portless route add NAME` sends a `RouteSpec{Type: "mytype", Config: ...}`; the daemon looks up the factory and constructs the backend.
This is how the k8s module adds the `"k8s"` type without the core importing client-go.
