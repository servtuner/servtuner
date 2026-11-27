package propose

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// updateFixtures rewrites bench/testdata/propose/runs from the table below:
//
//	go test ./internal/propose -run TestWriteFixtureBundles -update
var updateFixtures = flag.Bool("update", false, "rewrite the fixture bundles under bench/testdata/propose")

const (
	fixtureDir        = "../../testdata/propose"
	minimalRunSpec    = "../../../schema/examples/runspec/valid/minimal-vllm-client-cert.json"
	fixtureAnalyzer   = "1.22.0"
	fixtureTimestamp  = "2026-11-20T10:00:00Z"
	fixtureBenchBuild = "0.0.0"
)

// fixtureBundle is one minimal bundle: the files propose reads, with the
// SLO-max of a recorded RTX 3070 run. See
// bench/testdata/propose/README.md for where each number comes from.
type fixtureBundle struct {
	dir, runID string
	ladder     LadderKind
	knobs      Knobs
	// offered is the ladder, ascending; sloMax lies between two of its rungs,
	// or at or above the last (then the ladder never saw the knee).
	offered []float64
	sloMax  float64
	advice  []map[string]any
}

// sessionKnobs are the knobs every fixture run declares besides the tuned ones.
func sessionKnobs(seqs, batchedTokens float64) Knobs {
	return Knobs{
		"vllm.enable_prefix_caching":  true,
		"vllm.gpu_memory_utilization": 0.75,
		"vllm.max_num_seqs":           seqs,
		"vllm.max_num_batched_tokens": batchedTokens,
		"vllm.async_scheduling":       false,
		"vllm.api_server_count":       1,
	}
}

var (
	// probeLadder is the default 8-rung probe ladder.
	probeLadder = []float64{1, 2, 4, 8, 16, 32, 64, 128}
	// screenLadder has 5 rungs and fullLadder 9, the default lengths.
	screenLadder = []float64{20, 28, 36, 40, 44}
	fullLadder   = []float64{18.96, 26.54, 32.23, 36.02, 37.92, 39.82, 43.61, 49.3, 56.88}
)

// knobAdvice is a vLLM advisor recommendation on a knob, with every field the
// recommendations schema requires.
func knobAdvice(rule, knob string, recommended, max any) map[string]any {
	advice := map[string]any{
		"id": rule + "/target", "rule": rule, "tier": "diagnostic", "confidence": "medium", "recommended": recommended,
		"target":   map[string]any{"role": "target", "target_type": "vllm", "namespace": "inference"},
		"subject":  map[string]any{"kind": "knob", "name": knob},
		"evidence": []any{map[string]any{"text": "recorded in the fixture", "stage_index": 0}},
		"caveats":  []any{},
	}
	if max != nil {
		advice["max"] = max
	}
	return advice
}

// fixtureRecommendations is a whole recommendations.json around the advice.
func fixtureRecommendations(advice []map[string]any) map[string]any {
	return map[string]any{
		"analyzer_version": fixtureAnalyzer,
		"operating_point":  map[string]any{"mode": "slo_max", "stage_index": 0, "rps": 1},
		"derivation":       map[string]any{"nodes": []any{}},
		"policy": map[string]any{
			"cpu_limit": "follow", "memory_limit": "set", "qos": "burstable",
			"headroom": map[string]any{"request": 0.2, "limit": 0.3, "replicas": 0.2, "gpu": 0.1},
		},
		"recommendations": advice,
	}
}

func fixtureBundles() []fixtureBundle {
	bundles := []fixtureBundle{
		// The original-ladder runs: max_num_seqs 64, 128 and 256.
		{dir: "seqs64-probe", runID: "01M807ST8SRKPDH0318H1T4RYW", ladder: LadderProbe, knobs: sessionKnobs(64, 8192), offered: probeLadder, sloMax: 20.997,
			advice: []map[string]any{knobAdvice("V1", "vllm.max_num_seqs", 128, nil)}},
		{dir: "seqs128", runID: "01M808ZJD5N88S47PGSSC5WK78", ladder: LadderScreen, knobs: sessionKnobs(128, 8192), offered: screenLadder, sloMax: 37.922,
			advice: []map[string]any{knobAdvice("V1", "vllm.max_num_seqs", 256, 310)}},
		{dir: "seqs256", runID: "01M80A7FZ1CWEKRXX4ZTN1R4NB", ladder: LadderScreen, knobs: sessionKnobs(256, 8192), offered: screenLadder, sloMax: 39.397},
		// A frontend-bound leader: V7 fires. Not a recorded run.
		{dir: "seqs128-frontend", runID: "01M80F00000000000000000000", ladder: LadderScreen, knobs: sessionKnobs(128, 8192), offered: screenLadder, sloMax: 37.922,
			advice: []map[string]any{knobAdvice("V7", "vllm.api_server_count", nil, nil)}},
	}
	// The fine-ladder runs: three repeats each, on the confirmation ladder. The
	// recorded configurations give a mean and a 95% interval, not the three
	// values; the three here have that mean and standard deviation.
	repeats := []struct {
		seqs   float64
		ids    []string
		values []float64
	}{
		{256, []string{"01M80BG58Y5V7TTQD29W0DE1EW", "01M80BTS14CSE5M37WSZVJQNTS", "01M80C5CWM7892JPTPN9A91R3A"}, []float64{44.01, 44.02, 44.03}},
		{128, []string{"01M80CJ54CMCEWYJES6JV5F8K3", "01M80CWTTEFPEQFQCETGR8BR60", "01M80D7HNZK01Q6JQ8AVYHNKY6"}, []float64{41.61, 41.71, 41.81}},
	}
	for _, config := range repeats {
		for i, id := range config.ids {
			dir := "confirm-seqs" + formatValue(config.seqs) + "-r" + formatValue(float64(i+1))
			bundles = append(bundles, fixtureBundle{dir: dir, runID: id, ladder: LadderFull, knobs: sessionKnobs(config.seqs, 8192), offered: fullLadder, sloMax: config.values[i]})
		}
	}
	// A confirmation run whose ladder aborted before its last stage (the target
	// saturated): 8 stages, not 9. Not a recorded run.
	bundles = append(bundles, fixtureBundle{dir: "confirm-seqs128-aborted", runID: "01M80DZZZZ0000000000000000", ladder: LadderFull,
		knobs: sessionKnobs(128, 8192), offered: fullLadder[:len(fullLadder)-1], sloMax: 41.81})
	return bundles
}

func TestWriteFixtureBundles(t *testing.T) {
	if !*updateFixtures {
		t.Skip("pass -update to rewrite the fixture bundles")
	}
	base, err := os.ReadFile(minimalRunSpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtureBundles() {
		directory := filepath.Join(fixtureDir, "runs", fixture.dir)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		runSpec, err := spec.ParseRunSpec(base)
		if err != nil {
			t.Fatal(err)
		}
		runSpec.Knobs = spec.RunSpecKnobs(fixture.knobs)
		runSpec.Name = "propose-fixture"
		// The SLOs of the recorded experiment.
		ttft, itl, errorRate := spec.Duration("500ms"), spec.Duration("50ms"), 0.01
		runSpec.Slos = &spec.Slos{TtftP95: &ttft, ItlP95: &itl, MaxErrorRate: &errorRate}
		runSpec.Labels = spec.RunSpecLabels{LabelLadder: string(fixture.ladder), LabelConfig: fixture.knobs.id()}
		writeFixtureDocument(t, directory, bundle.ResolvedSpecJSON, spec.SchemaRunSpec, runSpec)
		writeFixtureDocument(t, directory, bundle.ManifestJSON, spec.SchemaManifest, fixtureManifest(fixture))
		writeFixtureDocument(t, directory, bundle.AnalysisJSON, spec.SchemaAnalysis, fixtureAnalysis(fixture))
		if fixture.advice != nil {
			writeFixtureDocument(t, directory, bundle.RecommendationsJSON, spec.SchemaRecommendations, fixtureRecommendations(fixture.advice))
		}
	}
}

func writeFixtureDocument(t *testing.T, directory, name, schema string, document any) {
	t.Helper()
	encoded, err := json.Marshal(document) // one line per file: the repo-wide duplication check counts lines
	if err != nil {
		t.Fatal(err)
	}
	if schema != "" {
		if err := spec.Validate(schema, encoded); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, name), append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureManifest(fixture fixtureBundle) spec.Manifest {
	finished := spec.UtcTimestamp(fixtureTimestamp)
	return spec.Manifest{BundleVersion: 1, RunId: fixture.runID, BenchVersion: fixtureBenchBuild, Status: spec.RunStatusSucceeded,
		StartedAt: spec.UtcTimestamp(fixtureTimestamp), FinishedAt: &finished, Files: []spec.ManifestFile{}}
}

func fixtureAnalysis(fixture fixtureBundle) spec.AnalysisResult {
	const gpus = 1 // one RTX 3070
	analysis := spec.AnalysisResult{AnalyzerVersion: fixtureAnalyzer, Quality: spec.RunQuality{Grade: spec.QualityGradeGood}}
	failed := -1
	for i, rps := range fixture.offered {
		if failed < 0 && rps > fixture.sloMax {
			failed = i
		}
		analysis.Stages = append(analysis.Stages, spec.StageAggregate{StageIndex: i, OfferedRps: rps, AchievedRps: rps, Requests: 1000})
		analysis.Quality.Stages = append(analysis.Quality.Stages, spec.StageQuality{StageIndex: i, IsValid: true})
	}
	gpuCount := gpus
	analysis.Capacity.PerReplica = &spec.PerReplica{ReadyReplicas: 1, RpsPerReplica: fixture.sloMax, TotalGpus: &gpuCount}
	if failed < 0 {
		analysis.Capacity.SloMax = &spec.SloMaxRps{Rps: fixture.sloMax, ValidStageIndex: len(fixture.offered) - 1}
		return analysis
	}
	analysis.Capacity.SloMax = &spec.SloMaxRps{Rps: fixture.sloMax, ValidStageIndex: failed - 1, FailedStageIndex: &failed, IsInterpolated: true}
	return analysis
}
