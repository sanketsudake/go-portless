package portless

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sanketsudake/go-portless/internal/backoff"
)

const defaultReadyTimeout = 60 * time.Second

// Registry maps names to backends and implements ContextDialer: dialing
// "name:port" resolves through the named route, blocking until the backend
// is ready (bounded by ctx and the route's ready timeout).
type Registry struct {
	cfg config

	mu     sync.RWMutex
	routes map[string]*Route
	closed bool

	// done is closed by Close; in-flight readiness waits observe it.
	done      chan struct{}
	closeOnce sync.Once

	// Shared HTTP plumbing, built lazily by DefaultTransport/DefaultClient
	// under httpMu (a plain mutex, not sync.Once, so Close can observe
	// "never built" without building).
	httpMu           sync.Mutex
	defaultTransport *http.Transport
	defaultClient    *http.Client

	// hasRewrites lets the per-request host-rewrite path no-op with one
	// atomic load until a rewrite-declaring route is first registered (the
	// common case is never). It is sticky: Remove does not clear it, so a
	// registry that once had a rewrite keeps taking the lookup path.
	hasRewrites atomic.Bool
}

// New creates a Registry.
func New(opts ...Option) *Registry {
	cfg := config{
		readyTimeout: defaultReadyTimeout,
		logger:       slog.Default(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.forceStrict {
		cfg.fallback = nil
	}
	return &Registry{
		cfg:    cfg,
		routes: make(map[string]*Route),
		done:   make(chan struct{}),
	}
}

// Add registers name with backend b. If b implements Starter, Start is called
// (with ctx) before the route becomes dialable; a Start error fails the Add.
// ctx bounds only the Start call.
func (r *Registry) Add(ctx context.Context, name string, b Backend, opts ...RouteOption) (*Route, error) {
	if name == "" {
		return nil, errors.New("portless: route name must not be empty")
	}
	if b == nil {
		return nil, errors.New("portless: backend must not be nil")
	}
	key := strings.ToLower(name)

	rcfg := routeConfig{readyTimeout: r.cfg.readyTimeout}
	for _, o := range opts {
		o(&rcfg)
	}
	if err := validateHostRewrite(rcfg.hostRewrite); err != nil {
		return nil, fmt.Errorf("portless: add %q: %w", name, err)
	}
	rt := &Route{name: name, backend: b, cfg: rcfg, registry: r}
	rt.buildDial(r.cfg.middleware)
	if rcfg.hostRewrite != "" {
		r.hasRewrites.Store(true)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	if _, dup := r.routes[key]; dup {
		r.mu.Unlock()
		return nil, fmt.Errorf("portless: add %q: %w", name, ErrRouteExists)
	}
	// Reserve the name before Start so concurrent Adds can't double-start,
	// but publish only after Start succeeds.
	r.routes[key] = nil
	r.mu.Unlock()

	// Wire the event sink only after the name is reserved, so a rejected Add
	// never leaves the backend emitting into this registry.
	if es, ok := b.(EventSinkSetter); ok {
		es.SetEventSink(func(e Event) {
			if e.Route == "" {
				e.Route = name
			}
			r.emit(e)
		})
	}

	if s, ok := b.(Starter); ok {
		if err := s.Start(ctx); err != nil {
			r.mu.Lock()
			delete(r.routes, key)
			r.mu.Unlock()
			return nil, fmt.Errorf("portless: start backend for %q: %w", name, err)
		}
	}

	r.mu.Lock()
	if r.closed {
		delete(r.routes, key)
		r.mu.Unlock()
		stopBackend(b)
		return nil, ErrClosed
	}
	r.routes[key] = rt
	r.mu.Unlock()
	r.emit(Event{Type: EventRouteAdded, Route: name})
	return rt, nil
}

// AddReady registers name with backend b and blocks until the route accepts
// connections (bounded by ctx and the route's ready timeout). If readiness
// fails, the route is removed again, so the name is immediately reusable —
// transactional setup for callers whose bootstrap is "register, wait, then
// bind dependent resources", without a Remove on every error path.
func (r *Registry) AddReady(ctx context.Context, name string, b Backend, opts ...RouteOption) (*Route, error) {
	rt, err := r.Add(ctx, name, b, opts...)
	if err != nil {
		return nil, err
	}
	if err := rt.Ready(ctx); err != nil {
		// ctx may already be expired; cleanup must still run.
		_ = r.Remove(context.WithoutCancel(ctx), name)
		return nil, fmt.Errorf("portless: add ready %q: %w", name, err)
	}
	return rt, nil
}

// Remove unregisters name. If the backend implements Stopper, Stop is called
// with ctx.
func (r *Registry) Remove(ctx context.Context, name string) error {
	key := strings.ToLower(name)
	r.mu.Lock()
	rt, ok := r.routes[key]
	if ok && rt != nil {
		delete(r.routes, key)
	}
	r.mu.Unlock()
	if !ok || rt == nil {
		return fmt.Errorf("portless: remove %q: %w", name, ErrRouteNotFound)
	}
	r.emit(Event{Type: EventRouteRemoved, Route: rt.name})
	if s, ok := rt.backend.(Stopper); ok {
		return s.Stop(ctx)
	}
	return nil
}

// Lookup returns the route registered under name (case-insensitive).
func (r *Registry) Lookup(name string) (*Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.routes[strings.ToLower(name)]
	return rt, ok && rt != nil
}

// Routes returns all registered routes.
func (r *Registry) Routes() []*Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Route, 0, len(r.routes))
	for _, rt := range r.routes {
		if rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

// Ready waits until each named route accepts connections, dialing them
// concurrently (all registered routes when no names are given). It is
// doctor-as-a-function: the common bootstrap shape "block until my services
// are up" in one call. Each wait is bounded by ctx and the route's ready
// timeout; failures are joined with their route names.
func (r *Registry) Ready(ctx context.Context, names ...string) error {
	var routes []*Route
	if len(names) == 0 {
		routes = r.Routes()
	} else {
		for _, name := range names {
			rt, ok := r.Lookup(name)
			if !ok {
				return fmt.Errorf("portless: ready %q: %w", name, ErrRouteNotFound)
			}
			routes = append(routes, rt)
		}
	}
	errs := make([]error, len(routes))
	var wg sync.WaitGroup
	for i, rt := range routes {
		wg.Go(func() {
			if err := rt.Ready(ctx); err != nil {
				errs[i] = fmt.Errorf("route %q: %w", rt.Name(), err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Close stops all backends and releases the registry. In-flight readiness
// waits return ErrClosed. Close is idempotent.
func (r *Registry) Close() error {
	var errs []error
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		routes := r.routes
		r.routes = make(map[string]*Route)
		r.mu.Unlock()
		close(r.done)

		// Drop the shared pool's idle conns, if the pool was ever built.
		r.httpMu.Lock()
		if r.defaultTransport != nil {
			r.defaultTransport.CloseIdleConnections()
		}
		r.httpMu.Unlock()

		for _, rt := range routes {
			if rt == nil {
				continue
			}
			if s, ok := rt.backend.(Stopper); ok {
				if err := s.Stop(context.Background()); err != nil {
					errs = append(errs, fmt.Errorf("stop %q: %w", rt.name, err))
				}
			}
		}
	})
	return errors.Join(errs...)
}

// DialContext dials address ("host:port"). If host matches a registered route
// (case-insensitive), the route's backend handles the dial, retrying
// Retryable errors with backoff until success, a non-retryable error, ctx
// cancellation, or the route's ready timeout. Unknown hosts fail with
// ErrRouteNotFound unless a fallback dialer was configured (WithFallback).
func (r *Registry) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host := hostOf(address)

	r.mu.RLock()
	closed := r.closed
	rt, ok := r.routes[strings.ToLower(host)]
	r.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if !ok || rt == nil {
		if r.cfg.fallback == nil {
			return nil, fmt.Errorf("portless: dial %q: %w", address, ErrRouteNotFound)
		}
		return r.cfg.fallback.DialContext(ctx, network, address)
	}
	return r.dialRoute(ctx, rt, network, address)
}

// dialRoute runs the readiness loop: retry Retryable backend errors (and
// failing health checks) with backoff until success, terminal error, ctx
// expiry, ready timeout, or Close.
func (r *Registry) dialRoute(ctx context.Context, rt *Route, network, address string) (net.Conn, error) {
	// The route's ready timeout is a safety cap for callers that pass an
	// unbounded context. An explicit caller deadline (a test's context, or
	// the control /ready?timeout= parameter) is authoritative and wins.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel func()
		ctx, cancel = context.WithTimeoutCause(ctx, rt.cfg.readyTimeout,
			fmt.Errorf("portless: route %q not ready within %v", rt.name, rt.cfg.readyTimeout))
		defer cancel()
	}

	start := time.Now()
	r.emit(Event{Type: EventDialStart, Route: rt.name, Address: address})

	fail := func(err error) (net.Conn, error) {
		r.emit(Event{Type: EventDialError, Route: rt.name, Address: address, Err: err})
		return nil, err
	}

	bo := backoff.New(25*time.Millisecond, 500*time.Millisecond)
	var lastErr error
	for attempt := 0; ; attempt++ {
		conn, err := rt.dial(ctx, network, address)
		if err == nil && rt.cfg.health != nil {
			// Health checks dial the backend directly (no port map or
			// middleware) so a probe port never collides with the port map.
			if herr := rt.cfg.health(ctx, rt.backend.DialContext); herr != nil {
				_ = conn.Close()
				// A failing health check means "not ready yet": retry until
				// the route is healthy or the ready timeout elapses.
				conn, err = nil, Retryable(fmt.Errorf("health check: %w", herr))
			}
		}
		if err == nil {
			r.emit(Event{Type: EventDialSuccess, Route: rt.name, Address: address, Elapsed: time.Since(start)})
			return conn, nil
		}
		lastErr = err
		if ctxErr := context.Cause(ctx); ctxErr != nil {
			return fail(dialWaitError(rt.name, ctx, lastErr))
		}
		if !IsRetryable(err) {
			return fail(fmt.Errorf("portless: dial route %q: %w", rt.name, err))
		}
		r.emit(Event{Type: EventDialRetry, Route: rt.name, Address: address, Attempt: attempt + 1, Err: err})
		r.cfg.logger.Debug("portless: backend not ready, retrying",
			"route", rt.name, "address", address, "err", err)

		timer := time.NewTimer(bo.Next())
		select {
		case <-ctx.Done():
			timer.Stop()
			return fail(dialWaitError(rt.name, ctx, lastErr))
		case <-r.done:
			timer.Stop()
			return fail(ErrClosed)
		case <-timer.C:
		}
	}
}

// dialWaitError builds the error for a readiness wait that ended before the
// backend came up, naming the route and the last backend error. Both the
// context cause and the last backend error are wrapped, so callers can
// errors.Is/As through to either (e.g. a typed not-found from a backend).
func dialWaitError(name string, ctx context.Context, lastErr error) error {
	cause := context.Cause(ctx)
	if lastErr != nil {
		return fmt.Errorf("portless: waiting for route %q: %w (last backend error: %w)", name, cause, lastErr)
	}
	return fmt.Errorf("portless: waiting for route %q: %w", name, cause)
}

func stopBackend(b Backend) {
	if s, ok := b.(Stopper); ok {
		_ = s.Stop(context.Background())
	}
}
