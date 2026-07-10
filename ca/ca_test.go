package ca_test

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/sanketsudake/go-portless/ca"
)

func TestGenerateAndLoad(t *testing.T) {
	dir := t.TempDir()

	c1, err := ca.Load(dir) // first call generates
	if err != nil {
		t.Fatal(err)
	}
	if len(c1.CertPEM()) == 0 {
		t.Fatal("empty CA cert PEM")
	}
	// files persisted
	for _, f := range []string{"ca.crt", "ca.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("expected %s to be written: %v", f, err)
		}
	}
	// key file must not be world-readable
	fi, _ := os.Stat(filepath.Join(dir, "ca.key"))
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("ca.key mode = %v, want 0600-ish", fi.Mode().Perm())
	}

	c2, err := ca.Load(dir) // second call loads the same CA
	if err != nil {
		t.Fatal(err)
	}
	if string(c1.CertPEM()) != string(c2.CertPEM()) {
		t.Fatal("Load did not return the persisted CA on the second call")
	}
}

func TestLeafCertVerifiesAgainstCA(t *testing.T) {
	c, err := ca.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := c.LeafFor("web")
	if err != nil {
		t.Fatal(err)
	}

	// Build a cert pool trusting the CA and verify the leaf chains to it.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.CertPEM()) {
		t.Fatal("failed to add CA to pool")
	}
	x509Leaf, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "web"}); err != nil {
		t.Fatalf("leaf for web does not verify against the CA: %v", err)
	}
	// wrong name must fail
	if _, err := x509Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "other"}); err == nil {
		t.Fatal("leaf should not verify for a different name")
	}
}

func TestLeafCache(t *testing.T) {
	c, err := ca.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.LeafFor("web")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.LeafFor("web")
	if err != nil {
		t.Fatal(err)
	}
	if &a.Certificate[0][0] != &b.Certificate[0][0] {
		t.Fatal("LeafFor should cache and reuse the cert for the same name")
	}
}

func TestTLSConfigServesPerSNI(t *testing.T) {
	c, err := ca.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.ServerTLSConfig()
	if cfg.GetCertificate == nil {
		t.Fatal("ServerTLSConfig must set GetCertificate for SNI")
	}
	crt, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "api"})
	if err != nil {
		t.Fatal(err)
	}
	x509Leaf, _ := x509.ParseCertificate(crt.Certificate[0])
	if err := x509Leaf.VerifyHostname("api"); err != nil {
		t.Fatalf("SNI cert not valid for requested name: %v", err)
	}
}
