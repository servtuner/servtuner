package tlsconfig

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

// serve starts a server with ServerConfig, the configuration servtuner's own
// listeners use.
func serve(t *testing.T, server Server) *httptest.Server {
	t.Helper()
	config, err := ServerConfig(server)
	if err != nil {
		t.Fatal(err)
	}
	listener := httptest.NewUnstartedServer(okHandler)
	listener.TLS = config
	listener.StartTLS()
	t.Cleanup(listener.Close)
	return listener
}

// TestServerClientAuthMatrix: every policy against a client with no
// certificate, a good one, and one from a CA the server does not trust.
// optional verifies what is sent, so a bad certificate is still refused.
func TestServerClientAuthMatrix(t *testing.T) {
	authority := tlstest.NewAuthority(t)
	stranger := tlstest.NewAuthority(t)
	directory := t.TempDir()
	serverCert, serverKey := authority.IssueServer(t).WriteFiles(t, directory, "server")
	caFile := tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM)
	goodCert, goodKey := authority.IssueClient(t).WriteFiles(t, directory, "good")
	badCert, badKey := stranger.IssueClient(t).WriteFiles(t, directory, "bad")

	clients := map[string]struct{ cert, key string }{"no certificate": {}, "good certificate": {goodCert, goodKey}, "untrusted certificate": {badCert, badKey}}
	want := map[ClientAuth]map[string]bool{
		ClientAuthRequired: {"no certificate": false, "good certificate": true, "untrusted certificate": false},
		ClientAuthOptional: {"no certificate": true, "good certificate": true, "untrusted certificate": false},
		ClientAuthNone:     {"no certificate": true, "good certificate": true, "untrusted certificate": true},
	}
	for policy, outcomes := range want {
		server := Server{CertFile: serverCert, KeyFile: serverKey, ClientAuth: policy}
		if policy != ClientAuthNone {
			server.ClientCAFile = caFile
		}
		listener := serve(t, server)
		for name, files := range clients {
			t.Run(string(policy)+"/"+name, func(t *testing.T) {
				hop := Hop{Name: "test", TLS: true, URL: listener.URL, CertFile: files.cert, KeyFile: files.key, CAFile: caFile}
				client, err := NewHTTPClient(hop, clientTimeout)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Get(listener.URL)
				if err == nil {
					response.Body.Close()
				}
				if accepted := err == nil && response.StatusCode == http.StatusOK; accepted != outcomes[name] {
					t.Errorf("accepted = %v, want %v (%v)", accepted, outcomes[name], err)
				}
			})
		}
	}
}

func TestServerConfigRejectsContradictions(t *testing.T) {
	authority := tlstest.NewAuthority(t)
	directory := t.TempDir()
	cert, key := authority.IssueServer(t).WriteFiles(t, directory, "server")
	ca := tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM)
	for name, server := range map[string]Server{
		"no policy":                  {CertFile: cert, KeyFile: key},
		"unknown policy":             {CertFile: cert, KeyFile: key, ClientAuth: "sometimes"},
		"no certificate":             {KeyFile: key, ClientAuth: ClientAuthNone},
		"required without client CA": {CertFile: cert, KeyFile: key, ClientAuth: ClientAuthRequired},
		"optional without client CA": {CertFile: cert, KeyFile: key, ClientAuth: ClientAuthOptional},
		"none with a client CA":      {CertFile: cert, KeyFile: key, ClientAuth: ClientAuthNone, ClientCAFile: ca},
	} {
		if config, err := ServerConfig(server); err == nil {
			t.Errorf("%s accepted: %+v", name, config)
		}
	}
	config, err := ServerConfig(Server{CertFile: cert, KeyFile: key, ClientAuth: ClientAuthOptional, ClientCAFile: ca})
	if err != nil || config.MinVersion < 0x0303 || config.InsecureSkipVerify {
		t.Errorf("ServerConfig = %+v, %v", config, err)
	}
}

func TestParseClientAuth(t *testing.T) {
	for _, name := range []string{"required", "optional", "none"} {
		if got, err := ParseClientAuth(name); err != nil || string(got) != name {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"", "mtls", "Required"} {
		if _, err := ParseClientAuth(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

// TestSpecHopFilesMatchTheBlock: the run's files must match what the block
// says, so a certificate is never presented, or skipped, against the spec.
func TestSpecHopFilesMatchTheBlock(t *testing.T) {
	withCert := map[string]any{"enabled": true, "clientCert": map[string]any{"secretRef": map[string]any{"name": "c"}}}
	tlsOnly := map[string]any{"enabled": true}
	disabled := map[string]any{"enabled": false}
	cases := []struct {
		name      string
		block     spec.Tls
		url, cert string
		wantError string
	}{
		{"client cert and files", withCert, "https://svc", "c.crt", ""},
		{"tls without a cert", tlsOnly, "https://svc", "", ""},
		{"disabled", disabled, "http://svc", "", ""},
		{"client cert but no files", withCert, "https://svc", "", "names client certificate Secret"},
		{"files but no client cert", tlsOnly, "https://svc", "c.crt", "names no client certificate"},
		{"disabled with https", disabled, "https://svc", "", "http://"},
		{"0.1 block", map[string]any{"mode": "mtls"}, "https://svc", "", "servtuner 0.2"},
	}
	for _, c := range cases {
		key := ""
		if c.cert != "" {
			key = "c.key"
		}
		hop, err := SpecHop("target", c.block, c.url, c.cert, key, "")
		if c.wantError == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			} else if hop.PresentsClientCert() != (c.cert != "") {
				t.Errorf("%s: presents a certificate = %v", c.name, hop.PresentsClientCert())
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantError) {
			t.Errorf("%s: error %v, want it to mention %q", c.name, err, c.wantError)
		}
	}
}
