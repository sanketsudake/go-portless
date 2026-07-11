package backend

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	portless "github.com/sanketsudake/go-portless"
)

// ParseTCP turns a user-supplied address string — typically an env-var
// override like FOO_URL — into a TCP backend. It accepts:
//
//   - "host" (port defaults to 80)
//   - "host:port"
//   - "[v6]:port" (IPv6 literals must be bracketed when a port is given)
//   - "http://host[:port]" (port defaults to 80)
//
// It rejects, with descriptive errors, the inputs naive TrimPrefix parsing
// gets wrong: "https://" URLs (a TCP backend cannot terminate TLS, so
// accepting them would silently downgrade to plaintext), any other scheme,
// URLs carrying a path/query/fragment/userinfo, and empty hosts.
//
// Intended pairing:
//
//	if v := os.Getenv("FOO_URL"); v != "" {
//	    b, err = backend.ParseTCP(v)
//	}
func ParseTCP(s string) (portless.Backend, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("backend: parse tcp: empty address")
	}

	hostport := s
	if scheme, _, ok := strings.Cut(s, "://"); ok {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("backend: parse tcp %q: %w", s, err)
		}
		switch u.Scheme {
		case "http":
		case "https":
			return nil, fmt.Errorf("backend: parse tcp %q: a TCP backend cannot terminate TLS; refusing to silently downgrade https to a plaintext dial", s)
		default:
			return nil, fmt.Errorf("backend: parse tcp %q: unsupported scheme %q (want host[:port] or http://host[:port])", s, scheme)
		}
		if u.User != nil {
			return nil, fmt.Errorf("backend: parse tcp %q: userinfo not allowed in a dial address", s)
		}
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("backend: parse tcp %q: a dial address cannot carry a path, query, or fragment", s)
		}
		hostport = u.Host
	}

	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port (or an unbracketed IPv6 literal): default to 80.
		host, port = hostport, "80"
		if strings.Count(host, ":") > 0 && !strings.HasPrefix(host, "[") {
			return nil, fmt.Errorf("backend: parse tcp %q: IPv6 literals must be bracketed, e.g. \"[::1]:8080\"", s)
		}
		host = strings.Trim(host, "[]")
	}
	if host == "" {
		return nil, fmt.Errorf("backend: parse tcp %q: empty host", s)
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return nil, fmt.Errorf("backend: parse tcp %q: invalid port %q", s, port)
	}
	return TCP(net.JoinHostPort(host, port)), nil
}
