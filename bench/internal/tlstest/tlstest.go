// Package tlstest is the one place that generates certificates and starts
// httptest servers in each transport mode, for tests of every client in bench
// ("Tests share fixtures"). It is only for tests.
package tlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Authority is a throwaway certificate authority.
type Authority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	// CertPEM is the CA certificate, as written to ca.crt.
	CertPEM []byte
}

// Pair is a certificate and its private key, PEM encoded.
type Pair struct {
	CertPEM []byte
	KeyPEM  []byte
}

var serialNumber = big.NewInt(1)

func nextSerial() *big.Int {
	serialNumber = new(big.Int).Add(serialNumber, big.NewInt(1))
	return serialNumber
}

func mustKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// NewAuthority creates a self-signed CA.
func NewAuthority(t testing.TB) *Authority {
	t.Helper()
	key := mustKey(t)
	template := &x509.Certificate{
		SerialNumber:          nextSerial(),
		Subject:               pkix.Name{CommonName: "servtuner test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &Authority{
		certificate: certificate,
		key:         key,
		CertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

func (a *Authority) issue(t testing.TB, subject string, usage x509.ExtKeyUsage, addresses []net.IP) Pair {
	t.Helper()
	key := mustKey(t)
	template := &x509.Certificate{
		SerialNumber: nextSerial(),
		Subject:      pkix.Name{CommonName: subject},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  addresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return Pair{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

// IssueServer returns a server certificate valid for the loopback address.
func (a *Authority) IssueServer(t testing.TB) Pair {
	t.Helper()
	return a.issue(t, "servtuner test server", x509.ExtKeyUsageServerAuth, []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback})
}

// IssueClient returns a client certificate.
func (a *Authority) IssueClient(t testing.TB) Pair {
	t.Helper()
	return a.issue(t, "servtuner test client", x509.ExtKeyUsageClientAuth, nil)
}

// WriteFile writes content into dir and returns its path.
func WriteFile(t testing.TB, dir, name string, content []byte) string {
	t.Helper()
	location := filepath.Join(dir, name)
	if err := os.WriteFile(location, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return location
}

// WriteFiles writes the pair as <name>.crt and <name>.key in dir.
func (p Pair) WriteFiles(t testing.TB, dir, name string) (certFile, keyFile string) {
	t.Helper()
	return WriteFile(t, dir, name+".crt", p.CertPEM), WriteFile(t, dir, name+".key", p.KeyPEM)
}

// NewMTLSServer starts an HTTPS server that requires a client certificate
// signed by clientCA, and presents a certificate signed by serverCA.
func NewMTLSServer(t testing.TB, serverCA, clientCA *Authority, handler http.Handler) *httptest.Server {
	t.Helper()
	serverPair := serverCA.IssueServer(t)
	certificate, err := tls.X509KeyPair(serverPair.CertPEM, serverPair.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	clientPool := x509.NewCertPool()
	clientPool.AddCert(clientCA.certificate)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientPool,
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

// NewPlatformServer starts a plain HTTP server, as in platform mode.
func NewPlatformServer(t testing.TB, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// NewTLSServer starts an HTTPS server that presents a certificate signed by
// serverCA and asks for no client certificate, as in tls mode (vLLM with
// --ssl-cert-reqs 0).
func NewTLSServer(t testing.TB, serverCA *Authority, handler http.Handler) *httptest.Server {
	t.Helper()
	serverPair := serverCA.IssueServer(t)
	certificate, err := tls.X509KeyPair(serverPair.CertPEM, serverPair.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}
