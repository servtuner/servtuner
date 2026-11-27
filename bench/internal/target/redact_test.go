package target

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIsSecretName(t *testing.T) {
	secret := []string{"HF_TOKEN", "OPENAI_API_KEY", "admin_password", "client_secret", "Authorization", "X-Api-Key", "aws_secret_access_key", "credentials", "--api-key"}
	notSecret := []string{"max_num_batched_tokens", "--max-num-batched-tokens", "TOKENIZERS_PARALLELISM", "--tokenizer", "MODEL", "concurrency", "buffer_size", "VLLM_LOGGING_LEVEL"}
	for _, name := range secret {
		if !IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = false, want true", name)
		}
	}
	for _, name := range notSecret {
		if IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = true, want false", name)
		}
	}
}

func TestRedactEnv(t *testing.T) {
	tests := []struct {
		description string
		name        string
		value       string
		isIndirect  bool
		wantHidden  bool
	}{
		{"secretKeyRef is always redacted, whatever its name", "MODE", "", true, true},
		{"literal with a secret-looking name", "HF_TOKEN", "sentinel-secret-env-token", false, true},
		{"literal with an ordinary name", "VLLM_LOGGING_LEVEL", "INFO", false, false},
		{"literal URL with a password", "DATABASE_URL", "postgres://user:sentinel-secret-env-token@db:5432/x", false, true},
	}
	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			entry := RedactEnv(test.name, test.value, test.isIndirect)
			if entry.IsRedacted != test.wantHidden {
				t.Fatalf("IsRedacted = %v, want %v", entry.IsRedacted, test.wantHidden)
			}
			encoded, _ := json.Marshal(entry)
			if test.wantHidden && (entry.Value != nil || strings.Contains(string(encoded), "sentinel")) {
				t.Errorf("redacted entry carries a value: %s", encoded)
			}
			if !test.wantHidden && (entry.Value == nil || *entry.Value != test.value) {
				t.Errorf("ordinary entry lost its value: %s", encoded)
			}
		})
	}
}

func TestRedactArgs(t *testing.T) {
	got := RedactArgs([]string{
		"serve", "model", "--max-num-batched-tokens", "8192",
		"--api-key", "sentinel-secret-api-key-arg",
		"--hf-token=sentinel-secret-api-key-arg",
		"--max-num-seqs=256",
		"--proxy", "http://u:sentinel-secret-api-key-arg@proxy:3128",
	})
	want := []string{
		"serve", "model", "--max-num-batched-tokens", "8192",
		"--api-key", RedactedPlaceholder,
		"--hf-token=" + RedactedPlaceholder,
		"--max-num-seqs=256",
		"--proxy", RedactedPlaceholder,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RedactArgs =\n %q\nwant\n %q", got, want)
	}
}

func TestRedactJSONHidesProviderKeysAndSecretFields(t *testing.T) {
	var document any
	if err := json.Unmarshal(readFixture(t, "bifrost-providers.json"), &document); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(RedactJSON(document))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range sentinelSecrets {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("redacted document still contains %q:\n%s", secret, encoded)
		}
	}
	for _, kept := range []string{`"concurrency":1000`, `"buffer_size":5000`, `"X-Team":"servtuner"`, `"models":["test-model"]`, `"name":"primary"`} {
		if !strings.Contains(string(encoded), kept) {
			t.Errorf("redaction removed %s:\n%s", kept, encoded)
		}
	}
}

func TestRedactJSONKeepsBooleansAndDoesNotMutateInput(t *testing.T) {
	var document any
	if err := json.Unmarshal(readFixture(t, "bifrost-config.json"), &document); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(document)
	redacted, _ := json.Marshal(RedactJSON(document))
	if !strings.Contains(string(redacted), `"is_enabled":true`) {
		t.Errorf("auth is_enabled flag was lost: %s", redacted)
	}
	if strings.Contains(string(redacted), "sentinel-secret-admin-password") {
		t.Errorf("admin password survived: %s", redacted)
	}
	after, _ := json.Marshal(document)
	if string(original) != string(after) {
		t.Error("RedactJSON modified its input")
	}
}
