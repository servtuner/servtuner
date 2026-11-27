package analyze

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/queueing"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Derivation nodes the burst check reads (bench/internal/analyze/queueing).
const (
	nodeRecommendedConcurrency = "queue.C"
	nodeRecommendedQueue       = queueing.NodeQueue
)

// checkBursts compares each burst stage's peak in flight per replica with the
// recommended C + B: the queue sized
// from a steady stage must also hold a short step above it. The verdict is
// evidence on the queue recommendation, urgent when the burst did not fit.
func checkBursts(run input.Run, analysis spec.AnalysisResult, stages []input.Stage, outputs *sizing.Outputs) error {
	burst := burstStages(run.Spec.Load)
	if len(burst) == 0 || len(outputs.Recommendations.Derivation.Nodes) == 0 {
		return nil
	}
	concurrency, hasC := derivationValue(outputs.Recommendations, nodeRecommendedConcurrency)
	queue, hasB := derivationValue(outputs.Recommendations, nodeRecommendedQueue)
	if !hasC || !hasB {
		return nil
	}
	recommendation := queueRecommendation(outputs.Recommendations.Recommendations)
	if recommendation == nil {
		return nil
	}
	replicas := 1
	if per := analysis.Capacity.PerReplica; per != nil && per.ReadyReplicas > 0 {
		replicas = per.ReadyReplicas
	}
	slot := queueing.WholeRequest
	if run.Spec.Target.Type == spec.TargetTypeBifrost && run.Spec.Workload.Streaming {
		slot = queueing.UntilFirstToken
	}
	capacity := concurrency + queue
	for _, stage := range stages {
		if !burst[stage.Index] {
			continue
		}
		rows, err := rowsOfStage(run.Records, stage.Index)
		if err != nil {
			return err
		}
		peak, ok := queueing.PeakInFlight(stage, rows, slot)
		if !ok {
			continue
		}
		perReplica := peak / float64(replicas)
		index := stage.Index
		rate := analysis.Stages[stage.Index].OfferedRps
		text := fmt.Sprintf("Burst stage %d at %s RPS peaked at %s in flight per replica, within C + B = %s",
			stage.Index, derive.FormatNumber(rate), derive.FormatNumber(perReplica), derive.FormatNumber(capacity))
		if perReplica > capacity {
			urgent := true
			recommendation.IsUrgent = &urgent
			text = fmt.Sprintf("Burst stage %d at %s RPS peaked at %s in flight per replica, above C + B = %s: with these settings requests would be dropped; raise B, or set analysis.burst_factor to the burst's ratio",
				stage.Index, derive.FormatNumber(rate), derive.FormatNumber(perReplica), derive.FormatNumber(capacity))
		}
		recommendation.Evidence = append(recommendation.Evidence, spec.RecommendationEvidence{Text: text, StageIndex: &index})
	}
	return nil
}

func derivationValue(recommendations spec.Recommendations, id string) (float64, bool) {
	for _, node := range recommendations.Derivation.Nodes {
		if node.Id == id {
			return node.Value, true
		}
	}
	return 0, false
}

// queueRecommendation is the recommendation of B, under the queueing model's
// rule name or the Bifrost advisor's (B2).
func queueRecommendation(recommendations []spec.Recommendation) *spec.Recommendation {
	for i := range recommendations {
		node := recommendations[i].DerivationNode
		if node != nil && *node == nodeRecommendedQueue {
			return &recommendations[i]
		}
	}
	return nil
}
