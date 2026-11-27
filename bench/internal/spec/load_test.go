package spec

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const yamlFixture = "../../testdata/runspec-minimal.yaml"

func TestLoadRunSpecYAMLMatchesJSON(t *testing.T) {
	yamlDocument, err := os.ReadFile(yamlFixture)
	if err != nil {
		t.Fatal(err)
	}
	fromYAML, asJSON, err := LoadRunSpec(yamlDocument)
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := ParseRunSpec(asJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML, fromJSON) || fromYAML.Name != "vllm-smoke-yaml" || fromYAML.Load.Stages[0].Rate != 1 {
		t.Errorf("YAML and JSON forms differ: %+v vs %+v", fromYAML, fromJSON)
	}
}

func TestLoadRunSpecPassesJSONThroughUntouched(t *testing.T) {
	document, err := os.ReadFile(filepath.Join(examplesRoot, "runspec", "valid", "router-bifrost-platform.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, asJSON, err := LoadRunSpec(document)
	if err != nil || string(asJSON) != string(document) {
		t.Errorf("JSON was altered: %v", err)
	}
}

func TestLoadRunSpecRejectsBadDocuments(t *testing.T) {
	for name, document := range map[string]string{
		"not yaml":         "a: [unclosed",
		"schema violation": "apiVersion: servtuner.dev/v1\nkind: RunSpec\n",
		"empty object":     "{}",
	} {
		if _, _, err := LoadRunSpec([]byte(document)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestModeOf(t *testing.T) {
	cases := map[string]struct {
		tls  Tls
		want TransportMode
	}{
		"mtls":     {map[string]any{"mode": "mtls", "secretRef": map[string]any{"name": "s"}}, TransportModeMtls},
		"platform": {map[string]any{"mode": "platform"}, TransportModePlatform},
	}
	for name, c := range cases {
		if got, err := ModeOf(c.tls); err != nil || got != c.want {
			t.Errorf("%s: ModeOf = %q, %v", name, got, err)
		}
	}
	for name, tls := range map[string]Tls{
		"platform with secretRef": map[string]any{"mode": "platform", "secretRef": map[string]any{"name": "s"}},
		"unknown mode":            map[string]any{"mode": "plain"},
		"skip verify":             map[string]any{"mode": "mtls", "secretRef": map[string]any{"name": "s"}, "skipVerify": true},
	} {
		if _, err := ModeOf(tls); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestTimeConversions(t *testing.T) {
	moment := time.Date(2026, 10, 28, 7, 0, 0, 500, time.FixedZone("EST", -5*3600))
	stamp := NewUTCTimestamp(moment)
	if err := Validate("phases", []byte(`{"phases":[{"name":"warmup","kind":"warmup","start":"`+string(stamp)+`","end":"`+string(stamp)+`"}]}`)); err != nil {
		t.Errorf("timestamp %s is not schema-valid: %v", stamp, err)
	}
	back, err := stamp.Time()
	if err != nil || !back.Equal(moment) || back.Location() != time.UTC {
		t.Errorf("round trip = %v, %v", back, err)
	}
	if _, err := UtcTimestamp("2026-02-30T00:00:00Z").Time(); err == nil {
		t.Error("impossible date accepted")
	}
	if got, err := Duration("1m30s").Std(); err != nil || got != 90*time.Second {
		t.Errorf("Std = %v, %v", got, err)
	}
	defaults, err := DefaultStageValidity()
	if err != nil || defaults.MinAchievedRpsRatio != 0.97 || defaults.MaxSendLagP99 != "50ms" {
		t.Errorf("defaults = %+v, %v", defaults, err)
	}
}
