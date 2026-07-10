package backend

import (
	"net"

	portless "github.com/sanketsudake/go-portless"
)

// Listener returns a Backend that dials l's address. The caller keeps
// ownership of l: removing the route does not close it.
func Listener(l net.Listener) portless.Backend {
	return TCP(l.Addr().String())
}
