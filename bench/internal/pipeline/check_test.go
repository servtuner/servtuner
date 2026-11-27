package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

const checkKey = "sk-check-test-0123456789abcdef"

// fakeOpenAI serves /v1/models and a streaming /v1/chat/completions behind a
// bearer key, as vLLM does with --api-key.
func fakeOpenAI(models ...string) http.Handler {
	mux := http.NewServeMux()
	authorized := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+checkKey }
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		data := make([]map[string]string, len(models))
		for i, model := range models {
			data[i] = map[string]string{"id": model}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n")
	})
	return mux
}

// writeSecret writes one Secret key the way Kubernetes mounts it.
func writeSecret(t *testing.T, dir, name, key string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, key), value, 0o400); err != nil {
		t.Fatal(err)
	}
}

func checkRequest(t *testing.T, document string) spec.CheckTargetRequest {
	t.Helper()
	request, err := spec.Parse[spec.CheckTargetRequest](spec.SchemaCheckTargetRequest, []byte(document))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func statuses(result spec.CheckTargetResult) string {
	var parts []string
	for _, check := range result.Checks {
		parts = append(parts, fmt.Sprintf("%s=%s", check.Name, check.Status))
	}
	return strings.Join(parts, " ")
}

func checkNamed(t *testing.T, result spec.CheckTargetResult, name spec.CheckName) spec.TargetCheck {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("no %s check in %s", name, statuses(result))
	return spec.TargetCheck{}
}

// assertNoSecret fails when the key appears anywhere in the result.
func assertNoSecret(t *testing.T, result spec.CheckTargetResult) {
	t.Helper()
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), checkKey) {
		t.Fatalf("the check result leaks the key: %s", encoded)
	}
	if err := spec.Validate(spec.SchemaCheckTargetResult, encoded); err != nil {
		t.Fatalf("the result breaks its schema: %v", err)
	}
}

func platformTarget(endpoint, model string, withAuth bool) string {
	auth := ""
	if withAuth {
		auth = `"auth": {"secretRef": {"name": "vllm-key", "key": "token"}},`
	}
	return fmt.Sprintf(`{"type": "vllm", "endpoint": %q, "model": %q, %s "tls": {"mode": "platform"}}`, endpoint, model, auth)
}

func TestCheckTargetPlatformPassesWithTheRightKey(t *testing.T) {
	server := tlstest.NewPlatformServer(t, fakeOpenAI("tiny"))
	secretsDir := t.TempDir()
	writeSecret(t, secretsDir, "vllm-key", "token", []byte(checkKey+"\n"))
	request := checkRequest(t, fmt.Sprintf(`{"target": %s, "generate": true}`, platformTarget(server.URL+"/v1", "tiny", true)))

	result := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, request)

	assertNoSecret(t, result)
	if result.Status != spec.CheckStatusOk {
		t.Fatalf("every check passes or is skipped: %s", statuses(result))
	}
	want := "resolve=ok connect=ok tls=ok auth=ok model=ok generate=ok metrics=skipped gpu_metrics=skipped workload=skipped"
	if got := statuses(result); got != want {
		t.Errorf("checks:\n got %s\nwant %s", got, want)
	}
}

func TestCheckTargetSaysWhatIsWrongWithTheKeyOrModel(t *testing.T) {
	server := tlstest.NewPlatformServer(t, fakeOpenAI("tiny", "other"))
	for _, scenario := range []struct {
		name, key, model, wantCheck, wantMessage string
		withAuth                                 bool
	}{
		{name: "rejected key", key: "sk-wrong", model: "tiny", withAuth: true, wantCheck: "auth", wantMessage: "the API key was rejected"},
		{name: "no key configured", model: "tiny", withAuth: false, wantCheck: "auth", wantMessage: "the target has none (target.auth)"},
		{name: "key Secret missing", model: "tiny", withAuth: true, wantCheck: "auth", wantMessage: "No usable API key"},
		{name: "model not served", key: checkKey, model: "big", withAuth: true, wantCheck: "model", wantMessage: "the server lists tiny, other"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			secretsDir := t.TempDir()
			if scenario.key != "" {
				writeSecret(t, secretsDir, "vllm-key", "token", []byte(scenario.key))
			}
			request := checkRequest(t, fmt.Sprintf(`{"target": %s}`, platformTarget(server.URL+"/v1", scenario.model, scenario.withAuth)))
			result := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, request)
			assertNoSecret(t, result)
			if result.Status != spec.CheckStatusFailed {
				t.Fatalf("the check fails: %s", statuses(result))
			}
			failed := checkNamed(t, result, spec.CheckName(scenario.wantCheck))
			if failed.Status != spec.CheckStepStatusFailed || !strings.Contains(failed.Message, scenario.wantMessage) {
				t.Errorf("%s: want failed with %q, got %s %q", scenario.wantCheck, scenario.wantMessage, failed.Status, failed.Message)
			}
			if scenario.wantCheck == "auth" && checkNamed(t, result, spec.CheckNameModel).Status != spec.CheckStepStatusSkipped {
				t.Errorf("model depends on auth and is skipped: %s", statuses(result))
			}
		})
	}
}

func TestCheckTargetStopsAtARefusedConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // nothing listens there now
	request := checkRequest(t, fmt.Sprintf(`{"target": %s}`, platformTarget("http://"+address+"/v1", "tiny", false)))
	result := CheckTarget(context.Background(), CheckOptions{SecretsDir: t.TempDir()}, request)
	want := "resolve=ok connect=failed tls=skipped auth=skipped model=skipped generate=skipped"
	if got := statuses(result); !strings.HasPrefix(got, want) {
		t.Errorf("checks after a failed connect are skipped:\n got %s\nwant %s…", got, want)
	}
}

func mtlsTarget(endpoint, certSecret string) string {
	return fmt.Sprintf(`{"type": "openai-compatible", "endpoint": %q, "model": "tiny", "auth": {"secretRef": {"name": "key", "key": "token"}}, "tls": {"mode": "mtls", "secretRef": {"name": %q}}}`, endpoint, certSecret)
}

func TestCheckTargetMTLSHandshakesWithTheClientCertificate(t *testing.T) {
	serverCA, clientCA := tlstest.NewAuthority(t), tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, serverCA, clientCA, fakeOpenAI("tiny"))
	client := clientCA.IssueClient(t)
	secretsDir := t.TempDir()
	writeSecret(t, secretsDir, "key", "token", []byte(checkKey))
	writeSecret(t, secretsDir, "bench-tls", "tls.crt", client.CertPEM)
	writeSecret(t, secretsDir, "bench-tls", "tls.key", client.KeyPEM)
	writeSecret(t, secretsDir, "bench-tls", "ca.crt", serverCA.CertPEM)

	result := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, checkRequest(t, fmt.Sprintf(`{"target": %s}`, mtlsTarget(server.URL+"/v1", "bench-tls"))))
	assertNoSecret(t, result)
	if tlsCheck := checkNamed(t, result, spec.CheckNameTls); tlsCheck.Status != spec.CheckStepStatusOk || !strings.Contains(tlsCheck.Message, "client certificate") {
		t.Fatalf("the mtls handshake succeeds: %+v", tlsCheck)
	}
	if result.Status != spec.CheckStatusOk {
		t.Errorf("an authenticated mtls target passes: %s", statuses(result))
	}

	// A certificate the server does not trust is refused at the handshake.
	stranger := tlstest.NewAuthority(t).IssueClient(t)
	writeSecret(t, secretsDir, "stranger-tls", "tls.crt", stranger.CertPEM)
	writeSecret(t, secretsDir, "stranger-tls", "tls.key", stranger.KeyPEM)
	writeSecret(t, secretsDir, "stranger-tls", "ca.crt", serverCA.CertPEM)
	refused := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, checkRequest(t, fmt.Sprintf(`{"target": %s}`, mtlsTarget(server.URL+"/v1", "stranger-tls"))))
	if checkNamed(t, refused, spec.CheckNameTls).Status != spec.CheckStepStatusFailed && checkNamed(t, refused, spec.CheckNameAuth).Status != spec.CheckStepStatusFailed {
		t.Errorf("an untrusted client certificate is refused: %s", statuses(refused))
	}

	// mtls declared against an http:// URL never connects in plain text.
	plain := tlstest.NewPlatformServer(t, fakeOpenAI("tiny"))
	mixed := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, checkRequest(t, fmt.Sprintf(`{"target": %s}`, mtlsTarget(plain.URL+"/v1", "bench-tls"))))
	if checkNamed(t, mixed, spec.CheckNameTls).Status != spec.CheckStepStatusFailed {
		t.Errorf("mtls with an http:// endpoint fails the tls check: %s", statuses(mixed))
	}
}

func tlsTarget(endpoint, caSecret string) string {
	tls := `{"mode": "tls"}`
	if caSecret != "" {
		tls = fmt.Sprintf(`{"mode": "tls", "caSecretRef": {"name": %q}}`, caSecret)
	}
	return fmt.Sprintf(`{"type": "openai-compatible", "endpoint": %q, "model": "tiny", "auth": {"secretRef": {"name": "key", "key": "token"}}, "tls": %s}`, endpoint, tls)
}

func TestCheckTargetTLSVerifiesTheServerWithoutAClientCertificate(t *testing.T) {
	serverCA := tlstest.NewAuthority(t)
	server := tlstest.NewTLSServer(t, serverCA, fakeOpenAI("tiny"))
	secretsDir := t.TempDir()
	writeSecret(t, secretsDir, "key", "token", []byte(checkKey))
	writeSecret(t, secretsDir, "vllm-ca", "ca.crt", serverCA.CertPEM)

	result := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, checkRequest(t, fmt.Sprintf(`{"target": %s}`, tlsTarget(server.URL+"/v1", "vllm-ca"))))
	assertNoSecret(t, result)
	if tlsCheck := checkNamed(t, result, spec.CheckNameTls); tlsCheck.Status != spec.CheckStepStatusOk || !strings.Contains(tlsCheck.Message, "without a client certificate") {
		t.Fatalf("the tls handshake succeeds: %+v", tlsCheck)
	}
	if result.Status != spec.CheckStatusOk {
		t.Errorf("an authenticated tls target passes: %s", statuses(result))
	}

	// Without the private CA the server is not trusted.
	untrusted := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, checkRequest(t, fmt.Sprintf(`{"target": %s}`, tlsTarget(server.URL+"/v1", ""))))
	if checkNamed(t, untrusted, spec.CheckNameTls).Status != spec.CheckStepStatusFailed {
		t.Errorf("a server signed by an unknown CA fails the tls check: %s", statuses(untrusted))
	}

	// A caSecretRef without ca.crt fails, rather than falling back silently.
	writeSecret(t, secretsDir, "empty-ca", "other", []byte("x"))
	missing := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, checkRequest(t, fmt.Sprintf(`{"target": %s}`, tlsTarget(server.URL+"/v1", "empty-ca"))))
	if checkNamed(t, missing, spec.CheckNameTls).Status != spec.CheckStepStatusFailed {
		t.Errorf("a CA Secret without ca.crt fails the tls check: %s", statuses(missing))
	}

	// A server that requires client certificates refuses a tls client.
	mtlsServer := tlstest.NewMTLSServer(t, serverCA, tlstest.NewAuthority(t), fakeOpenAI("tiny"))
	refused := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, checkRequest(t, fmt.Sprintf(`{"target": %s}`, tlsTarget(mtlsServer.URL+"/v1", "vllm-ca"))))
	if refused.Status == spec.CheckStatusOk {
		t.Errorf("an mtls server refuses a tls client: %s", statuses(refused))
	}
}

// fakePrometheus answers instant queries: the vllm marker with one series, and
// DCGM queries with none or one.
func fakePrometheus(t *testing.T, withDCGM bool) string {
	t.Helper()
	server := tlstest.NewPlatformServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.FormValue("query")
		result := `[]`
		if strings.Contains(query, "vllm:") || (withDCGM && strings.Contains(query, "DCGM_")) {
			result = fmt.Sprintf(`[{"metric":{"__name__":"x"},"value":[%d,"1"]}]`, time.Now().Unix())
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
	}))
	return server.URL
}

func TestCheckTargetFindsTheTargetsSeriesInPrometheus(t *testing.T) {
	server := tlstest.NewPlatformServer(t, fakeOpenAI("tiny"))
	secretsDir := t.TempDir()
	writeSecret(t, secretsDir, "vllm-key", "token", []byte(checkKey))
	offCluster := fmt.Sprintf(`{"type": "vllm", "endpoint": %q, "model": "tiny",
		"auth": {"secretRef": {"name": "vllm-key", "key": "token"}},
		"metrics": {"namespace": "servtuner-external", "pod": "vllm-lan", "node": "vllm-lan-gpu"},
		"tls": {"mode": "platform"}}`, server.URL+"/v1")
	for _, withDCGM := range []bool{true, false} {
		prometheusURL := fakePrometheus(t, withDCGM)
		request := checkRequest(t, fmt.Sprintf(`{"target": %s, "prometheus": {"url": %q, "tls": {"mode": "platform"}}}`, offCluster, prometheusURL))
		result := CheckTarget(context.Background(), CheckOptions{SecretsDir: secretsDir}, request)
		if got := checkNamed(t, result, spec.CheckNameMetrics); got.Status != spec.CheckStepStatusOk {
			t.Errorf("the vllm marker is found: %+v", got)
		}
		gpu := checkNamed(t, result, spec.CheckNameGpuMetrics)
		switch {
		case withDCGM && gpu.Status != spec.CheckStepStatusOk:
			t.Errorf("DCGM series are found for the node: %+v", gpu)
		case !withDCGM && (gpu.Status != spec.CheckStepStatusWarning || result.Status != spec.CheckStatusWarning):
			t.Errorf("missing GPU series are a warning, not a failure: %+v, overall %s", gpu, result.Status)
		}
	}
}
