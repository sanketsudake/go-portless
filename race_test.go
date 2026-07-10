package portless_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	"github.com/sanketsudake/go-portless/backend"
)

// TestConcurrentAddRemoveDialClose hammers the registry from many goroutines.
// Run with -race; correctness assertion is "no panic, no race, no deadlock".
func TestConcurrentAddRemoveDialClose(t *testing.T) {
	l := echoListener(t)
	r := portless.New(portless.WithReadyTimeout(200 * time.Millisecond))

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("svc%d.test", i)
			for range 50 {
				r.Add(context.Background(), name, backend.TCP(l.Addr().String()))
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				if conn, err := r.DialContext(ctx, "tcp", name+":80"); err == nil {
					conn.Close()
				}
				cancel()
				r.Remove(context.Background(), name)
				r.Lookup(name)
				r.Routes()
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: concurrent ops did not finish")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
