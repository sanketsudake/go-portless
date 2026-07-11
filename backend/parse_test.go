package backend_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/sanketsudake/go-portless/backend"
)

func TestParseTCP(t *testing.T) {
	cases := []struct {
		in string
		// wantAddr is the address the backend should dial; "" means an error
		// containing wantErr is expected.
		wantAddr string
		wantErr  string
	}{
		{in: "127.0.0.1:8080", wantAddr: "127.0.0.1:8080"},
		{in: "example.com:9000", wantAddr: "example.com:9000"},
		{in: "example.com", wantAddr: "example.com:80"},
		{in: "http://example.com", wantAddr: "example.com:80"},
		{in: "http://example.com:8888", wantAddr: "example.com:8888"},
		{in: "http://127.0.0.1:8888", wantAddr: "127.0.0.1:8888"},
		{in: "[::1]:8080", wantAddr: "[::1]:8080"},
		{in: "[::1]", wantAddr: "[::1]:80"},
		{in: "http://[::1]:8080", wantAddr: "[::1]:8080"},
		{in: " 127.0.0.1:8080 ", wantAddr: "127.0.0.1:8080"},

		{in: "", wantErr: "empty address"},
		{in: "https://example.com", wantErr: "downgrade"},
		{in: "https://example.com:8443", wantErr: "downgrade"},
		{in: "grpc://example.com", wantErr: "unsupported scheme"},
		{in: "http://example.com/path", wantErr: "path"},
		{in: "http://example.com?x=1", wantErr: "query"},
		{in: "http://user:pw@example.com", wantErr: "userinfo"},
		{in: "http://", wantErr: "empty host"},
		{in: ":8080", wantErr: "empty host"},
		{in: "::1", wantErr: "bracketed"},
		{in: "example.com:notaport", wantErr: "invalid port"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			b, err := backend.ParseTCP(c.in)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseTCP(%q) = %v, want error containing %q", c.in, b, c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("ParseTCP(%q) error = %v, want it to contain %q", c.in, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTCP(%q): %v", c.in, err)
			}
			if got := dialTarget(t, b); got != c.wantAddr {
				t.Fatalf("ParseTCP(%q) dials %q, want %q", c.in, got, c.wantAddr)
			}
		})
	}
}

// dialTarget reads the address the backend was configured with via the
// Addresser capability (TCP backends always implement it).
func dialTarget(t *testing.T, b any) string {
	t.Helper()
	a, ok := b.(interface{ Addr() net.Addr })
	if !ok {
		t.Fatal("ParseTCP backend does not expose its address")
	}
	return a.Addr().String()
}

func TestParseTCPBackendDials(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		fmt.Fprint(c, "hi")
		c.Close()
	}()

	b, err := backend.ParseTCP("http://" + l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := b.DialContext(context.Background(), "tcp", "name:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, _ := io.ReadAll(conn)
	if string(got) != "hi" {
		t.Fatalf("read %q, want %q", got, "hi")
	}
}
