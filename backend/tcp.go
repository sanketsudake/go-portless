// Package backend provides built-in portless backends, plus the small
// port-plumbing helpers that pair with them: ListenAndAdd (bind a loopback
// listener and register it in one call) and ReservePorts (distinct free
// ports for components that take port ints).
package backend

import (
	"context"
	"net"

	portless "github.com/sanketsudake/go-portless"
)

// TCP returns a Backend that dials a fixed TCP address (e.g. "127.0.0.1:8080"),
// ignoring the port in the requested route address. Connection-refused and
// timeout errors are retryable, so dials block until the address accepts.
func TCP(addr string) portless.Backend {
	return &tcpBackend{addr: addr}
}

type tcpBackend struct {
	addr   string
	dialer net.Dialer
}

func (b *tcpBackend) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	return b.dialer.DialContext(ctx, network, b.addr)
}

// Addr exposes the configured address (portless.Addresser).
func (b *tcpBackend) Addr() net.Addr { return tcpAddr(b.addr) }

// tcpAddr is a net.Addr over a not-yet-resolved "host:port" string.
type tcpAddr string

func (a tcpAddr) Network() string { return "tcp" }
func (a tcpAddr) String() string  { return string(a) }
