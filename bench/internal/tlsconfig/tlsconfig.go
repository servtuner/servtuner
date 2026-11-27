// Package tlsconfig builds the TLS configuration of one connection. It answers
// the two questions TLS asks, each stated by the caller and never inferred
// from the URL:
//
//   - Is the connection encrypted? A hop with TLS needs an https:// URL and
//     verifies the server against the system pool plus an optional private CA
//     bundle. A hop without TLS needs an http:// URL and takes no certificate
//     settings: something else (a service mesh, an encrypted CNI) encrypts it,
//     or it is a local test.
//   - Client certificates. A client presents one when it is given one. A
//     server requires one, verifies one if it is sent, or ignores them
//     (ClientAuth).
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
	// TLS says whether the connection uses TLS.
	TLS bool
	URL string
	// CertFile and KeyFile are the client certificate and key: both set to
	// present one, both empty to present none. TLS only.
	CertFile string
	KeyFile  string
	// CAFile is an extra CA bundle added to the system pool. TLS only. The
	// system pool already honours SSL_CERT_FILE, which is how a pod's combined
	// bundle reaches Go clients.
	CAFile string
}

// PresentsClientCert says whether the hop presents a client certificate.
func (h Hop) PresentsClientCert() bool { return h.CertFile != "" }

// CheckScheme enforces that TLS and plain connections do not mix: TLS needs an
// https:// URL and no TLS needs http://. The RunSpec checks and the client
// constructor both call it, so the rule has one definition.
func CheckScheme(useTLS bool, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("url %q has no host", rawURL)
	}
	if useTLS && parsed.Scheme != "https" {
		return fmt.Errorf("tls enabled needs an https:// url, got %q", rawURL)
	}
	if !useTLS && parsed.Scheme != "http" {
		return fmt.Errorf("tls disabled needs an http:// url, got %q", rawURL)
	}
	return nil
}

// Validate rejects a hop that mixes TLS and plain settings, or names half a
// client certificate. All problems are reported together.
func (h Hop) Validate() error {
	if err := CheckScheme(h.TLS, h.URL); err != nil {
		return fmt.Errorf("%s hop: %w", h.Name, err)
	}
	var problems []error
	if (h.CertFile == "") != (h.KeyFile == "") {
		problems = append(problems, errors.New("a client certificate needs both its certificate and its key"))
	}
	if !h.TLS && (h.CertFile != "" || h.KeyFile != "" || h.CAFile != "") {
		problems = append(problems, errors.New("tls disabled takes no certificate, key or CA settings"))
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("%s hop: %w", h.Name, err)
	}
	return nil
}

// ClientConfig returns the *tls.Config of a TLS hop, with the client
// certificate when the hop has one. It returns nil for a hop without TLS.
func ClientConfig(h Hop) (*tls.Config, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	if !h.TLS {
		return nil, nil
	}
	roots, err := trustPool(h.CAFile)
	if err != nil {
		return nil, fmt.Errorf("%s hop: %w", h.Name, err)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if h.PresentsClientCert() {
		certificate, err := tls.LoadX509KeyPair(h.CertFile, h.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("%s hop: load client certificate: %w", h.Name, err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

// ClientAuth is a server's client-certificate policy.
type ClientAuth string

const (
	// ClientAuthRequired refuses a client without a valid certificate.
	ClientAuthRequired ClientAuth = "required"
	// ClientAuthOptional verifies a certificate when one is sent, refusing a
	// bad one, and accepts a client that sends none: authentication then
	// rests on tokens.
	ClientAuthOptional ClientAuth = "optional"
	// ClientAuthNone asks for no client certificate.
	ClientAuthNone ClientAuth = "none"
)

// ParseClientAuth reads a policy name, as flags and config files give it.
func ParseClientAuth(name string) (ClientAuth, error) {
	switch policy := ClientAuth(name); policy {
	case ClientAuthRequired, ClientAuthOptional, ClientAuthNone:
		return policy, nil
	}
	return "", fmt.Errorf("client auth %q: want %s, %s or %s", name, ClientAuthRequired, ClientAuthOptional, ClientAuthNone)
}

// tlsType is the crypto/tls setting of the policy.
func (c ClientAuth) tlsType() tls.ClientAuthType {
	switch c {
	case ClientAuthRequired:
		return tls.RequireAndVerifyClientCert
	case ClientAuthOptional:
		return tls.VerifyClientCertIfGiven
	}
	return tls.NoClientCert
}

// Server describes a TLS listener.
type Server struct {
	CertFile, KeyFile string
	// ClientAuth is the client-certificate policy.
	ClientAuth ClientAuth
	// ClientCAFile verifies client certificates, added to the system pool.
	// Required unless ClientAuth is none, where it is not allowed.
	ClientCAFile string
}

// ServerConfig returns the *tls.Config of a TLS listener.
func ServerConfig(s Server) (*tls.Config, error) {
	if _, err := ParseClientAuth(string(s.ClientAuth)); err != nil {
		return nil, err
	}
	if s.CertFile == "" || s.KeyFile == "" {
		return nil, errors.New("a TLS server needs its certificate and key")
	}
	if s.ClientAuth == ClientAuthNone && s.ClientCAFile != "" {
		return nil, errors.New("client auth none verifies no client certificates, so it takes no client CA")
	}
	if s.ClientAuth != ClientAuthNone && s.ClientCAFile == "" {
		return nil, fmt.Errorf("client auth %s needs the CA that signs client certificates", s.ClientAuth)
	}
	certificate, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}, ClientAuth: s.ClientAuth.tlsType()}
	if s.ClientAuth != ClientAuthNone {
		if config.ClientCAs, err = trustPool(s.ClientCAFile); err != nil {
			return nil, err
		}
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
// redirect could carry the request, and its bearer token, across the TLS or
// scheme boundary the hop was configured for.
func NewHTTPClient(h Hop, timeout time.Duration) (*http.Client, error) {
	tlsConfig, err := ClientConfig(h)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Without TLS this stays nil, which is correct for a plain http:// URL.
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// SpecHop is the hop a RunSpec tls block describes, with the files the run
// was given for it. The block decides and the files must match it: a client
// certificate is given exactly when the block names one, so a run never
// presents a certificate the spec did not ask for, or silently goes without
// one it did.
func SpecHop(name string, block spec.Tls, rawURL, certFile, keyFile, caFile string) (Hop, error) {
	decoded, err := spec.DecodeTLS(block)
	if err != nil {
		return Hop{}, fmt.Errorf("%s.tls: %w", name, err)
	}
	switch {
	case decoded.ClientCert != nil && certFile == "":
		return Hop{}, fmt.Errorf("%s.tls names client certificate Secret %q, but the run was given no certificate", name, decoded.ClientCert.Name)
	case decoded.ClientCert == nil && certFile != "":
		return Hop{}, fmt.Errorf("%s.tls names no client certificate, but the run was given one", name)
	}
	hop := Hop{Name: name, TLS: decoded.Enabled, URL: rawURL, CertFile: certFile, KeyFile: keyFile, CAFile: caFile}
	return hop, hop.Validate()
}
