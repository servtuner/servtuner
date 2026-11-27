package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// examplesRoot is the one fixture set shared with the Python tests: neither
// language keeps copies ("Tests share fixtures").
const examplesRoot = "../../../schema/examples"

// typedDocuments maps a schema name to a constructor for its generated type.
var typedDocuments = map[string]func() any{
	"runspec":         func() any { return new(RunSpec) },
	"workload-preset": func() any { return new(WorkloadPreset) },

	SchemaSearchSpaceTemplate: func() any { return new(SearchSpaceTemplate) },
	"manifest":                func() any { return new(Manifest) },
	"phases":                  func() any { return new(Phases) },
	"environment":             func() any { return new(Environment) },
	"summary":                 func() any { return new(Summary) },
	"request-record":          func() any { return new(RequestRecord) },
	"events":                  func() any { return new(Event) },

	SchemaPrometheusQueries:  func() any { return new(PrometheusQueries) },
	SchemaPrometheusTargets:  func() any { return new(PrometheusTargets) },
	SchemaTargetSnapshot:     func() any { return new(TargetSnapshot) },
	SchemaClientSample:       func() any { return new(ClientSample) },
	SchemaAnalysis:           func() any { return new(AnalysisResult) },
	SchemaRecommendations:    func() any { return new(Recommendations) },
	SchemaCapacity:           func() any { return new(CapacityTable) },
	SchemaCapacityModel:      func() any { return new(RunCapacityModel) },
	SchemaPlanRequest:        func() any { return new(PlanRequest) },
	SchemaPlan:               func() any { return new(Plan) },
	SchemaComparison:         func() any { return new(Comparison) },
	SchemaChartTimeseries:    func() any { return new(ClientTimeseries) },
	SchemaChartHistograms:    func() any { return new(LatencyHistograms) },
	SchemaChartInFlight:      func() any { return new(InFlightChart) },
	SchemaChartServer:        func() any { return new(ServerChart) },
	SchemaInfraPricing:       func() any { return new(InfraPricing) },
	SchemaTokenPricing:       func() any { return new(TokenPricing) },
	SchemaCheckTargetRequest: func() any { return new(CheckTargetRequest) },
	SchemaCheckTargetResult:  func() any { return new(CheckTargetResult) },
	SchemaSearchSpace:        func() any { return new(SearchSpace) },
	SchemaBootResults:        func() any { return new(BootResults) },
	SchemaBootProbeRequest:   func() any { return new(BootProbeRequest) },
	SchemaKnobCatalog:        func() any { return new(KnobCatalog) },
	SchemaHostMachine:        func() any { return new(HostMachine) },
	SchemaProposal:           func() any { return new(Proposal) },
}

func exampleFiles(t *testing.T, schemaName, validity string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(examplesRoot, schemaName, validity, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestEverySchemaHasExamples(t *testing.T) {
	names := SchemaNames()
	if len(names) != len(typedDocuments) {
		t.Fatalf("schemas %v do not match the typed documents table", names)
	}
	for _, name := range names {
		if _, ok := typedDocuments[name]; !ok {
			t.Errorf("schema %s has no generated type in typedDocuments", name)
		}
		for _, validity := range []string{"valid", "invalid"} {
			if len(exampleFiles(t, name, validity)) == 0 {
				t.Errorf("schema %s has no %s examples", name, validity)
			}
		}
	}
	if got := len(exampleFiles(t, "runspec", "valid")); got < 3 {
		t.Errorf("want at least 3 valid RunSpecs, got %d", got)
	}
	if got := len(exampleFiles(t, "runspec", "invalid")); got < 5 {
		t.Errorf("want at least 5 invalid RunSpecs, got %d", got)
	}
}

// TestExamplesMatchSchema is half of the cross-language conformance proof:
// every example must validate (or not) exactly as its directory says.
func TestExamplesMatchSchema(t *testing.T) {
	for _, name := range SchemaNames() {
		for _, validity := range []string{"valid", "invalid"} {
			for _, file := range exampleFiles(t, name, validity) {
				t.Run(fmt.Sprintf("%s/%s/%s", name, validity, filepath.Base(file)), func(t *testing.T) {
					document, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					err = Validate(name, document)
					if validity == "valid" && err != nil {
						t.Errorf("valid example rejected: %v", err)
					}
					if validity == "invalid" && err == nil {
						t.Errorf("invalid example accepted")
					}
				})
			}
		}
	}
}

// TestValidExamplesRoundTrip is the other half: the generated types must carry
// every field of a valid example through unmarshal and marshal unchanged.
func TestValidExamplesRoundTrip(t *testing.T) {
	for _, name := range SchemaNames() {
		for _, file := range exampleFiles(t, name, "valid") {
			t.Run(fmt.Sprintf("%s/%s", name, filepath.Base(file)), func(t *testing.T) {
				original, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				typed := typedDocuments[name]()
				if err := json.Unmarshal(original, typed); err != nil {
					t.Fatalf("unmarshal into generated type: %v", err)
				}
				remarshaled, err := json.Marshal(typed)
				if err != nil {
					t.Fatalf("marshal generated type: %v", err)
				}
				if err := Validate(name, remarshaled); err != nil {
					t.Fatalf("round-tripped document no longer valid: %v\n%s", err, remarshaled)
				}

				// A second pass must change nothing, or a default overwrote an explicit value.
				again := typedDocuments[name]()
				if err := json.Unmarshal(remarshaled, again); err != nil {
					t.Fatalf("unmarshal round-tripped document: %v", err)
				}
				if !reflect.DeepEqual(typed, again) {
					t.Errorf("second round trip differs:\nfirst:  %+v\nsecond: %+v", typed, again)
				}

				// Nothing the example said may be dropped; only defaults may be added.
				var want, got any
				if err := json.Unmarshal(original, &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(remarshaled, &got); err != nil {
					t.Fatal(err)
				}
				if missing := findMissing("", want, got); missing != "" {
					t.Errorf("round trip lost or changed %s", missing)
				}
			})
		}
	}
}

// findMissing returns the path of the first value in want that got lacks or
// changes, or "" when got contains all of want.
func findMissing(prefix string, want, got any) string {
	switch wantValue := want.(type) {
	case map[string]any:
		gotMap, ok := got.(map[string]any)
		if got == nil && len(wantValue) == 0 {
			return "" // as for arrays: empty objects are omitted and come back as the empty default
		}
		if !ok {
			return prefix
		}
		keys := make([]string, 0, len(wantValue))
		for key := range wantValue {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if missing := findMissing(prefix+"/"+key, wantValue[key], gotMap[key]); missing != "" {
				return missing
			}
		}
		return ""
	case []any:
		gotSlice, ok := got.([]any)
		if got == nil && len(wantValue) == 0 {
			return "" // go-jsonschema omits empty arrays; absent means the declared default, which is empty
		}
		if !ok || len(gotSlice) != len(wantValue) {
			return prefix
		}
		for i := range wantValue {
			if missing := findMissing(fmt.Sprintf("%s/%d", prefix, i), wantValue[i], gotSlice[i]); missing != "" {
				return missing
			}
		}
		return ""
	case string:
		if wantValue == got {
			return ""
		}
		return prefix
	default:
		if got == nil && (want == false || want == float64(0)) {
			// omitempty drops zero scalars. That is only safe when the default is the zero
			// value too, which the second round trip in the caller proves.
			return ""
		}
		if !reflect.DeepEqual(want, got) {
			return prefix
		}
		return ""
	}
}

// TestInvalidRunSpecsAreRejectedByParse mirrors Python's
// test_generated_runspec_model_rejects_invalid_examples: the typed entry point
// must refuse everything the schema refuses, TLS mixing included.
func TestInvalidRunSpecsAreRejectedByParse(t *testing.T) {
	for _, file := range exampleFiles(t, "runspec", "invalid") {
		t.Run(filepath.Base(file), func(t *testing.T) {
			document, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseRunSpec(document); err == nil {
				t.Error("ParseRunSpec accepted an invalid RunSpec")
			}
		})
	}
}

func TestParseRunSpecAcceptsValidExamples(t *testing.T) {
	for _, file := range exampleFiles(t, "runspec", "valid") {
		document, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseRunSpec(document); err != nil {
			t.Errorf("%s: %v", filepath.Base(file), err)
		}
	}
}

// dropOmittedValues removes what go-jsonschema's omitempty drops on marshal:
// empty arrays and objects, false and 0. These defaults are applied on
// unmarshal (the typed assertions below check the maps) but cannot be seen in
// marshalled JSON, so the comparison with the shared expectations ignores them.
func dropOmittedValues(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		kept := map[string]any{}
		for key, child := range typed {
			if reduced := dropOmittedValues(child); reduced != nil {
				kept[key] = reduced
			}
		}
		if len(kept) == 0 {
			return nil
		}
		return kept
	case []any:
		if len(typed) == 0 {
			return nil
		}
		return typed
	case bool:
		if !typed {
			return nil
		}
	case float64:
		if typed == 0 {
			return nil
		}
	}
	return value
}

// TestDefaultsAreApplied checks every schema default against the expectations
// shared with Python (schema/examples/runspec/defaults).
func TestDefaultsAreApplied(t *testing.T) {
	read := func(name string) []byte {
		document, err := os.ReadFile(filepath.Join(examplesRoot, "runspec", "defaults", name))
		if err != nil {
			t.Fatal(err)
		}
		return document
	}
	if err := Validate("runspec", read("expected.json")); err != nil {
		t.Fatalf("expected.json is not a valid RunSpec: %v", err)
	}
	runSpec, err := ParseRunSpec(read("input.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runSpec.Knobs == nil || runSpec.Labels == nil || runSpec.Target.Upstreams == nil || runSpec.Engine.Options == nil {
		t.Errorf("map and array defaults must be non-nil empties: %+v", runSpec)
	}

	applied, err := json.Marshal(runSpec)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(applied, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(read("expected.json"), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dropOmittedValues(got), dropOmittedValues(want)) {
		t.Errorf("defaults differ from expected.json:\ngot:  %s\nwant: %s", applied, read("expected.json"))
	}
}

func TestDecodeTLS(t *testing.T) {
	tlsOf := func(t *testing.T, tlsJSON string) Tls {
		t.Helper()
		var tls Tls
		if err := json.Unmarshal([]byte(tlsJSON), &tls); err != nil {
			t.Fatal(err)
		}
		return tls
	}

	t.Run("valid examples decode to the variant named by their own mode", func(t *testing.T) {
		for _, file := range exampleFiles(t, "runspec", "valid") {
			document, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			runSpec, err := ParseRunSpec(document)
			if err != nil {
				t.Fatal(err)
			}
			tlsBlocks := []Tls{runSpec.Target.Tls}
			if runSpec.Prometheus != nil {
				tlsBlocks = append(tlsBlocks, runSpec.Prometheus.Tls)
			}
			for _, tls := range tlsBlocks {
				wantMode := tls.(map[string]any)["mode"]
				decoded, err := DecodeTLS(tls)
				if err != nil {
					t.Fatalf("%s: %v", filepath.Base(file), err)
				}
				if string(decoded.Mode) != wantMode || (wantMode == "mtls") != (decoded.Mtls != nil) || (wantMode == "tls") != (decoded.Server != nil) {
					t.Errorf("%s: mode %v decoded to %+v", filepath.Base(file), wantMode, decoded)
				}
			}
		}
	})

	t.Run("tls decodes with and without a CA Secret", func(t *testing.T) {
		for tlsJSON, wantCA := range map[string]string{
			`{"mode":"tls"}`: "",
			`{"mode":"tls","caSecretRef":{"name":"vllm-ca"}}`: "vllm-ca",
		} {
			decoded, err := DecodeTLS(tlsOf(t, tlsJSON))
			if err != nil {
				t.Fatalf("%s: %v", tlsJSON, err)
			}
			if decoded.Mode != TransportModeTls || decoded.Server == nil || decoded.Mtls != nil {
				t.Fatalf("%s: decoded to %+v", tlsJSON, decoded)
			}
			gotCA := ""
			if decoded.Server.CaSecretRef != nil {
				gotCA = decoded.Server.CaSecretRef.Name
			}
			if gotCA != wantCA {
				t.Errorf("%s: CA Secret %q, want %q", tlsJSON, gotCA, wantCA)
			}
		}
	})

	rejected := map[string]Tls{
		"platform with secretRef":   tlsOf(t, `{"mode":"platform","secretRef":{"name":"servtuner-client"}}`),
		"platform with skipVerify":  tlsOf(t, `{"mode":"platform","skipVerify":true}`),
		"mtls without secretRef":    tlsOf(t, `{"mode":"mtls"}`),
		"mtls with an extra key":    tlsOf(t, `{"mode":"mtls","secretRef":{"name":"servtuner-client"},"skipVerify":true}`),
		"mtls secretRef extra key":  tlsOf(t, `{"mode":"mtls","secretRef":{"name":"servtuner-client","key":"tls.key"}}`),
		"tls with a client cert":    tlsOf(t, `{"mode":"tls","secretRef":{"name":"servtuner-client"}}`),
		"tls with skipVerify":       tlsOf(t, `{"mode":"tls","skipVerify":true}`),
		"tls caSecretRef extra key": tlsOf(t, `{"mode":"tls","caSecretRef":{"name":"ca","key":"ca.crt"}}`),
		"unknown mode":              tlsOf(t, `{"mode":"insecure"}`),
		"missing mode":              tlsOf(t, `{"secretRef":{"name":"servtuner-client"}}`),
		"nil":                       nil,
		"non-object":                "mtls",
	}
	for name, tls := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := DecodeTLS(tls); err == nil {
				t.Error("want an error")
			}
		})
	}
}
