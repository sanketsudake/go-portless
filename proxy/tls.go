package proxy

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
)

// terminateTLS answers a CONNECT with a certificate for the requested name,
// then serves the decrypted HTTP over the TLS connection, forwarding each
// request to the backend by name. Clients must trust the issuer's CA.
func (p *Proxy) terminateTLS(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "portless proxy: hijacking unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, "portless proxy: hijack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = clientConn.Close() }()
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	connectHost := r.Host
	tlsConn := tls.Server(clientConn, p.tls.ServerTLSConfig())
	nc := &notifyConn{Conn: tlsConn, closed: make(chan struct{})}

	srv := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		// The request arrives in origin form; reconstruct an absolute URL
		// pointing at the route named by Host so forward can dial it.
		req.URL.Scheme = "http"
		host := req.Host
		if host == "" {
			host = connectHost
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		req.URL.Host = host
		p.forward(rw, req)
	})}
	// Serve returns once the client closes the TLS connection.
	_ = srv.Serve(&singleConnListener{conn: nc, addr: clientConn.LocalAddr()})
}

// notifyConn signals on Close so a singleConnListener knows the served
// connection is finished and Serve can stop accepting.
type notifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *notifyConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// singleConnListener yields one connection to http.Serve, then blocks the
// accept loop until that connection closes and returns io.EOF, so Serve
// returns cleanly instead of looping forever.
type singleConnListener struct {
	conn   *notifyConn
	addr   net.Addr
	handed bool
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if !l.handed {
		l.handed = true
		return l.conn, nil
	}
	<-l.conn.closed
	return nil, io.EOF
}

func (l *singleConnListener) Close() error { return l.conn.Close() }

func (l *singleConnListener) Addr() net.Addr { return l.addr }
