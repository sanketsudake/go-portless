package proxy

import (
	"io"
	"net/http"
)

// hopByHop headers are consumed by each proxy hop and must not be forwarded.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// handleAbsolute forwards a plain-HTTP absolute-form request
// (GET http://name:port/path) through the registry-dialing transport.
func (p *Proxy) handleAbsolute(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.RequestURI = "" // client requests must not set it
	out.Header = r.Header.Clone()
	stripHopByHop(out.Header)

	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		p.logger.Debug("proxy: forward failed", "url", r.URL.String(), "err", err)
		http.Error(w, "portless proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	stripHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck // client may hang up mid-body
}

func stripHopByHop(h http.Header) {
	for _, f := range h.Values("Connection") {
		h.Del(f)
	}
	for _, f := range hopByHop {
		h.Del(f)
	}
}
