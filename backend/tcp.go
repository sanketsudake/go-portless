// Package backend provides built-in portless backends.
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
