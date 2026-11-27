package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// legacyRunSpecs pairs each servtuner 0.1 RunSpec in testdata/legacy (the
// example as it was before 0.2) with the example it became.
func legacyRunSpecs(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "legacy", "runspec-v1-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no legacy RunSpecs: %v", err)
	}
	pairs := map[string]string{}
	for _, file := range files {
		name := strings.TrimPrefix(filepath.Base(file), "runspec-v1-")
		pairs[file] = filepath.Join("..", "..", "..", "schema", "examples", "runspec", "valid", name)
	}
	return pairs
}

func readJSON(t *testing.T, path string) any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(content, &tree); err != nil {
		t.Fatal(err)
	}
	return tree
}

// TestUpgradeLegacyRunSpecMatchesTheConvertedExamples: an old example,
// upgraded on read, is exactly the example as 0.2 writes it.
func TestUpgradeLegacyRunSpecMatchesTheConvertedExamples(t *testing.T) {
	for legacyFile, currentFile := range legacyRunSpecs(t) {
		legacy, err := os.ReadFile(legacyFile)
		if err != nil {
			t.Fatal(err)
		}
		upgraded, err := UpgradeLegacyRunSpec(legacy)
		if err != nil {
			t.Fatalf("%s: %v", legacyFile, err)
		}
		if _, err := ParseRunSpec(upgraded); err != nil {
			t.Errorf("%s: the upgrade is not a valid RunSpec: %v", legacyFile, err)
		}
		var got any
		if err := json.Unmarshal(upgraded, &got); err != nil {
			t.Fatal(err)
		}
		if want := readJSON(t, currentFile); !reflect.DeepEqual(got, want) {
			t.Errorf("%s upgrades to\n%s\nwant %s", legacyFile, upgraded, currentFile)
		}
	}
}

// TestParseRunSpecRejectsV1AndSaysWhatToChange: a v1 RunSpec someone submits
// is never upgraded silently; the error names the apiVersion and every tls
// block's replacement.
func TestParseRunSpecRejectsV1AndSaysWhatToChange(t *testing.T) {
	for legacyFile := range legacyRunSpecs(t) {
		legacy, err := os.ReadFile(legacyFile)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseRunSpec(legacy)
		if err == nil {
			t.Fatalf("%s: a servtuner.dev/v1 RunSpec was accepted", legacyFile)
		}
		message := err.Error()
		for _, want := range []string{APIVersionV1, "apiVersion: " + APIVersionV2, "target.tls:", `"enabled"`} {
			if !strings.Contains(message, want) {
				t.Errorf("%s: error %q does not mention %q", legacyFile, message, want)
			}
		}
	}
}

func TestUpgradeLegacyRunSpecLeavesV2Alone(t *testing.T) {
	current, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "examples", "runspec", "valid", "minimal-vllm-client-cert.json"))
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := UpgradeLegacyRunSpec(current)
	if err != nil || string(upgraded) != string(current) {
		t.Errorf("a v2 RunSpec changed: %v", err)
	}
}

// TestUpgradeLegacyEnvironment: each 0.1 mode becomes the transport it
// recorded, and the result is a valid environment.json.
func TestUpgradeLegacyEnvironment(t *testing.T) {
	legacy, err := os.ReadFile(filepath.Join("testdata", "legacy", "environment-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(SchemaEnvironment, legacy); err == nil {
		t.Fatal("the format-1 environment is schema-valid as it is: the fixture no longer tests the upgrade")
	}
	upgraded, err := UpgradeBundleDocument(SchemaEnvironment, legacy)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := Parse[Environment](SchemaEnvironment, upgraded)
	if err != nil {
		t.Fatalf("the upgrade is not a valid environment: %v", err)
	}
	want := HopTransports{
		Target:      HopTransport{Tls: true, ClientCert: true},
		Prometheus:  &HopTransport{},
		Api:         &HopTransport{Tls: true, ClientCert: true},
		ObjectStore: &HopTransport{},
	}
	if !reflect.DeepEqual(environment.Transport, want) {
		t.Errorf("transport = %+v, want %+v", environment.Transport, want)
	}
	again, err := UpgradeLegacyEnvironment(upgraded)
	if err != nil || string(again) != string(upgraded) {
		t.Errorf("upgrading a current environment changed it: %v", err)
	}
}

func TestUpgradeBundleDocumentPassesOtherSchemasThrough(t *testing.T) {
	document := []byte(`{"apiVersion":"servtuner.dev/v1","transport":{"target":"mtls"}}`)
	got, err := UpgradeBundleDocument(SchemaManifest, document)
	if err != nil || string(got) != string(document) {
		t.Errorf("a manifest changed: %s, %v", got, err)
	}
}
