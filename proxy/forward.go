package proxy

import (
	"io"
	"net/http"
	"strings"
)

// hostRewriter is the optional interface a dialer (typically
// *portless.Registry) implements to declare per-route Host overrides: it maps
// a URL host ("name" or "name:port") to the Host header to send, or ok=false.
// CONNECT tunnels are opaque bytes and cannot be rewritten; only
// absolute-form (and TLS-terminated) HTTP passes through here.
type hostRewriter interface {
	HostRewrite(urlHost string) (string, bool)
}

// hopByHop headers are consumed by each proxy hop and must not be forwarded.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// handleAbsolute forwards a plain-HTTP absolute-form request
// (GET http://name:port/path) through the registry-dialing transport.
func (p *Proxy) handleAbsolute(w http.ResponseWriter, r *http.Request) {
	p.forward(w, r)
}

// forward re-issues r through the registry-dialing transport and copies the
// response back. r.URL must be absolute (absolute-form requests already are;
// TLS-terminated requests have their scheme/host filled in first).
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.RequestURI = "" // client requests must not set it
	out.Header = r.Header.Clone()
	stripHopByHop(out.Header)
	p.applyHostRewrite(out)

	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		p.logger.Debug("proxy: forward failed", "url", r.URL.String(), "err", err)
		http.Error(w, "portless proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	stripHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck // client may hang up mid-body
}

// applyHostRewrite sets the outgoing Host header when the dialer declares a
// rewrite for the target route (see portless.RouteWithHostRewrite).
func (p *Proxy) applyHostRewrite(out *http.Request) {
	if hr, ok := p.dialer.(hostRewriter); ok {
		if rewrite, ok := hr.HostRewrite(out.URL.Host); ok {
			out.Host = rewrite
		}
	}
}

func stripHopByHop(h http.Header) {
	// A Connection header nominates hop-by-hop headers as comma-separated
	// tokens, which may share one header line ("Connection: close, X-Foo").
	for _, line := range h.Values("Connection") {
		for tok := range strings.SplitSeq(line, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				h.Del(tok)
			}
		}
	}
	for _, f := range hopByHop {
		h.Del(f)
	}
}
