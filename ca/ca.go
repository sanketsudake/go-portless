// Package ca is a small local certificate authority: it issues short-lived
// leaf certificates for route names so a client that trusts the CA can reach
// "https://web" with a verified certificate. It is used by the forward proxy
// to terminate TLS, and can be used directly to give a test server a cert.
//
// It depends only on the standard library.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA is a loaded certificate authority that issues leaf certificates.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte

	mu     sync.Mutex
	leaf   map[string]*tls.Certificate
	serial int64
}

// Load returns the CA stored in dir, generating and persisting a new one the
// first time. The private key is written 0600 inside a 0700 directory.
func Load(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ca: create state dir: %w", err)
	}
	crtPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	crtPEM, crtErr := os.ReadFile(crtPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if crtErr == nil && keyErr == nil {
		return fromPEM(crtPEM, keyPEM)
	}
	if crtErr != nil && !os.IsNotExist(crtErr) {
		return nil, crtErr
	}

	c, crtPEM, keyPEM, err := generate()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(crtPath, crtPEM, 0o644); err != nil {
		return nil, fmt.Errorf("ca: write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("ca: write key: %w", err)
	}
	return c, nil
}

// CertPEM returns the CA certificate in PEM form, for adding to a trust store.
func (c *CA) CertPEM() []byte { return c.certPEM }

// LeafFor returns a leaf certificate valid for name (an entry per name is
// cached), signed by the CA.
func (c *CA) LeafFor(name string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if crt, ok := c.leaf[name]; ok {
		return crt, nil
	}
	crt, err := c.issue(name)
	if err != nil {
		return nil, err
	}
	c.leaf[name] = crt
	return crt, nil
}

// ServerTLSConfig returns a *tls.Config that answers each TLS handshake with a
// leaf certificate for the requested SNI name.
func (c *CA) ServerTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = "localhost"
			}
			return c.LeafFor(name)
		},
	}
}

func (c *CA) issue(name string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	c.serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(c.serial),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, c.cert.Raw},
		PrivateKey:  key,
		Leaf:        tmpl,
	}, nil
}

func generate() (*CA, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "portless local CA", Organization: []string{"go-portless"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	crtPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return &CA{cert: cert, key: key, certPEM: crtPEM, leaf: map[string]*tls.Certificate{}}, crtPEM, keyPEM, nil
}

func fromPEM(crtPEM, keyPEM []byte) (*CA, error) {
	crtBlock, _ := pem.Decode(crtPEM)
	if crtBlock == nil {
		return nil, fmt.Errorf("ca: invalid cert PEM")
	}
	cert, err := x509.ParseCertificate(crtBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("ca: invalid key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, certPEM: crtPEM, leaf: map[string]*tls.Certificate{}}, nil
}

// DefaultStateDir returns the CA/state directory: $PORTLESS_STATE_DIR, else
// <user config dir>/portless.
func DefaultStateDir() string {
	if d := os.Getenv("PORTLESS_STATE_DIR"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "portless")
	}
	return filepath.Join(os.TempDir(), "portless")
}
