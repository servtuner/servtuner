// Package sizing recommends per-container resources at the operating point.
// Every number is a node of a derivation graph
// (package derive), so the recommendation carries its own calculation.
package sizing

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// OperatingPoint picks the stage the resources are sized at. By
// default it is the SLO-max stage: the most one replica served while meeting
// the SLOs. With target_rps it is the lowest usable stage at or above the
// target, never extrapolated past the highest usable stage; a target beyond
// that falls back to the SLO-max stage and says why.
func OperatingPoint(analysis spec.AnalysisResult, targetRPS *float64) spec.OperatingPoint {
	usable := usableStages(analysis)
	if targetRPS != nil {
		for _, index := range usable {
			if rps := analysis.Stages[index].AchievedRps; rps >= *targetRPS {
				stageIndex, target := analysis.Stages[index].StageIndex, *targetRPS
				return spec.OperatingPoint{Mode: spec.OperatingPointModeTargetRps, StageIndex: &stageIndex, Rps: &rps, TargetRps: &target}
			}
		}
	}
	sloMax := analysis.Capacity.SloMax
	if sloMax == nil {
		reason := "no stage is valid and meets the SLOs, so there is no load to size for"
		return spec.OperatingPoint{Mode: spec.OperatingPointModeUnavailable, Reason: &reason}
	}
	stageIndex := sloMax.ValidStageIndex
	rps := analysis.Stages[positionOf(analysis, stageIndex)].AchievedRps
	point := spec.OperatingPoint{Mode: spec.OperatingPointModeSloMax, StageIndex: &stageIndex, Rps: &rps}
	if targetRPS != nil {
		reason := fmt.Sprintf("target_rps %g is above the highest usable stage; sizing never extrapolates, so it uses the SLO-max stage", *targetRPS)
		point.Reason = &reason
	}
	return point
}

// usableStages are the positions of stages that are valid, not excluded and
// meet every SLO, in stage order.
func usableStages(analysis spec.AnalysisResult) []int {
	var usable []int
	for i, stage := range analysis.Stages {
		quality := analysis.Quality.Stages[i]
		if !quality.IsValid || quality.IsExcluded || !meetsSLOs(stage) {
			continue
		}
		usable = append(usable, i)
	}
	return usable
}

func meetsSLOs(stage spec.StageAggregate) bool {
	for _, result := range stage.SloResults {
		if !result.IsMet {
			return false
		}
	}
	return true
}

// positionOf finds a stage's position by its index; stages are written in
// order, so this is usually the identity.
func positionOf(analysis spec.AnalysisResult, stageIndex int) int {
	for i, stage := range analysis.Stages {
		if stage.StageIndex == stageIndex {
			return i
		}
	}
	return stageIndex
}
