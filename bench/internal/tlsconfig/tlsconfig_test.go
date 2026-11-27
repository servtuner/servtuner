package tlsconfig

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

const clientTimeout = 5 * time.Second

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "ok")
})

// mtlsFixture is a CA, a server that demands client certificates, and a
// client certificate from the same CA.
type mtlsFixture struct {
	authority *tlstest.Authority
	serverURL string
	hop       Hop
}

func newMTLSFixture(t *testing.T) mtlsFixture {
	t.Helper()
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, okHandler)
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	return mtlsFixture{
		authority: authority,
		serverURL: server.URL,
		hop: Hop{
			Name:     "test",
			Mode:     spec.TransportModeMtls,
			URL:      server.URL,
			CertFile: certFile,
			KeyFile:  keyFile,
			CAFile:   tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM),
		},
	}
}

func TestMTLSClientSucceedsWithCertificate(t *testing.T) {
	fixture := newMTLSFixture(t)
	client, err := NewHTTPClient(fixture.hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(fixture.serverURL)
	if err != nil {
		t.Fatalf("mtls request failed: %v", err)
	}
	defer response.Body.Close()
	if body, _ := io.ReadAll(response.Body); string(body) != "ok" {
		t.Errorf("body = %q", body)
	}
}

// TestMTLSServerRefusesClientWithoutCertificate builds a client that trusts the
// server but presents no certificate, using the same trust pool, and checks the
// server turns it away ("Tests").
func TestMTLSServerRefusesClientWithoutCertificate(t *testing.T) {
	fixture := newMTLSFixture(t)
	tlsConfig, err := ClientConfig(fixture.hop)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig.Certificates = nil
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport, Timeout: clientTimeout}
	response, err := client.Get(fixture.serverURL)
	if err == nil {
		response.Body.Close()
		t.Fatal("server accepted a client with no certificate")
	}
}

func TestMTLSClientRejectsUntrustedServer(t *testing.T) {
	fixture := newMTLSFixture(t)
	otherAuthority := tlstest.NewAuthority(t)
	untrustedServer := tlstest.NewMTLSServer(t, otherAuthority, fixture.authority, okHandler)
	hop := fixture.hop
	hop.URL = untrustedServer.URL
	client, err := NewHTTPClient(hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(untrustedServer.URL)
	if err == nil {
		response.Body.Close()
		t.Fatal("client accepted a server certificate from an untrusted CA")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("error does not mention the certificate: %v", err)
	}
}

func TestMTLSClientWithoutPrivateCAdoesNotTrustServer(t *testing.T) {
	fixture := newMTLSFixture(t)
	hop := fixture.hop
	hop.CAFile = ""
	client, err := NewHTTPClient(hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get(fixture.serverURL); err == nil {
		response.Body.Close()
		t.Fatal("client trusted a private CA that is not in the system pool")
	}
}

func TestPlatformClientWorksAgainstPlainServer(t *testing.T) {
	server := tlstest.NewPlatformServer(t, okHandler)
	client, err := NewHTTPClient(Hop{Name: "test", Mode: spec.TransportModePlatform, URL: server.URL}, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("platform request failed: %v", err)
	}
	response.Body.Close()
}

// tlsFixture is a CA, a server that presents its certificate and asks for no
// client certificate, and a tls hop that trusts the CA.
type tlsFixture struct {
	authority *tlstest.Authority
	serverURL string
	hop       Hop
}

func newTLSFixture(t *testing.T) tlsFixture {
	t.Helper()
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewTLSServer(t, authority, okHandler)
	return tlsFixture{
		authority: authority,
		serverURL: server.URL,
		hop: Hop{
			Name:   "test",
			Mode:   spec.TransportModeTls,
			URL:    server.URL,
			CAFile: tlstest.WriteFile(t, t.TempDir(), "ca.crt", authority.CertPEM),
		},
	}
}

func TestTLSClientVerifiesServerWithoutCertificate(t *testing.T) {
	fixture := newTLSFixture(t)
	config, err := ClientConfig(fixture.hop)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Certificates) != 0 {
		t.Errorf("tls hop presents %d client certificates, want none", len(config.Certificates))
	}
	client, err := NewHTTPClient(fixture.hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(fixture.serverURL)
	if err != nil {
		t.Fatalf("tls request failed: %v", err)
	}
	defer response.Body.Close()
	if body, _ := io.ReadAll(response.Body); string(body) != "ok" {
		t.Errorf("body = %q", body)
	}
}

func TestTLSClientRejectsUntrustedServer(t *testing.T) {
	fixture := newTLSFixture(t)
	untrusted := tlstest.NewTLSServer(t, tlstest.NewAuthority(t), okHandler)
	hop := fixture.hop
	hop.URL = untrusted.URL
	client, err := NewHTTPClient(hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get(untrusted.URL); err == nil {
		response.Body.Close()
		t.Fatal("client accepted a server certificate from an untrusted CA")
	}
}

func TestTLSClientWithoutPrivateCAdoesNotTrustServer(t *testing.T) {
	fixture := newTLSFixture(t)
	hop := fixture.hop
	hop.CAFile = ""
	client, err := NewHTTPClient(hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get(fixture.serverURL); err == nil {
		response.Body.Close()
		t.Fatal("client trusted a private CA that is not in the system pool")
	}
}

// TestTLSClientIsRefusedByMTLSServer: a tls hop presents nothing, so a server
// that requires client certificates turns it away; tls is never a quiet
// stand-in for mtls.
func TestTLSClientIsRefusedByMTLSServer(t *testing.T) {
	fixture := newMTLSFixture(t)
	hop := Hop{Name: "test", Mode: spec.TransportModeTls, URL: fixture.serverURL, CAFile: fixture.hop.CAFile}
	client, err := NewHTTPClient(hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get(fixture.serverURL); err == nil {
		response.Body.Close()
		t.Fatal("an mtls server accepted a tls client with no certificate")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	server := tlstest.NewPlatformServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/", http.StatusTemporaryRedirect)
	}))
	client, err := NewHTTPClient(Hop{Name: "test", Mode: spec.TransportModePlatform, URL: server.URL}, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want the redirect itself", response.StatusCode)
	}
}

func TestMixingTheModesIsRejected(t *testing.T) {
	fixture := newMTLSFixture(t)
	cases := map[string]Hop{
		"mtls with http url":          {Mode: spec.TransportModeMtls, URL: "http://svc:80", CertFile: fixture.hop.CertFile, KeyFile: fixture.hop.KeyFile},
		"platform with https url":     {Mode: spec.TransportModePlatform, URL: "https://svc:443"},
		"platform with certificate":   {Mode: spec.TransportModePlatform, URL: "http://svc:80", CertFile: fixture.hop.CertFile, KeyFile: fixture.hop.KeyFile},
		"platform with CA bundle":     {Mode: spec.TransportModePlatform, URL: "http://svc:80", CAFile: fixture.hop.CAFile},
		"mtls without certificate":    {Mode: spec.TransportModeMtls, URL: "https://svc:443"},
		"mtls without key":            {Mode: spec.TransportModeMtls, URL: "https://svc:443", CertFile: fixture.hop.CertFile},
		"mtls url without host":       {Mode: spec.TransportModeMtls, URL: "https://", CertFile: fixture.hop.CertFile, KeyFile: fixture.hop.KeyFile},
		"platform url without host":   {Mode: spec.TransportModePlatform, URL: "http:///v1"},
		"unknown mode":                {Mode: "plain", URL: "http://svc:80"},
		"empty mode (never inferred)": {URL: "https://svc:443"},
		"non-http scheme under mtls":  {Mode: spec.TransportModeMtls, URL: "ftp://svc", CertFile: fixture.hop.CertFile, KeyFile: fixture.hop.KeyFile},
		"unparseable url under mtls":  {Mode: spec.TransportModeMtls, URL: "https://svc:bad", CertFile: fixture.hop.CertFile, KeyFile: fixture.hop.KeyFile},
		"tls with http url":           {Mode: spec.TransportModeTls, URL: "http://svc:80"},
		"tls with client certificate": {Mode: spec.TransportModeTls, URL: "https://svc:443", CertFile: fixture.hop.CertFile, KeyFile: fixture.hop.KeyFile},
		"tls with client key only":    {Mode: spec.TransportModeTls, URL: "https://svc:443", KeyFile: fixture.hop.KeyFile},
	}
	for name, hop := range cases {
		t.Run(name, func(t *testing.T) {
			hop.Name = "test"
			if client, err := NewHTTPClient(hop, clientTimeout); err == nil {
				t.Errorf("accepted: %+v", client)
			}
			if config, err := ClientConfig(hop); err == nil {
				t.Errorf("ClientConfig accepted: %+v", config)
			}
		})
	}
}

func TestMissingCertificateFilesAreErrors(t *testing.T) {
	fixture := newMTLSFixture(t)
	hop := fixture.hop
	hop.CertFile = hop.CertFile + ".missing"
	if _, err := NewHTTPClient(hop, clientTimeout); err == nil {
		t.Error("missing certificate file accepted")
	}
	hop = fixture.hop
	hop.CAFile = tlstest.WriteFile(t, t.TempDir(), "empty.crt", []byte("not a certificate"))
	if _, err := NewHTTPClient(hop, clientTimeout); err == nil {
		t.Error("CA bundle without certificates accepted")
	}
}

func TestClientConfigNeverSkipsVerification(t *testing.T) {
	fixture := newMTLSFixture(t)
	config, err := ClientConfig(fixture.hop)
	if err != nil {
		t.Fatal(err)
	}
	if config.InsecureSkipVerify {
		t.Error("InsecureSkipVerify is set")
	}
	if config.MinVersion < 0x0303 {
		t.Errorf("MinVersion = %#x, want at least TLS 1.2", config.MinVersion)
	}
	platformConfig, err := ClientConfig(Hop{Name: "test", Mode: spec.TransportModePlatform, URL: "http://svc"})
	if err != nil || platformConfig != nil {
		t.Errorf("platform ClientConfig = %v, %v; want nil, nil", platformConfig, err)
	}
}

func TestCheckScheme(t *testing.T) {
	if err := CheckScheme(spec.TransportModeMtls, "https://a"); err != nil {
		t.Error(err)
	}
	if err := CheckScheme(spec.TransportModePlatform, "http://a"); err != nil {
		t.Error(err)
	}
	if err := CheckScheme(spec.TransportModeMtls, "http://a"); err == nil {
		t.Error("mtls with http accepted")
	}
	if err := CheckScheme(spec.TransportModeTls, "https://a"); err != nil {
		t.Error(err)
	}
	if err := CheckScheme(spec.TransportModeTls, "http://a"); err == nil {
		t.Error("tls with http accepted")
	}
}
