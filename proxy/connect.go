package proxy

import (
	"io"
	"net"
	"net/http"
	"sync"
)

type closeWriter interface{ CloseWrite() error }

// handleConnect dials the target through the registry (blocking until the
// backend is ready), then tunnels bytes both ways with TCP half-close. With
// TLS termination enabled, it decrypts instead of tunneling.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if p.tls != nil {
		p.terminateTLS(w, r)
		return
	}
	backendConn, err := p.dialer.DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		p.logger.Debug("proxy: CONNECT dial failed", "target", r.Host, "err", err)
		http.Error(w, "portless proxy: dial "+r.Host+": "+err.Error(), http.StatusBadGateway)
		return
	}
	defer backendConn.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "portless proxy: hijacking unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, "portless proxy: hijack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	// Bytes the client sent before hijack completed.
	if n := buf.Reader.Buffered(); n > 0 {
		if _, err := io.CopyN(backendConn, buf.Reader, int64(n)); err != nil {
			return
		}
	}

	var once sync.Once
	closeBoth := func() { once.Do(func() { clientConn.Close(); backendConn.Close() }) }

	var wg sync.WaitGroup
	wg.Add(2)
	go tunnel(&wg, backendConn, clientConn, closeBoth)
	go tunnel(&wg, clientConn, backendConn, closeBoth)
	wg.Wait()
}

// tunnel copies src→dst. On EOF it half-closes dst's write side so the peer
// sees EOF while the opposite direction can still drain. Conns that do not
// support CloseWrite (e.g. an in-memory backend, or a ConnWrapper that does
// not embed net.Conn) cannot half-close, so the whole tunnel is torn down
// symmetrically instead — preserve CloseWrite through wrappers to keep true
// half-close.
func tunnel(wg *sync.WaitGroup, dst, src net.Conn, closeBoth func()) {
	defer wg.Done()
	io.Copy(dst, src) //nolint:errcheck // best-effort tunnel; errors end the copy
	if cw, ok := dst.(closeWriter); ok {
		cw.CloseWrite() //nolint:errcheck
		return
	}
	closeBoth()
}
