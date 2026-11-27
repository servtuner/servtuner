package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

	t.Run("valid examples decode to what their own block says", func(t *testing.T) {
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
				raw := tls.(map[string]any)
				decoded, err := DecodeTLS(tls)
				if err != nil {
					t.Fatalf("%s: %v", filepath.Base(file), err)
				}
				_, hasCert := raw["clientCert"]
				_, hasCA := raw["caSecretRef"]
				if decoded.Enabled != raw["enabled"] || hasCert != (decoded.ClientCert != nil) || hasCA != (decoded.CA != nil) {
					t.Errorf("%s: %v decoded to %+v", filepath.Base(file), raw, decoded)
				}
			}
		}
	})

	t.Run("enabled decodes with and without a client certificate and a CA", func(t *testing.T) {
		for tlsJSON, want := range map[string]struct{ cert, ca string }{
			`{"enabled":true}`: {},
			`{"enabled":true,"caSecretRef":{"name":"vllm-ca"}}`:                                    {ca: "vllm-ca"},
			`{"enabled":true,"clientCert":{"secretRef":{"name":"servtuner-client"}}}`:              {cert: "servtuner-client"},
			`{"enabled":true,"clientCert":{"secretRef":{"name":"c"}},"caSecretRef":{"name":"ca"}}`: {cert: "c", ca: "ca"},
		} {
			decoded, err := DecodeTLS(tlsOf(t, tlsJSON))
			if err != nil {
				t.Fatalf("%s: %v", tlsJSON, err)
			}
			gotCert, gotCA := "", ""
			if decoded.ClientCert != nil {
				gotCert = decoded.ClientCert.Name
			}
			if decoded.CA != nil {
				gotCA = decoded.CA.Name
			}
			if !decoded.Enabled || gotCert != want.cert || gotCA != want.ca {
				t.Errorf("%s: decoded to %+v", tlsJSON, decoded)
			}
			if transport := decoded.Transport(); !transport.Tls || transport.ClientCert != (want.cert != "") {
				t.Errorf("%s: transport %+v", tlsJSON, transport)
			}
		}
		decoded, err := DecodeTLS(tlsOf(t, `{"enabled":false}`))
		if err != nil || decoded != (DecodedTLS{}) || decoded.Transport() != (HopTransport{}) {
			t.Errorf("enabled false decoded to %+v, %v", decoded, err)
		}
	})

	t.Run("a servtuner 0.1 block names its replacement", func(t *testing.T) {
		for legacy, want := range map[string]string{
			`{"mode":"mtls","secretRef":{"name":"servtuner-client"}}`: `{"clientCert":{"secretRef":{"name":"servtuner-client"}},"enabled":true}`,
			`{"mode":"tls","caSecretRef":{"name":"ca"}}`:              `{"caSecretRef":{"name":"ca"},"enabled":true}`,
			`{"mode":"platform"}`:                                     `{"enabled":false}`,
		} {
			_, err := DecodeTLS(tlsOf(t, legacy))
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "servtuner 0.2") {
				t.Errorf("%s: error %v, want it to name %s", legacy, err, want)
			}
		}
	})

	rejected := map[string]Tls{
		"disabled with a client cert":        tlsOf(t, `{"enabled":false,"clientCert":{"secretRef":{"name":"servtuner-client"}}}`),
		"disabled with a CA":                 tlsOf(t, `{"enabled":false,"caSecretRef":{"name":"ca"}}`),
		"disabled with skipVerify":           tlsOf(t, `{"enabled":false,"skipVerify":true}`),
		"client cert without secretRef":      tlsOf(t, `{"enabled":true,"clientCert":{}}`),
		"enabled with an extra key":          tlsOf(t, `{"enabled":true,"clientCert":{"secretRef":{"name":"servtuner-client"}},"skipVerify":true}`),
		"client cert secretRef extra key":    tlsOf(t, `{"enabled":true,"clientCert":{"secretRef":{"name":"servtuner-client","key":"tls.key"}}}`),
		"enabled with a top-level secretRef": tlsOf(t, `{"enabled":true,"secretRef":{"name":"servtuner-client"}}`),
		"enabled with skipVerify":            tlsOf(t, `{"enabled":true,"skipVerify":true}`),
		"caSecretRef extra key":              tlsOf(t, `{"enabled":true,"caSecretRef":{"name":"ca","key":"ca.crt"}}`),
		"enabled not a boolean":              tlsOf(t, `{"enabled":"true"}`),
		"missing enabled":                    tlsOf(t, `{"clientCert":{"secretRef":{"name":"servtuner-client"}}}`),
		"0.1 mtls":                           tlsOf(t, `{"mode":"mtls","secretRef":{"name":"servtuner-client"}}`),
		"0.1 unknown mode":                   tlsOf(t, `{"mode":"insecure"}`),
		"nil":                                nil,
		"non-object":                         "mtls",
	}
	for name, tls := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := DecodeTLS(tls); err == nil {
				t.Error("want an error")
			}
		})
	}
}
