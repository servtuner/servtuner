package compare

import (
	"math"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func costedRun(usdPerHour *float64, isPriced bool) Run {
	tokens := 2000.0
	return Run{
		Analysis: spec.AnalysisResult{
			Capacity: spec.CapacityModel{
				SloMax:     &spec.SloMaxRps{Rps: 40, ValidStageIndex: 1},
				PerReplica: &spec.PerReplica{ReadyReplicas: 2},
			},
			Stages: []spec.StageAggregate{{StageIndex: 1, OutputTokensPerSecond: &tokens}},
		},
		Model: &spec.RunCapacityModel{InfraCost: &spec.InfraCost{Source: spec.InfraCostSourceStatic, IsPriced: isPriced, UsdPerReplicaHour: usdPerHour}},
	}
}

func TestCostObjectivesFromTheReplicasHourlyPrice(t *testing.T) {
	price := 0.72
	run := costedRun(&price, true)
	// 40 RPS over 2 replicas is 20 RPS each: 72000 requests an hour per replica.
	if got, reason := infraCostPer1KRequests(run); reason != "" || math.Abs(got-0.01) > 1e-12 {
		t.Errorf("per 1k requests %v (%s), want 0.01", got, reason)
	}
	// 2000 tokens/s over 2 replicas: 3.6M tokens an hour per replica.
	if got, reason := infraCostPer1MOutputTokens(run); reason != "" || math.Abs(got-0.2) > 1e-12 {
		t.Errorf("per 1M tokens %v (%s), want 0.2", got, reason)
	}
	if direction(spec.ObjectiveNameInfraCostPer1KRequests) != spec.ObjectiveDirectionMinimize {
		t.Error("cheaper is better")
	}
}

func TestUnpricedRunsHaveNoCostNeverZero(t *testing.T) {
	reason := `no price for node type "x"`
	run := costedRun(nil, false)
	run.Model.InfraCost.Reason = &reason
	if _, why := infraCostPer1KRequests(run); !strings.Contains(why, "unpriced: no price for node type") {
		t.Errorf("reason %q", why)
	}
	run.Model.InfraCost = nil
	if _, why := infraCostPer1MOutputTokens(run); !strings.Contains(why, "no infrastructure prices") {
		t.Errorf("reason %q", why)
	}
}

func TestACensoredRunsCostIsAnUpperBoundAndCannotBeBeaten(t *testing.T) {
	cheap, dear := 0.5, 0.9
	measured := costedRun(&cheap, true)
	failed := 2
	measured.Analysis.Capacity.SloMax.FailedStageIndex = &failed
	censored := costedRun(&dear, true) // never failed: its SLO-max is a lower bound
	runs := []Run{measured, censored}
	upper := acrossRepeats(runs[1:], infraCostPer1KRequests, spec.ObjectiveDirectionMinimize, true)
	if !upper.IsUpperBound || upper.IsLowerBound {
		t.Fatalf("interval %+v", upper)
	}
	lower := acrossRepeats(runs[1:], perReplica, spec.ObjectiveDirectionMaximize, true)
	if !lower.IsLowerBound || lower.IsUpperBound {
		t.Fatalf("interval %+v", lower)
	}
	best := ranked{id: "c1", interval: acrossRepeats(runs[:1], infraCostPer1KRequests, spec.ObjectiveDirectionMinimize, true)}
	ok, reason := significant(best, ranked{id: "c2", interval: upper}, spec.ObjectiveDirectionMinimize)
	if ok || !strings.Contains(reason, "an upper bound and may be below c1's") {
		t.Errorf("significant=%v %q", ok, reason)
	}
}

func TestTokenCostIsPerRequestSoCensoringDoesNotBoundIt(t *testing.T) {
	perK, perM := 0.4, 4.0
	run := costedRun(nil, false) // never failed its SLOs
	run.Model.TokenCost = &spec.TokenCost{IsPriced: true, Model: "openai/gpt-4o-mini", UsdPer1KRequests: &perK, UsdPer1MOutputTokens: &perM}
	interval := acrossRepeats([]Run{run}, tokenCostPer1KRequests, spec.ObjectiveDirectionMinimize, !perRequest[spec.ObjectiveNameCostPer1KRequests])
	if interval.IsUpperBound || interval.IsLowerBound || *interval.Value != 0.4 {
		t.Errorf("interval %+v", interval)
	}
	run.Model.TokenCost = &spec.TokenCost{Model: "servtuner-mock/mock-model"}
	if _, reason := tokenCostPer1MOutputTokens(run); !strings.Contains(reason, "unpriced") {
		t.Errorf("reason %q", reason)
	}
}
