package prometheus

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/prometheus/prometheustest"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

// fastOptions keeps retries quick; the behaviour under test is the same.
var fastOptions = Options{RequestTimeout: 2 * time.Second, MaxAttempts: 4, InitialBackoff: time.Millisecond, MaxBackoff: 4 * time.Millisecond}

var testWindow = Window{
	Start: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 28, 12, 10, 0, 0, time.UTC),
}

var testSelectors = Selectors{NamespaceRegex: AnyOf("llm", "servtuner"), PodRegex: "vllm-.*|bench-.*", NodeRegex: "gpu-node-1"}

// newPlatformClient serves the fake over plain HTTP and returns a client for it.
func newPlatformClient(t *testing.T, fake *prometheustest.Server, wrap func(http.Handler) http.Handler) *Client {
	t.Helper()
	handler := fake.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	server := tlstest.NewPlatformServer(t, handler)
	client, err := NewClient(tlsconfig.Hop{Name: "prometheus", Mode: spec.TransportModePlatform, URL: server.URL}, fastOptions)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// newMTLSClient serves the fake over HTTPS that demands a client certificate and
// returns a client that presents one.
func newMTLSClient(t *testing.T, fake *prometheustest.Server) *Client {
	t.Helper()
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, fake.Handler())
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	client, err := NewClient(tlsconfig.Hop{
		Name: "prometheus", Mode: spec.TransportModeMtls, URL: server.URL,
		CertFile: certFile, KeyFile: keyFile, CAFile: tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM),
	}, fastOptions)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// answerEverything makes every query return one series.
func answerEverything() *prometheustest.Server {
	return &prometheustest.Server{Answer: func(string) []prometheustest.Series {
		return prometheustest.OneSeries(map[string]string{"pod": "vllm-0"}, 1793188800, "1")
	}}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
