// Package tlsconfig builds the TLS configuration and HTTP client of one
// outbound hop. Every hop is in exactly one mode, chosen by the
// caller and never inferred from the URL:
//
//   - mtls: present a client certificate and verify the server against the
//     system pool plus an optional private CA bundle. The URL must be https://.
//   - platform: plain http://; the platform encrypts the traffic. No
//     certificate settings are allowed.
//   - tls: server-authenticated TLS. Verify the server against the
//     system pool plus an optional private CA bundle and present no client
//     certificate. The URL must be https://.
//
// There is deliberately no option to skip verification.
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Hop describes one outbound connection.
type Hop struct {
	// Name says which hop this is ("api", "target"), for error messages.
	Name string
	Mode spec.TransportMode
	URL  string
	// CertFile and KeyFile are the client certificate and key (mtls only).
	CertFile string
	KeyFile  string
	// CAFile is an extra CA bundle added to the system pool (mtls and tls). The
	// system pool already honours SSL_CERT_FILE, which is how a pod's combined
	// bundle reaches Go clients.
	CAFile string
}

// CheckScheme enforces that the modes do not mix: mtls and tls need https://
// and platform needs http://. The RunSpec checks and the client
// constructor both call it, so the rule has one definition.
func CheckScheme(mode spec.TransportMode, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("url %q has no host", rawURL)
	}
	switch mode {
	case spec.TransportModeMtls, spec.TransportModeTls:
		if parsed.Scheme != "https" {
			return fmt.Errorf("mode %s needs an https:// url, got %q", mode, rawURL)
		}
	case spec.TransportModePlatform:
		if parsed.Scheme != "http" {
			return fmt.Errorf("mode platform needs an http:// url, got %q", rawURL)
		}
	default:
		return fmt.Errorf("unknown transport mode %q (want %s, %s or %s)", mode, spec.TransportModeMtls, spec.TransportModePlatform, spec.TransportModeTls)
	}
	return nil
}

// Validate rejects a hop that mixes the modes or lacks what its mode needs.
// All problems are reported together.
func (h Hop) Validate() error {
	if err := CheckScheme(h.Mode, h.URL); err != nil {
		return fmt.Errorf("%s hop: %w", h.Name, err)
	}
	var problems []error
	switch h.Mode {
	case spec.TransportModeMtls:
		if h.CertFile == "" || h.KeyFile == "" {
			problems = append(problems, errors.New("mode mtls needs both a client certificate and a key"))
		}
	case spec.TransportModePlatform:
		if h.CertFile != "" || h.KeyFile != "" || h.CAFile != "" {
			problems = append(problems, errors.New("mode platform takes no certificate, key or CA settings"))
		}
	case spec.TransportModeTls:
		if h.CertFile != "" || h.KeyFile != "" {
			problems = append(problems, errors.New("mode tls presents no client certificate or key"))
		}
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("%s hop: %w", h.Name, err)
	}
	return nil
}

// ClientConfig returns the *tls.Config of an mtls or tls hop. It returns nil
// for a platform hop, which has no TLS configuration.
func ClientConfig(h Hop) (*tls.Config, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	if h.Mode == spec.TransportModePlatform {
		return nil, nil
	}
	roots, err := trustPool(h.CAFile)
	if err != nil {
		return nil, fmt.Errorf("%s hop: %w", h.Name, err)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if h.Mode == spec.TransportModeMtls {
		certificate, err := tls.LoadX509KeyPair(h.CertFile, h.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("%s hop: load client certificate: %w", h.Name, err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

// trustPool is the system pool plus the private CA. Replacing the system pool
// with the private CA alone would break verification of public endpoints.
func trustPool(caFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system certificate pool: %w", err)
	}
	if caFile == "" {
		return pool, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA bundle %s contains no certificates", caFile)
	}
	return pool, nil
}

// NewHTTPClient returns a client for the hop. It never follows redirects: a
// redirect could carry the request, and its bearer token, across the mode or
// scheme boundary the hop was configured for.
func NewHTTPClient(h Hop, timeout time.Duration) (*http.Client, error) {
	tlsConfig, err := ClientConfig(h)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// For platform this stays nil, which is correct for a plain http:// URL.
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}
