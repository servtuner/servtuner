package capacity

import "github.com/servtuner/servtuner/bench/internal/spec"

// sloMax is the highest usable stage where every SLO holds, interpolated
// linearly toward the first usable stage above it that fails. The ladder is read upward from its first usable stage and stops at
// the first stage that fails: a later stage that passes again is noise, not
// capacity, and claiming it would report an RPS that a lower, failed stage
// contradicts. Stages at the bottom of the ladder that fail only because a
// percentile lacks the samples Rule A asks for are skipped, not failed: a short
// stage at 1 RPS says nothing about capacity, and it must not end the scan
// before a stage could pass. Once a stage has passed, too few samples fails
// as before. It is nil when no SLO is configured or the first judged stage
// already fails.
func sloMax(usable []Stage) *spec.SloMaxRps {
	var passing, failed *Stage
	for i := range usable {
		if passing == nil && !usable[i].IsTargetSaturated && onlyUnderpowered(usable[i]) {
			continue
		}
		if usable[i].IsTargetSaturated || !holds(usable[i]) {
			failed = &usable[i]
			break
		}
		passing = &usable[i]
	}
	if passing == nil {
		return nil
	}
	result := &spec.SloMaxRps{Rps: passing.Aggregate.AchievedRps, ValidStageIndex: passing.Window.Index}
	if failed == nil {
		return result
	}
	failedIndex := failed.Window.Index
	result.FailedStageIndex = &failedIndex
	limiting, fraction, canInterpolate := crossing(*passing, *failed)
	if limiting != "" {
		result.LimitingSlo = &limiting
	}
	if canInterpolate && failed.Aggregate.AchievedRps > passing.Aggregate.AchievedRps {
		result.Rps = passing.Aggregate.AchievedRps + fraction*(failed.Aggregate.AchievedRps-passing.Aggregate.AchievedRps)
		result.IsInterpolated = true
	}
	return result
}

// holds says whether every SLO is met in the stage. A stage with no SLO result
// holds nothing: without an SLO there is no SLO-max.
func holds(stage Stage) bool {
	if len(stage.Aggregate.SloResults) == 0 {
		return false
	}
	for _, result := range stage.Aggregate.SloResults {
		if !result.IsMet {
			return false
		}
	}
	return true
}

// onlyUnderpowered says whether every SLO the stage misses lacks an observed
// value (too few samples), so the stage could not be judged at all.
func onlyUnderpowered(stage Stage) bool {
	missed := false
	for _, result := range stage.Aggregate.SloResults {
		if result.IsMet {
			continue
		}
		if result.Observed != nil {
			return false
		}
		missed = true
	}
	return missed
}

// crossing finds the SLO that the failed stage breaks first on the way up from
// the passing one, and how far along the line from the passing stage to the
// failed one it crosses its threshold. canInterpolate is false when any
// failing SLO has no observed value at either end (too few samples), or did not
// get worse, because the crossing of that SLO cannot be placed and could lie
// earlier than the others; the limiting SLO is then the first that fails.
func crossing(passing, failed Stage) (limiting spec.SloName, fraction float64, canInterpolate bool) {
	passingObserved := map[spec.SloName]float64{}
	for _, result := range passing.Aggregate.SloResults {
		if result.Observed != nil {
			passingObserved[result.Slo] = *result.Observed
		}
	}
	canInterpolate = true
	earliest := 2.0
	var earliestSlo spec.SloName
	for _, result := range failed.Aggregate.SloResults {
		if result.IsMet {
			continue
		}
		if limiting == "" {
			limiting = result.Slo
		}
		before, hasBefore := passingObserved[result.Slo]
		if result.Observed == nil || !hasBefore || *result.Observed <= before {
			canInterpolate = false
			continue
		}
		at := min(max((result.Threshold-before)/(*result.Observed-before), 0), 1)
		if at < earliest {
			earliest, earliestSlo = at, result.Slo
		}
	}
	if canInterpolate && earliestSlo != "" {
		return earliestSlo, earliest, true
	}
	return limiting, 0, false
}
