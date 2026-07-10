package proxy

import (
	"io"
	"net"
	"net/http"
	"sync"
)

// handleConnect dials the target through the registry (blocking until the
// backend is ready), then tunnels bytes both ways with TCP half-close.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
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

	var wg sync.WaitGroup
	wg.Add(2)
	go tunnel(&wg, backendConn, clientConn)
	go tunnel(&wg, clientConn, backendConn)
	wg.Wait()
}

// tunnel copies src→dst, then half-closes dst's write side so the peer sees
// EOF while the opposite direction can still drain.
func tunnel(wg *sync.WaitGroup, dst, src net.Conn) {
	defer wg.Done()
	io.Copy(dst, src) //nolint:errcheck // best-effort tunnel; errors end the copy
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite() //nolint:errcheck
	} else {
		dst.Close()
	}
}
