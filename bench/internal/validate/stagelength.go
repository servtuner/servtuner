package validate

import (
	"fmt"
	"math"
	"time"

	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Stage-length rules. They use the effective
// scrape interval I, the slowest relevant one among target, cadvisor and DCGM.
// With no Prometheus to ask, I is prometheus.AssumedInterval (the 5s of the
// servtuner-targets job) and the warning says so.
//
//	stage  >= max(10 x I, 60s)               min_stage_duration
//	warmup >= max(2 x I, 30s)                warmup_duration
//	stage  >= N / rate, N by percentile      percentile_sample_count (Rule A)
//
// Rule B (the stage is at least 10 x the mean E2E) needs the E2E that only the
// warmup measures, so the pipeline evaluates it before stage 1 starts.
const (
	minimumStageFloor      = 60 * time.Second
	minimumSamplesPerStage = 10
	minimumWarmupFloor     = 30 * time.Second
	warmupIntervals        = 2
	// Rule A: a tail percentile needs about 10 observations beyond it,
	// and at least 100 requests whatever the percentile.
	observationsBeyondTail = 10
	minimumPercentileCount = 100
)

// MinimumStageDuration is the shortest stage that gives every series at least
// minimumSamplesPerStage samples at the given scrape interval.
func MinimumStageDuration(scrapeInterval time.Duration) time.Duration {
	return max(minimumSamplesPerStage*scrapeInterval, minimumStageFloor)
}

// MinimumWarmupDuration is the shortest warmup that leaves the first stage with
// established series and counters.
func MinimumWarmupDuration(scrapeInterval time.Duration) time.Duration {
	return max(warmupIntervals*scrapeInterval, minimumWarmupFloor)
}

// percentileFractions are the reported percentiles as fractions.
var percentileFractions = map[spec.PercentileName]float64{
	spec.PercentileNameP50: 0.50,
	spec.PercentileNameP90: 0.90,
	spec.PercentileNameP95: 0.95,
	spec.PercentileNameP99: 0.99,
}

// RequestsForPercentile is Rule A: how many requests a stage needs for the
// percentile to be stable (p50 100, p95 200, p99 1000).
func RequestsForPercentile(percentile spec.PercentileName) int {
	return max(minimumPercentileCount, int(math.Round(observationsBeyondTail/(1-percentileFractions[percentile]))))
}

// reportedPercentile is the percentile the run's SLOs are stated in, which is
// the one Rule A checks. Every SLO the RunSpec offers is a p95, so this is p95
// when any SLO is set and none otherwise.
func reportedPercentile(runSpec spec.RunSpec) (spec.PercentileName, bool) {
	slos := runSpec.Slos
	if slos == nil || (slos.E2EP95 == nil && slos.ItlP95 == nil && slos.TtftP95 == nil) {
		return "", false
	}
	return spec.PercentileNameP95, true
}

// stageLengthProblems checks the warmup and every stage against the stage-length
// rules. A short stage is a warning, not an error: the
// run is still well-formed, only its resolution is limited, and the bundle must
// say so instead of refusing the run. The error list is for malformed
// durations, which the schema pattern lets through.
func stageLengthProblems(runSpec spec.RunSpec, scrapeInterval prometheus.ScrapeInterval) (warnings []spec.StageLengthWarning, errs []string) {
	if warning, isShort := warmupWarning(runSpec.Load.Warmup, scrapeInterval); isShort {
		warnings = append(warnings, warning)
	}
	percentile, hasPercentile := reportedPercentile(runSpec)
	measured := 0
	for _, stage := range runSpec.Load.Stages {
		if !stage.Burst() {
			measured++
		}
	}
	if len(runSpec.Load.Stages) > 0 && measured == 0 {
		errs = append(errs, "load.stages: every stage is_burst, so nothing measures capacity; a burst probe needs ordinary stages to size the queue it checks")
	}
	for index, stage := range runSpec.Load.Stages {
		duration, err := stage.Duration.Std()
		if err != nil {
			errs = append(errs, fmt.Sprintf("load.stages[%d].duration: %v", index, err))
			continue
		}
		if duration <= 0 {
			errs = append(errs, fmt.Sprintf("load.stages[%d].duration: must be positive, got %s", index, stage.Duration))
			continue
		}
		if stage.Burst() {
			continue // a probe: its in-flight peak comes from client records, not scrapes
		}
		if warning, isShort := minimumStageWarning(index, duration, scrapeInterval); isShort {
			warnings = append(warnings, warning)
		}
		if hasPercentile {
			if warning, hasTooFew := percentileCountWarning(runSpec.Load, index, duration, percentile, scrapeInterval); hasTooFew {
				warnings = append(warnings, warning)
			}
		}
	}
	return warnings, errs
}

// intervalNote says where the interval of a message came from.
func intervalNote(scrapeInterval prometheus.ScrapeInterval) string {
	if scrapeInterval.Source == spec.ScrapeIntervalSourceAssumed {
		return "the interval is assumed, not read from Prometheus"
	}
	return fmt.Sprintf("the interval is read from Prometheus job %s", scrapeInterval.Job)
}

// newWarning fills the fields every warning shares.
func newWarning(rule spec.StageLengthRule, required, actual time.Duration, scrapeInterval prometheus.ScrapeInterval, message string) spec.StageLengthWarning {
	warning := spec.StageLengthWarning{
		Rule:                    rule,
		RequiredDurationSeconds: required.Seconds(),
		ActualDurationSeconds:   actual.Seconds(),
		ScrapeIntervalSeconds:   scrapeInterval.Interval.Seconds(),
		ScrapeIntervalSource:    scrapeInterval.Source,
		Message:                 message,
	}
	if scrapeInterval.Job != "" {
		job := scrapeInterval.Job
		warning.Job = &job
	}
	return warning
}

func minimumStageWarning(index int, duration time.Duration, scrapeInterval prometheus.ScrapeInterval) (spec.StageLengthWarning, bool) {
	minimum := MinimumStageDuration(scrapeInterval.Interval)
	if duration >= minimum {
		return spec.StageLengthWarning{}, false
	}
	warning := newWarning(spec.StageLengthRuleMinStageDuration, minimum, duration, scrapeInterval, fmt.Sprintf(
		"load.stages[%d].duration %s is shorter than the minimum %s (max(%d x %s scrape interval, %s); %s)",
		index, duration, minimum, minimumSamplesPerStage, scrapeInterval.Interval, minimumStageFloor, intervalNote(scrapeInterval)))
	warning.StageIndex = &index
	return warning, true
}

// warmupWarning checks the warmup. The schema defaults the warmup to 30s, so it
// is only absent when it was set to nothing, which is not checked.
func warmupWarning(warmup spec.Duration, scrapeInterval prometheus.ScrapeInterval) (spec.StageLengthWarning, bool) {
	duration, err := warmup.Std()
	if err != nil || duration <= 0 {
		return spec.StageLengthWarning{}, false
	}
	minimum := MinimumWarmupDuration(scrapeInterval.Interval)
	if duration >= minimum {
		return spec.StageLengthWarning{}, false
	}
	return newWarning(spec.StageLengthRuleWarmupDuration, minimum, duration, scrapeInterval, fmt.Sprintf(
		"load.warmup %s is shorter than the minimum %s (max(%d x %s scrape interval, %s); %s)",
		duration, minimum, warmupIntervals, scrapeInterval.Interval, minimumWarmupFloor, intervalNote(scrapeInterval))), true
}

// percentileCountWarning is Rule A: a stage that offers too few requests cannot
// support the reported percentile. The stage is as long as its mean offered rate
// needs to reach the count.
func percentileCountWarning(load spec.Load, index int, duration time.Duration, percentile spec.PercentileName, scrapeInterval prometheus.ScrapeInterval) (spec.StageLengthWarning, bool) {
	meanRate := meanOfferedRate(load, index)
	if meanRate <= 0 {
		return spec.StageLengthWarning{}, false
	}
	required := RequestsForPercentile(percentile)
	offered := meanRate * duration.Seconds()
	if offered >= float64(required) {
		return spec.StageLengthWarning{}, false
	}
	requiredDuration := time.Duration(math.Ceil(float64(required)/meanRate)) * time.Second
	warning := newWarning(spec.StageLengthRulePercentileSampleCount, requiredDuration, duration, scrapeInterval, fmt.Sprintf(
		"load.stages[%d] offers about %.0f requests (%.4g requests/s for %s), fewer than the %d a stable %s needs; it needs at least %s",
		index, offered, meanRate, duration, required, percentile, requiredDuration))
	warning.StageIndex = &index
	warning.Percentile = &percentile
	warning.RequiredRequests = &required
	return warning, true
}

// meanOfferedRate is the average rate of a stage. With linear pacing the rate
// ramps from the previous stage's rate to this one's, and stage 0 holds its
// rate; every other pacing averages to the stage rate.
func meanOfferedRate(load spec.Load, index int) float64 {
	rate := load.Stages[index].Rate
	if load.Pacing == spec.PacingLinear && index > 0 {
		return (load.Stages[index-1].Rate + rate) / 2
	}
	return rate
}

// missingRoleWarnings names the relevant roles that have no scrape job. The
// target role always matters. cAdvisor matters when the target runs in
// Kubernetes (it has a workload), because CPU and memory sizing comes from it.
func missingRoleWarnings(runSpec spec.RunSpec, missing []spec.JobRole) []spec.ScrapeWarning {
	var warnings []spec.ScrapeWarning
	for _, role := range missing {
		switch {
		case role == spec.JobRoleTarget:
			warnings = append(warnings, missingRole(role, fmt.Sprintf(
				"prometheus has no %s job for the targets under test, so their server-side series may be scraped slower than the reported interval or not at all", prometheus.TargetJob)))
		case role == spec.JobRoleCadvisor && runSpec.Target.Workload != nil:
			warnings = append(warnings, missingRole(role,
				"prometheus has no cAdvisor job, so container CPU and memory series may be missing or scraped slower than the reported interval"))
		}
	}
	return warnings
}

func missingRole(role spec.JobRole, message string) spec.ScrapeWarning {
	return spec.ScrapeWarning{Rule: spec.ScrapeWarningRuleMissingScrapeRole, Role: role, Message: message}
}
