package backend

import (
	"context"
	"errors"
	"net"
	"sync"

	portless "github.com/sanketsudake/go-portless"
)

// Future returns a backend whose address is supplied later via Set or
// SetListener. Dials before then fail with a Retryable error, so through a
// Registry they block until the address arrives.
//
// This replaces the find-a-free-port-then-hope pattern: bind ":0" yourself,
// start the component on that listener, then SetListener.
func Future() *FutureBackend {
	return &FutureBackend{}
}

// FutureBackend is created by Future.
type FutureBackend struct {
	mu     sync.RWMutex
	addr   string
	dialer net.Dialer
}

// Set supplies the backend's TCP address (e.g. "127.0.0.1:53412").
func (f *FutureBackend) Set(addr string) {
	f.mu.Lock()
	f.addr = addr
	f.mu.Unlock()
}

// SetListener supplies the backend's address from a bound listener.
func (f *FutureBackend) SetListener(l net.Listener) { f.Set(l.Addr().String()) }

func (f *FutureBackend) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	f.mu.RLock()
	addr := f.addr
	f.mu.RUnlock()
	if addr == "" {
		return nil, portless.Retryable(errors.New("portless: future backend address not set yet"))
	}
	return f.dialer.DialContext(ctx, network, addr)
}
