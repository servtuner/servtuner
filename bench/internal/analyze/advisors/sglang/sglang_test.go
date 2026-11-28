package sglang

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func server() spec.RecommendationTarget {
	return spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeSglang}
}

// calm is a healthy server: no queue, KV pool half full, no retraction,
// prefixes reused.
func calm() Input {
	stages := []StageView{
		{Index: 0, RPS: 10, RunningPeak: 20, HasScheduler: true, TokenUsagePeak: 0.3, HasRetracted: true, PrefixReuse: 0.8, HasPrefixReuse: true},
		{Index: 1, RPS: 20, RunningPeak: 40, HasScheduler: true, TokenUsagePeak: 0.5, HasRetracted: true, PrefixReuse: 0.8, HasPrefixReuse: true},
	}
	return Input{
		Target: server(), Stages: stages, Operating: stages[1],
		Knobs:      Knobs{MemFractionStatic: 0.82, MaxRunningRequests: 64, SchedulePolicy: "fcfs", IsRadixCacheCaptured: true},
		Confidence: spec.RecommendationConfidenceMedium,
	}
}

func rules(recommendations []spec.Recommendation) []string {
	var names []string
	for _, recommendation := range recommendations {
		names = append(names, recommendation.Rule)
	}
	return names
}

func only(t *testing.T, got []spec.Recommendation, rule string) spec.Recommendation {
	t.Helper()
	if !slices.Equal(rules(got), []string{rule}) {
		t.Fatalf("rules = %v, want only %s", rules(got), rule)
	}
	return got[0]
}

func TestACalmServerGetsNoAdvice(t *testing.T) {
	in := calm()
	in.IsSharedPrefixWorkload = true
	if got := Advise(in); len(got) != 0 {
		t.Errorf("rules = %v", rules(got))
	}
}

func TestKVBoundNeedsAFullPoolAndAQueue(t *testing.T) {
	in := calm()
	in.Stages[1].TokenUsagePeak = 0.97
	if got := Advise(in); len(got) != 0 {
		t.Errorf("a full pool with no queue: rules = %v", rules(got))
	}
	in.Stages[1].QueuePeak = 12
	s1 := only(t, Advise(in), RuleKVBound)
	if !strings.Contains(s1.Recommended.(string), "--mem-fraction-static above 0.82") || *s1.Evidence[0].StageIndex != 1 {
		t.Errorf("S1 = %v %v", s1.Recommended, s1.Evidence)
	}
}

func TestRetractionAtTheOperatingPoint(t *testing.T) {
	in := calm()
	in.Operating.Retracted = 7
	s2 := only(t, Advise(in), RuleRetraction)
	if !strings.Contains(s2.Evidence[0].Text, "7 requests were retracted") {
		t.Errorf("S2 evidence = %v", s2.Evidence)
	}
}

func TestRunningCapNeedsTheCapturedCapAndRoomInThePool(t *testing.T) {
	in := calm()
	in.Stages[1].RunningPeak, in.Stages[1].QueuePeak = 64, 30
	s3 := only(t, Advise(in), RuleRunningCap)
	if !strings.Contains(s3.Recommended.(string), "--max-running-requests above 64") {
		t.Errorf("S3 = %v", s3.Recommended)
	}
	in.Stages[1].TokenUsagePeak = 0.9 // the pool is the limit too: not the cap alone
	if slices.Contains(rules(Advise(in)), RuleRunningCap) {
		t.Error("S3 fired with the KV pool near full")
	}
	in = calm()
	in.Knobs.MaxRunningRequests = 0
	in.Stages[1].RunningPeak, in.Stages[1].QueuePeak = 64, 30
	if slices.Contains(rules(Advise(in)), RuleRunningCap) {
		t.Error("S3 fired without a captured cap")
	}
}

func TestPrefixReuseSuggestsLPMOnSharedPrefixWorkloads(t *testing.T) {
	in := calm()
	in.Operating.PrefixReuse = 0.2
	if got := Advise(in); len(got) != 0 {
		t.Errorf("not a shared-prefix workload: rules = %v", rules(got))
	}
	in.IsSharedPrefixWorkload = true
	s4 := only(t, Advise(in), RulePrefixReuse)
	if !strings.Contains(s4.Recommended.(string), "--schedule-policy lpm (now fcfs)") {
		t.Errorf("S4 = %v", s4.Recommended)
	}
	in.Knobs.SchedulePolicy = "lpm"
	if got := Advise(in); len(got) != 0 {
		t.Errorf("already lpm: rules = %v", rules(got))
	}
	in.Knobs.SchedulePolicy = ""
	s4 = only(t, Advise(in), RulePrefixReuse)
	if len(s4.Caveats) != 1 || !strings.Contains(s4.Caveats[0], "default (fcfs) is assumed") {
		t.Errorf("caveats = %v", s4.Caveats)
	}
}

func TestDisabledRadixCacheOnASharedPrefixWorkload(t *testing.T) {
	in := calm()
	in.IsSharedPrefixWorkload = true
	in.Knobs.IsRadixCacheDisabled = true
	s4 := only(t, Advise(in), RulePrefixReuse)
	if !strings.Contains(s4.Recommended.(string), "Drop --disable-radix-cache") || s4.Evidence[0].Series != nil {
		t.Errorf("S4 = %v %v", s4.Recommended, s4.Evidence)
	}
}

func TestViewStageAggregatesRanksAndGroups(t *testing.T) {
	start := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	series := func(labels map[string]string, values ...float64) input.Series {
		one := input.Series{Labels: labels}
		for i, value := range values {
			one.Samples = append(one.Samples, input.Sample{At: at(5 * i), Value: value})
		}
		return one
	}
	rank := func(pod, dp, tp string) map[string]string {
		return map[string]string{"pod": pod, "dp_rank": dp, "tp_rank": tp}
	}
	matrix := input.MapMatrix{
		// Two TP ranks of one group report the same batch: counted once. A second DP group adds.
		runningReqs: {series(rank("a", "0", "0"), 3, 8, 5), series(rank("a", "0", "1"), 3, 8, 5), series(rank("a", "1", "2"), 2, 4, 6)},
		queueReqs:   {series(rank("a", "0", "0"), 0, 2, 1), series(rank("other", "0", "0"), 50, 50, 50)},
		tokenUsage:  {series(rank("a", "0", "0"), 0.4, 0.7, 0.6), series(rank("a", "1", "2"), 0.5, 0.9, 0.8)},
		// The counter every TP rank exports: one group's increase.
		retractedTotal: {series(rank("a", "0", "0"), 0, 2, 4), series(rank("a", "0", "1"), 0, 2, 4)},
		promptTokens:   {series(map[string]string{"pod": "a", "is_streaming": "true"}, 0, 500, 1000)},
		cachedTokens:   {series(map[string]string{"pod": "a", "cache_source": "device"}, 0, 200, 400), series(map[string]string{"pod": "a", "cache_source": "host"}, 0, 50, 100)},
	}
	stage := input.Stage{Index: 2, Start: at(0), End: at(15)}
	view, err := ViewStage(matrix, map[string]bool{"a": true}, stage, spec.StageAggregate{AchievedRps: 9})
	if err != nil {
		t.Fatal(err)
	}
	if view.RunningPeak != 14 || view.QueuePeak != 2 || view.TokenUsagePeak != 0.9 || !view.HasScheduler {
		t.Errorf("scheduler view = %+v", view)
	}
	if !view.HasRetracted || view.Retracted != 4 {
		t.Errorf("retracted = %v %v", view.Retracted, view.HasRetracted)
	}
	if !view.HasPrefixReuse || view.PrefixReuse != 0.5 {
		t.Errorf("prefix reuse = %v %v", view.PrefixReuse, view.HasPrefixReuse)
	}
}
