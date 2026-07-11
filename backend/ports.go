package backend

import (
	"fmt"
	"net"
)

// ReservePorts picks n distinct free TCP ports by holding all n listeners
// open simultaneously before closing any of them. Two sequential
// listen-":0"-then-close calls can return the SAME port — the kernel may
// hand the just-freed port right back — which breaks components needing
// several distinct ports.
//
// The reservation ends when ReservePorts returns: another process can grab
// a returned port before the caller binds it. Use it for components that
// take port ints and bind promptly; components that accept a net.Listener
// should be handed one directly (see Listener / ListenAndAdd), which has no
// race at all.
//
// The reservation binds the wildcard address deliberately: a port reserved
// on all interfaces is guaranteed free for a component that later binds any
// specific one. IPv4 is preferred (an IPv6-only wildcard would not reserve
// the port for later IPv4 binds), falling back to IPv6 on v6-only hosts.
// The listeners never accept and close before returning.
func ReservePorts(n int) ([]int, error) {
	if n <= 0 {
		return nil, fmt.Errorf("backend: reserve ports: n must be positive, got %d", n)
	}
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for range n {
		l, err := net.Listen("tcp4", ":0")
		if err != nil {
			l, err = net.Listen("tcp", ":0")
		}
		if err != nil {
			return nil, fmt.Errorf("backend: reserve ports: %w", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}
