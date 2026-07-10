package portless

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
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
}

// New creates a Registry.
func New(opts ...Option) *Registry {
	cfg := config{
		fallback:     &net.Dialer{},
		readyTimeout: defaultReadyTimeout,
		logger:       slog.Default(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &Registry{
		cfg:    cfg,
		routes: make(map[string]*Route),
		done:   make(chan struct{}),
	}
}

// Add registers name with backend b. If b implements Starter, Start is called
// before the route becomes dialable; a Start error fails the Add.
func (r *Registry) Add(name string, b Backend, opts ...RouteOption) (*Route, error) {
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
	rt := &Route{name: name, backend: b, cfg: rcfg, registry: r}

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

	if s, ok := b.(Starter); ok {
		if err := s.Start(context.Background()); err != nil {
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
// cancellation, or the route's ready timeout. Unknown hosts use the fallback
// dialer, or fail with ErrRouteNotFound in strict mode.
func (r *Registry) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address // bare host without port
	}

	r.mu.RLock()
	closed := r.closed
	rt, ok := r.routes[strings.ToLower(host)]
	r.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if !ok || rt == nil {
		if r.cfg.strict {
			return nil, fmt.Errorf("portless: dial %q: %w", address, ErrRouteNotFound)
		}
		return r.cfg.fallback.DialContext(ctx, network, address)
	}
	return r.dialRoute(ctx, rt, network, address)
}

// dialRoute runs the readiness loop: retry Retryable backend errors with
// backoff until success, terminal error, ctx expiry, ready timeout, or Close.
func (r *Registry) dialRoute(ctx context.Context, rt *Route, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, rt.cfg.readyTimeout,
		fmt.Errorf("portless: route %q not ready within %v", rt.name, rt.cfg.readyTimeout))
	defer cancel()

	bo := backoff.New(25*time.Millisecond, 500*time.Millisecond)
	var lastErr error
	for {
		conn, err := rt.backend.DialContext(ctx, network, address)
		if err == nil {
			return conn, nil
		}
		if ctxErr := context.Cause(ctx); ctxErr != nil {
			return nil, dialWaitError(rt.name, ctx, lastErr)
		}
		if !IsRetryable(err) {
			return nil, fmt.Errorf("portless: dial route %q: %w", rt.name, err)
		}
		lastErr = err
		r.cfg.logger.Debug("portless: backend not ready, retrying",
			"route", rt.name, "address", address, "err", err)

		timer := time.NewTimer(bo.Next())
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, dialWaitError(rt.name, ctx, lastErr)
		case <-r.done:
			timer.Stop()
			return nil, ErrClosed
		case <-timer.C:
		}
	}
}

// dialWaitError builds the error for a readiness wait that ended before the
// backend came up, naming the route and the last backend error.
func dialWaitError(name string, ctx context.Context, lastErr error) error {
	cause := context.Cause(ctx)
	if lastErr != nil {
		return fmt.Errorf("portless: waiting for route %q: %w (last backend error: %v)", name, cause, lastErr)
	}
	return fmt.Errorf("portless: waiting for route %q: %w", name, cause)
}

func stopBackend(b Backend) {
	if s, ok := b.(Stopper); ok {
		_ = s.Stop(context.Background())
	}
}
