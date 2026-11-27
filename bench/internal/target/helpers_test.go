package target

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

const testClientTimeout = 5 * time.Second

// sentinelSecrets are fake secret values planted in fixtures and in fake
// cluster objects. No output of this package may contain any of them.
var sentinelSecrets = []string{
	"sentinel-secret-admin-password",
	"sentinel-secret-provider-key",
	"sentinel-secret-header",
	"sentinel-secret-proxy-password",
	"sentinel-secret-env-token",
	"sentinel-secret-api-key-arg",
	"sentinel-secret-bearer",
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// serveFixtures answers each path with a fixture file. A path that is not in
// the map is a 404, as for an endpoint the target does not expose.
func serveFixtures(t *testing.T, fixtures map[string]string, requireAuthorization string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireAuthorization != "" && r.Header.Get("Authorization") != requireAuthorization {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		name, ok := fixtures[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if filepath.Ext(name) == ".json" {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = w.Write(readFixture(t, name))
	})
}

// forEachTransportMode runs the test once per transport mode, with an Endpoint
// whose client comes from tlsconfig exactly as the pipeline builds it.
func forEachTransportMode(t *testing.T, handler http.Handler, test func(t *testing.T, endpoint Endpoint)) {
	t.Helper()
	t.Run("platform", func(t *testing.T) {
		server := tlstest.NewPlatformServer(t, handler)
		client, err := tlsconfig.NewHTTPClient(tlsconfig.Hop{Name: "target", Mode: spec.TransportModePlatform, URL: server.URL}, testClientTimeout)
		if err != nil {
			t.Fatal(err)
		}
		test(t, Endpoint{Client: client, BaseURL: server.URL + "/v1"})
	})
	t.Run("mtls", func(t *testing.T) {
		endpoint, _ := newMTLSEndpoint(t, handler)
		test(t, endpoint)
	})
}

// newMTLSEndpoint starts a server that requires a client certificate and
// returns an Endpoint that presents one, plus the CA bundle file so a test can
// build a client that does not.
func newMTLSEndpoint(t *testing.T, handler http.Handler) (Endpoint, tlsconfig.Hop) {
	t.Helper()
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, handler)
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	hop := tlsconfig.Hop{
		Name:     "target",
		Mode:     spec.TransportModeMtls,
		URL:      server.URL,
		CertFile: certFile,
		KeyFile:  keyFile,
		CAFile:   tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM),
	}
	client, err := tlsconfig.NewHTTPClient(hop, testClientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return Endpoint{Client: client, BaseURL: server.URL + "/v1"}, hop
}
