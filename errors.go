package portless

import (
	"context"
	"errors"
	"net"
	"syscall"
)

var (
	// ErrRouteNotFound is returned when a dialed or looked-up name has no
	// registered route (only in strict mode for dials; see WithStrict).
	ErrRouteNotFound = errors.New("portless: route not found")

	// ErrRouteExists is returned by Add when the name is already registered.
	ErrRouteExists = errors.New("portless: route already exists")

	// ErrClosed is returned for operations on a closed Registry.
	ErrClosed = errors.New("portless: registry closed")
)

// Retryable wraps err to mark it as "endpoint not ready yet": the Registry's
// dial loop will keep retrying instead of failing the dial. Backends return
// Retryable errors while their endpoint is starting up or self-healing.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &retryableError{err: err}
}

// IsRetryable reports whether err indicates a not-ready-yet condition that the
// dial loop should retry: errors wrapped with Retryable, connection-refused,
// and network timeouts.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[*retryableError](err); ok {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	// The caller's own context expiring is terminal, not a not-ready signal.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return true
	}
	return false
}

type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }
