package tlsconfig

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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
			TLS:      true,
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

func TestPlainClientWorksAgainstPlainServer(t *testing.T) {
	server := tlstest.NewPlainServer(t, okHandler)
	client, err := NewHTTPClient(Hop{Name: "test", URL: server.URL}, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("plain request failed: %v", err)
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
			TLS:    true,
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
		t.Errorf("a hop without a client certificate presents %d, want none", len(config.Certificates))
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

// TestTLSClientIsRefusedByMTLSServer: a TLS hop without a client certificate
// presents nothing, so a server that requires one turns it away.
func TestTLSClientIsRefusedByMTLSServer(t *testing.T) {
	fixture := newMTLSFixture(t)
	hop := Hop{Name: "test", TLS: true, URL: fixture.serverURL, CAFile: fixture.hop.CAFile}
	client, err := NewHTTPClient(hop, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Get(fixture.serverURL); err == nil {
		response.Body.Close()
		t.Fatal("a server requiring client certificates accepted a client with none")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	server := tlstest.NewPlainServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/", http.StatusTemporaryRedirect)
	}))
	client, err := NewHTTPClient(Hop{Name: "test", URL: server.URL}, clientTimeout)
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

func TestMixingTLSAndPlainIsRejected(t *testing.T) {
	fixture := newMTLSFixture(t)
	cert, key, ca := fixture.hop.CertFile, fixture.hop.KeyFile, fixture.hop.CAFile
	cases := map[string]Hop{
		"tls with http url":                {TLS: true, URL: "http://svc:80"},
		"tls and a client cert, http url":  {TLS: true, URL: "http://svc:80", CertFile: cert, KeyFile: key},
		"no tls with https url":            {URL: "https://svc:443"},
		"no tls with a client certificate": {URL: "http://svc:80", CertFile: cert, KeyFile: key},
		"no tls with a CA bundle":          {URL: "http://svc:80", CAFile: ca},
		"client certificate without key":   {TLS: true, URL: "https://svc:443", CertFile: cert},
		"client key without certificate":   {TLS: true, URL: "https://svc:443", KeyFile: key},
		"tls url without host":             {TLS: true, URL: "https://"},
		"plain url without host":           {URL: "http:///v1"},
		"non-http scheme with tls":         {TLS: true, URL: "ftp://svc"},
		"unparseable url with tls":         {TLS: true, URL: "https://svc:bad"},
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
	platformConfig, err := ClientConfig(Hop{Name: "test", URL: "http://svc"})
	if err != nil || platformConfig != nil {
		t.Errorf("ClientConfig without TLS = %v, %v; want nil, nil", platformConfig, err)
	}
}

func TestCheckScheme(t *testing.T) {
	for _, c := range []struct {
		tls  bool
		url  string
		good bool
	}{
		{true, "https://a", true},
		{false, "http://a", true},
		{true, "http://a", false},
		{false, "https://a", false},
	} {
		if err := CheckScheme(c.tls, c.url); (err == nil) != c.good {
			t.Errorf("CheckScheme(%v, %s) = %v", c.tls, c.url, err)
		}
	}
}
