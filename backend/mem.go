package backend

import (
	"context"
	"errors"
	"net"
	"sync"

	portless "github.com/sanketsudake/go-portless"
)

// Mem returns an in-memory backend/listener pair: each dial produces a
// net.Pipe whose server half is delivered through the returned listener.
// Serve an http.Server (or any Listener-based server) on it to test with
// zero TCP sockets.
func Mem() (portless.Backend, net.Listener) {
	m := &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
	return m, m
}

type memListener struct {
	conns     chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

var errMemClosed = errors.New("portless: mem listener closed")

func (m *memListener) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case m.conns <- server:
		return client, nil
	case <-m.done:
		client.Close()
		server.Close()
		return nil, errMemClosed
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	}
}

func (m *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.done:
		return nil, errMemClosed
	}
}

func (m *memListener) Close() error {
	m.closeOnce.Do(func() { close(m.done) })
	return nil
}

func (m *memListener) Addr() net.Addr { return memAddr{} }

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "mem" }
