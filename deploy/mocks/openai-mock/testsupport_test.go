package main

import (
	"context"
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
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testPKI is a throwaway CA with a server and a client certificate on disk.
type testPKI struct {
	caFile, serverCertFile, serverKeyFile string
	caPool                                *x509.CertPool
	clientCertificate                     tls.Certificate
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	dir := t.TempDir()
	caKey, caCert, caDER := issueCertificate(t, nil, nil, "test-ca", true, nil)
	serverKey, _, serverDER := issueCertificate(t, caCert, caKey, "mock", false, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientKey, _, clientDER := issueCertificate(t, caCert, caKey, "client", false, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	pki := testPKI{
		caFile:         writePEM(t, dir, "ca.crt", "CERTIFICATE", caDER),
		serverCertFile: writePEM(t, dir, "tls.crt", "CERTIFICATE", serverDER),
		serverKeyFile:  writePEM(t, dir, "tls.key", "EC PRIVATE KEY", marshalKey(t, serverKey)),
		caPool:         x509.NewCertPool(),
		clientCertificate: tls.Certificate{
			Certificate: [][]byte{clientDER},
			PrivateKey:  clientKey,
		},
	}
	pki.caPool.AddCert(caCert)
	return pki
}

// issueCertificate signs with parent (self-signs when parent is nil).
func issueCertificate(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, commonName string, isCA bool, usages []x509.ExtKeyUsage) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           usages,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	if isCA {
		template.KeyUsage |= x509.KeyUsageCertSign
	}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, parsed, der
}

func marshalKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func writePEM(t *testing.T, dir, name, blockType string, der []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// startMock runs the mock on a port-0 listener and returns its host:port.
func startMock(t *testing.T, cfg Config) string {
	t.Helper()
	address, _ := startMockWithMetrics(t, cfg)
	return address
}

// startMockWithMetrics also opens a metrics listener when cfg.MetricsListenAddress is set (its value is
// ignored beyond that) and returns its host:port, or "" when /metrics stays on the main listener.
func startMockWithMetrics(t *testing.T, cfg Config) (address, metricsAddress string) {
	t.Helper()
	if cfg.MetricsListenAddress != "" {
		cfg.MetricsListenAddress = "127.0.0.1:0"
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var metricsListener net.Listener
	if cfg.MetricsListenAddress != "" {
		if metricsListener, err = net.Listen("tcp", cfg.MetricsListenAddress); err != nil {
			t.Fatal(err)
		}
		metricsAddress = metricsListener.Addr().String()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, listener, metricsListener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return listener.Addr().String(), metricsAddress
}

func platformConfig() Config {
	cfg := defaultConfig()
	cfg.TLSMode = tlsModePlatform
	cfg.TimeToFirstToken = 0
	cfg.InterTokenLatency = 0
	return cfg
}

func newClient(tlsConfig *tls.Config) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig, DisableKeepAlives: true}}
}
