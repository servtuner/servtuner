package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// With credentials source ambient, the store uses the SDK's default chain,
// which is how EKS Pod Identity reaches it: the agent's container-credentials
// endpoint, authorised by a token file. A fake agent and a fake S3
// endpoint show the chain fetched the agent's keys and signed with them.
func TestAmbientCredentialsComeFromTheContainerCredentialsEndpoint(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "eks-pod-identity-token")
	if err := os.WriteFile(tokenFile, []byte("pod-identity-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pod-identity-token" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"AccessKeyId": "ASIAPODIDENTITY", "SecretAccessKey": "pod-secret", "Token": "pod-session-token",
			"Expiration": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer agent.Close()
	var mu sync.Mutex
	var authorization, securityToken string
	s3Fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorization, securityToken = r.Header.Get("Authorization"), r.Header.Get("X-Amz-Security-Token")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>b</Name><IsTruncated>false</IsTruncated></ListBucketResult>`))
	}))
	defer s3Fake.Close()
	for name, value := range map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     agent.URL + "/v1/credentials",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
		"AWS_ACCESS_KEY_ID":                      "", "AWS_SECRET_ACCESS_KEY": "", "AWS_PROFILE": "",
		"AWS_CONFIG_FILE":             filepath.Join(t.TempDir(), "none"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(t.TempDir(), "none"),
	} {
		t.Setenv(name, value)
	}
	store, err := NewS3Store(context.Background(), S3Config{
		Endpoint: s3Fake.URL, Region: "us-east-1", Bucket: "b", ForcePathStyle: true, CredentialsSource: CredentialsAmbient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(authorization, "Credential=ASIAPODIDENTITY/") || securityToken != "pod-session-token" {
		t.Errorf("signed with %q, token %q: not the agent's credentials", authorization, securityToken)
	}
}

func TestSecretCredentialsNeedBothKeys(t *testing.T) {
	_, err := NewS3Store(context.Background(), S3Config{Region: "us-east-1", Bucket: "b", CredentialsSource: CredentialsSecret,
		Getenv: func(name string) string { return map[string]string{"AWS_ACCESS_KEY_ID": "only-one"}[name] }})
	if err == nil {
		t.Error("one key accepted")
	}
	if _, err := NewS3Store(context.Background(), S3Config{Region: "us-east-1", Bucket: "b", CredentialsSource: "guess"}); err == nil {
		t.Error("an unknown source accepted")
	}
	if _, err := NewS3Store(context.Background(), S3Config{Region: "us-east-1", Bucket: "b", CredentialsSource: CredentialsAmbient, ChecksumMode: "always"}); err == nil {
		t.Error("an unknown checksum mode accepted")
	}
}

// With nothing in the chain the store refuses to open, before any run starts,
// instead of signing anonymously. The instance metadata service is
// switched off so the chain fails at once rather than waiting on it.
func TestAmbientWithNoCredentialsFailsFast(t *testing.T) {
	for name, value := range map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": "", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": "", "AWS_WEB_IDENTITY_TOKEN_FILE": "", "AWS_ROLE_ARN": "",
		"AWS_ACCESS_KEY_ID": "", "AWS_SECRET_ACCESS_KEY": "", "AWS_SESSION_TOKEN": "", "AWS_PROFILE": "",
		"AWS_CONFIG_FILE":             filepath.Join(t.TempDir(), "none"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(t.TempDir(), "none"),
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		t.Setenv(name, value)
	}
	started := time.Now()
	_, err := NewS3Store(context.Background(), S3Config{Region: "us-east-1", Bucket: "b", CredentialsSource: CredentialsAmbient})
	if err == nil || !strings.Contains(err.Error(), "no AWS credentials resolved") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("took %v to fail", elapsed)
	}
}
