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
	document, err := os.ReadFile(filepath.Join(examplesRoot, "runspec", "valid", "router-bifrost-tls-off.json"))
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
		"schema violation": "apiVersion: servtuner.dev/v2\nkind: RunSpec\n",
		"empty object":     "{}",
	} {
		if _, _, err := LoadRunSpec([]byte(document)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestTransportOf(t *testing.T) {
	cases := map[string]struct {
		tls  Tls
		want HopTransport
	}{
		"client cert": {map[string]any{"enabled": true, "clientCert": map[string]any{"secretRef": map[string]any{"name": "s"}}}, HopTransport{Tls: true, ClientCert: true}},
		"tls":         {map[string]any{"enabled": true}, HopTransport{Tls: true}},
		"disabled":    {map[string]any{"enabled": false}, HopTransport{}},
	}
	for name, c := range cases {
		if got, err := TransportOf(c.tls); err != nil || got != c.want {
			t.Errorf("%s: TransportOf = %+v, %v", name, got, err)
		}
	}
	for name, tls := range map[string]Tls{
		"disabled with a client cert": map[string]any{"enabled": false, "clientCert": map[string]any{"secretRef": map[string]any{"name": "s"}}},
		"0.1 mode":                    map[string]any{"mode": "plain"},
		"skip verify":                 map[string]any{"enabled": true, "skipVerify": true},
	} {
		if _, err := TransportOf(tls); err == nil {
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
