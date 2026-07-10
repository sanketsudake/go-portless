package portless_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
)

func TestRetryable(t *testing.T) {
	base := errors.New("starting")
	err := portless.Retryable(base)
	if !portless.IsRetryable(err) {
		t.Fatal("Retryable-wrapped error should be retryable")
	}
	if !errors.Is(err, base) {
		t.Fatal("Retryable must preserve the error chain")
	}
	if portless.Retryable(nil) != nil {
		t.Fatal("Retryable(nil) must be nil")
	}
	// wrapped deeper in a chain still detected
	if !portless.IsRetryable(fmt.Errorf("outer: %w", err)) {
		t.Fatal("retryable inside a wrap chain should be detected")
	}
}

func TestIsRetryableClassification(t *testing.T) {
	// real connection-refused
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	_, dialErr := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
	if dialErr == nil {
		t.Skip("port unexpectedly accepting")
	}
	if !portless.IsRetryable(dialErr) {
		t.Fatalf("connection refused should be retryable: %v", dialErr)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		t.Logf("note: dial error is not ECONNREFUSED on this platform: %v", dialErr)
	}

	// network timeout
	toErr := &net.DNSError{Err: "timeout", IsTimeout: true}
	if !portless.IsRetryable(toErr) {
		t.Fatal("net timeout should be retryable")
	}

	// arbitrary errors are not retryable
	if portless.IsRetryable(errors.New("bad config")) {
		t.Fatal("plain error must not be retryable")
	}
	if portless.IsRetryable(nil) {
		t.Fatal("nil must not be retryable")
	}
	if portless.IsRetryable(context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded from the caller's ctx must not be retryable")
	}
	_ = time.Second
}
