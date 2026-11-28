package target

import (
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Every knob servtuner captures by name is in SGLang's catalog under the same
// flag, except the metrics switch servtuner sets itself.
func TestEveryBuiltInSGLangKnobIsInItsCatalog(t *testing.T) {
	for _, flag := range sglangFlags {
		if flag.knob == KnobSGLangEnableMetrics {
			if IsSGLangKnob(flag.knob) {
				t.Errorf("%s is servtuner's to set, not a knob", flag.knob)
			}
			continue
		}
		described, known := sglangKnobsByName()[flag.knob]
		if !known {
			t.Errorf("%s is not in the SGLang catalog", flag.knob)
			continue
		}
		if described.Flag != flag.flags[0] {
			t.Errorf("%s: catalog flag %s, table flag %s", flag.knob, described.Flag, flag.flags[0])
		}
		if _, labelled := sglangKnobLabels[flag.knob]; !labelled {
			t.Errorf("%s has no label", flag.knob)
		}
	}
}

// Secrets and the fields servtuner sets are never knobs, but are spellings
// SGLang accepts.
func TestSGLangSecretsAndOwnedFieldsAreNotKnobs(t *testing.T) {
	catalog, err := SGLangKnobCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Server != spec.KnobCatalogServerSglang || catalog.Version == "" || len(catalog.Commit) != 40 {
		t.Fatalf("catalog header: %s %s %s", catalog.Server, catalog.Version, catalog.Commit)
	}
	names := map[string]bool{}
	for _, knob := range catalog.Knobs {
		if names[knob.Name] {
			t.Errorf("%s is described twice", knob.Name)
		}
		names[knob.Name] = true
		field := strings.TrimPrefix(knob.Name, sglangPrefix)
		if sglangOwnedFields[field] || IsSecretName(field) {
			t.Errorf("%s is servtuner's or a secret, not a knob", knob.Name)
		}
		if knob.Help == "" && knob.BuiltIn {
			t.Errorf("built-in %s has no help", knob.Name)
		}
	}
	other := strings.Join(catalog.OtherFlags, " ")
	for _, spelling := range []string{"--host", "--port", "--model-path", "--api-key", "--enable-metrics"} {
		if !strings.Contains(other, spelling) {
			t.Errorf("other_flags lacks %s", spelling)
		}
	}
	for _, knob := range []string{KnobSGLangMemFractionStatic, KnobSGLangSchedulePolicy, KnobSGLangDisableRadixCache} {
		if !names[knob] {
			t.Errorf("%s is missing", knob)
		}
	}
}

func TestKnobCatalogForPicksTheServer(t *testing.T) {
	for _, server := range []spec.KnobCatalogServer{spec.KnobCatalogServerVllm, spec.KnobCatalogServerSglang} {
		catalog, err := KnobCatalogFor(server)
		if err != nil || catalog.Server != server {
			t.Errorf("%s: %s %v", server, catalog.Server, err)
		}
	}
	if _, err := KnobCatalogFor("tgi"); err == nil {
		t.Error("an unknown server must be an error")
	}
}
