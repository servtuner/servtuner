package plan

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// withMemory gives the model a router memory line: WS_0 0.4 GiB, m at the
// given request size, B = 10 and h_lim 0.3, under a 1 GiB limit.
func withMemory(model spec.RunCapacityModel, runID string, payload, perFlight float64) spec.RunCapacityModel {
	queue, headroom, limit := 10, 0.3, float64(1<<30)
	model.RunId = runID
	model.Memory = &spec.ModelMemory{BaselineBytes: 0.4 * (1 << 30), BytesPerInFlight: perFlight, Stages: 3, RequestBytesMean: &payload, QueueSize: &queue, LimitHeadroom: &headroom}
	perReplica := *model.PerReplica
	perReplica.MemoryLimitBytes = &limit
	model.PerReplica = &perReplica
	return model
}

func planWithSeries(t *testing.T, raw string, tier spec.RunCapacityModel, series ...spec.RunCapacityModel) spec.Plan {
	t.Helper()
	result, err := Make(request(t, raw), []spec.RunCapacityModel{tier}, series, "test")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if err := spec.Validate(spec.SchemaPlan, encoded); err != nil {
		t.Fatalf("plan.json is invalid: %v", err)
	}
	return result
}

func workingSetAt(tier spec.PlanTier, perFlight float64) float64 {
	return (0.4*(1<<30) + perFlight*float64(tier.Queue.Concurrency+10)) * 1.3
}

func checksOf(result spec.Plan, status spec.PlanCheckStatus) string {
	var texts []string
	for _, check := range result.Checks {
		if check.Status == status {
			texts = append(texts, check.Text)
		}
	}
	return strings.Join(texts, " | ")
}

const plain = `{"peak_rps": 400, "slos": {"ttft_p95": "500ms"}}`

// tenMegabytes is a worked hypothetical: 8 KB measured, then
// "what if messages were 10 MB?".
const tenMegabytes = `{"peak_rps": 400, "slos": {"ttft_p95": "500ms"}, "overrides": [{"node": "bifrost.payload_bytes", "kind": "hypothetical", "value": 10485760}]}`

func TestTheMeasuredSizeNeedsNoAssumption(t *testing.T) {
	result := planWithSeries(t, plain, withMemory(model(), "run-1", 8000, 40000))
	memory := result.Tiers[0].Memory
	if memory == nil || memory.Basis != spec.PlanTierMemoryBasisMeasured || memory.BytesPerInFlight != 40000 || memory.BytesPerInFlightLow != nil {
		t.Fatalf("memory = %+v", memory)
	}
	if math.Abs(memory.WorkingSetBytes-workingSetAt(result.Tiers[0], 40000)) > 1 {
		t.Errorf("working set = %v", memory.WorkingSetBytes)
	}
	if !strings.Contains(checksOf(result, spec.PlanCheckStatusOk), "fits the") {
		t.Errorf("checks = %+v", result.Checks)
	}
}

func TestOneMeasuredSizeGivesTheAssumedRange(t *testing.T) {
	result := planWithSeries(t, tenMegabytes, withMemory(model(), "run-1", 8000, 40000))
	tier := result.Tiers[0]
	memory := tier.Memory
	if memory == nil || memory.Basis != spec.PlanTierMemoryBasisAssumed || memory.BytesPerInFlightLow == nil {
		t.Fatalf("memory = %+v", memory)
	}
	// m(s) = 40 KB + k × (10 MB − 8 KB): about 10 MB at k = 1 and 30 MB at k = 3.
	if low, high := *memory.BytesPerInFlightLow, memory.BytesPerInFlight; low != 40000+10485760-8000 || high != 40000+3*(10485760-8000) {
		t.Errorf("m = %v to %v", low, high)
	}
	if math.Abs(*memory.WorkingSetBytesLow-workingSetAt(tier, *memory.BytesPerInFlightLow)) > 1 {
		t.Errorf("working set low = %v", *memory.WorkingSetBytesLow)
	}
	if !strings.Contains(checksOf(result, spec.PlanCheckStatusWarning), "assumes k between 1× and 3×") {
		t.Errorf("warnings = %s", checksOf(result, spec.PlanCheckStatusWarning))
	}
	// Even k = 1 cannot fit 10 MB requests at C + B in 1 GiB.
	if !strings.Contains(checksOf(result, spec.PlanCheckStatusViolatesMeasured), "past the 1073741824-byte memory limit") {
		t.Errorf("checks = %+v", result.Checks)
	}
	if tier.Confidence != spec.PlanTierConfidenceLow {
		t.Errorf("confidence = %s for a hypothetical payload", tier.Confidence)
	}
}

// series measures m = 16 KB + 3 × s at 8 KB, 64 KB and 1 MB requests.
func series() (spec.RunCapacityModel, []spec.RunCapacityModel) {
	line := func(s float64) float64 { return 16000 + 3*s }
	return withMemory(model(), "run-8k", 8000, line(8000)),
		[]spec.RunCapacityModel{withMemory(model(), "run-64k", 64000, line(64000)), withMemory(model(), "run-1m", 1e6, line(1e6))}
}

func TestAPayloadSeriesFitsTheLine(t *testing.T) {
	tier, others := series()
	within := `{"peak_rps": 400, "slos": {"ttft_p95": "500ms"}, "overrides": [{"node": "bifrost.payload_bytes", "kind": "hypothetical", "value": 500000}]}`
	memory := planWithSeries(t, within, tier, others...).Tiers[0].Memory
	if memory == nil || memory.Basis != spec.PlanTierMemoryBasisFitted || math.Abs(memory.BytesPerInFlight-1516000) > 1e-6 {
		t.Fatalf("memory = %+v", memory)
	}
	if strings.Join(memory.Runs, ",") != "run-8k,run-64k,run-1m" {
		t.Errorf("runs = %v", memory.Runs)
	}
	result := planWithSeries(t, tenMegabytes, tier, others...)
	if memory := result.Tiers[0].Memory; memory.Basis != spec.PlanTierMemoryBasisExtrapolated || memory.BytesPerInFlightLow != nil {
		t.Errorf("10 MB: memory = %+v", memory)
	}
	if !strings.Contains(checksOf(result, spec.PlanCheckStatusWarning), "outside the 8000 to 1000000 bytes") {
		t.Errorf("warnings = %s", checksOf(result, spec.PlanCheckStatusWarning))
	}
}

func TestAPayloadSeriesMustMatchATier(t *testing.T) {
	tier, others := series()
	stranger := others[0]
	stranger.Identity.Knobs = spec.ModelIdentityKnobs{"bifrost.concurrency": 50.0}
	if _, err := Make(request(t, plain), []spec.RunCapacityModel{tier}, []spec.RunCapacityModel{stranger}, "test"); err == nil || !strings.Contains(err.Error(), "matches no tier") {
		t.Errorf("err = %v", err)
	}
	unsized := others[0]
	unsized.Memory = nil
	if _, err := Make(request(t, plain), []spec.RunCapacityModel{tier}, []spec.RunCapacityModel{unsized}, "test"); err == nil || !strings.Contains(err.Error(), "no memory line") {
		t.Errorf("err = %v", err)
	}
	// Another request mix is what a payload series is: it is not refused.
	other := others[1]
	other.Identity.WorkloadHash = strings.Repeat("b", 64)
	planWithSeries(t, plain, tier, other)
}

func TestWithoutRequestSizesTheRunsOwnMIsUsed(t *testing.T) {
	tier := withMemory(model(), "run-1", 8000, 40000)
	tier.Memory.RequestBytesMean = nil
	memory := planWithSeries(t, plain, tier).Tiers[0].Memory
	if memory == nil || memory.Basis != spec.PlanTierMemoryBasisMeasured || memory.PayloadBytes != nil || memory.BytesPerInFlight != 40000 {
		t.Errorf("memory = %+v", memory)
	}
}

// Real series are not straight lines: on kind, m grew ~470× from 0.8 KB to
// 525 KB requests, so a least-squares line through the points misses the
// small sizes badly. m(s) goes through every measured point instead.
func TestMOfSIsExactAtEveryMeasuredSize(t *testing.T) {
	sizes := []float64{1e3, 1e4, 1e5, 1e6}
	perFlight := []float64{4e5, 9e5, 6e6, 5.7e7}
	tier := withMemory(model(), "run-1", sizes[0], perFlight[0])
	var others []spec.RunCapacityModel
	for i := 1; i < len(sizes); i++ {
		others = append(others, withMemory(model(), "run-"+string(rune('1'+i)), sizes[i], perFlight[i]))
	}
	at := func(payload float64) spec.PlanTierMemory {
		raw := `{"peak_rps": 400, "slos": {"ttft_p95": "500ms"}, "overrides": [{"node": "bifrost.payload_bytes", "kind": "hypothetical", "value": ` +
			strconv.FormatFloat(payload, 'f', -1, 64) + `}]}`
		return *planWithSeries(t, raw, tier, others...).Tiers[0].Memory
	}
	for i, size := range sizes {
		if got := at(size).BytesPerInFlight; math.Abs(got-perFlight[i]) > 1e-6 {
			t.Errorf("m(%v) = %v, want the measured %v", size, got, perFlight[i])
		}
	}
	// Halfway between the second and third sizes, halfway between their m.
	if got := at(55000).BytesPerInFlight; math.Abs(got-(9e5+6e6)/2) > 1e-6 {
		t.Errorf("m(55000) = %v", got)
	}
	// Outside the range the end segments carry on, and m never goes below 0.
	if got, want := at(2e6), 5.7e7+(5.7e7-6e6)/(1e6-1e5)*1e6; math.Abs(got.BytesPerInFlight-want) > 1e-3 || got.Basis != spec.PlanTierMemoryBasisExtrapolated {
		t.Errorf("m(2e6) = %+v, want %v", got, want)
	}
	if got := at(1).BytesPerInFlight; got != 4e5-(9e5-4e5)/(1e4-1e3)*999 {
		t.Errorf("m(1) = %v", got)
	}
}
