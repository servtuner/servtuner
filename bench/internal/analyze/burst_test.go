package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/queueing"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// overlapping are n requests sent at once in a stage, each taking 2s.
// burstStage is the is_burst value of a probe stage.
var burstStage = true

func overlapping(stage int64, start time.Time, n int) []engine.RequestRow {
	rows := make([]engine.RequestRow, n)
	for i := range rows {
		index := stage
		rows[i] = engine.RequestRow{Phase: engine.RowPhaseStage, StageIndex: &index, SentAt: start.Add(time.Second), E2ESeconds: 2, Status: 200}
	}
	return rows
}

func burstOutputs(c, b float64) sizing.Outputs {
	var outputs sizing.Outputs
	node := queueing.NodeQueue
	outputs.Recommendations.Derivation.Nodes = []spec.DerivationNode{{Id: "queue.C", Value: c}, {Id: queueing.NodeQueue, Value: b}}
	outputs.Recommendations.Recommendations = []spec.Recommendation{{Id: "queueing.queue/target", Rule: "queueing.queue", DerivationNode: &node, Evidence: []spec.RecommendationEvidence{}}}
	return outputs
}

func burstRun(peak int) (input.Run, spec.AnalysisResult, []input.Stage) {
	start := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	stages := []input.Stage{
		{Index: 0, Start: start, End: start.Add(time.Minute)},
		{Index: 1, Start: start.Add(time.Minute), End: start.Add(90 * time.Second)},
	}
	run := input.Run{
		Spec: spec.RunSpec{Load: spec.Load{Stages: []spec.Stage{
			{Rate: 10, Duration: "60s"}, {Rate: 30, Duration: "30s", IsBurst: &burstStage},
		}}},
		Records: input.SliceRecords(overlapping(1, stages[1].Start, peak)),
	}
	analysis := spec.AnalysisResult{
		Capacity: spec.CapacityModel{PerReplica: &spec.PerReplica{ReadyReplicas: 2}},
		Stages:   []spec.StageAggregate{{StageIndex: 0, OfferedRps: 10}, {StageIndex: 1, OfferedRps: 30}},
	}
	return run, analysis, stages
}

func TestABurstThatFitsCPlusBIsSaidSo(t *testing.T) {
	run, analysis, stages := burstRun(20) // 10 per replica
	outputs := burstOutputs(8, 4)
	if err := checkBursts(run, analysis, stages, &outputs); err != nil {
		t.Fatal(err)
	}
	queue := outputs.Recommendations.Recommendations[0]
	if queue.IsUrgent != nil || len(queue.Evidence) != 1 || !strings.Contains(queue.Evidence[0].Text, "peaked at 10 in flight per replica, within C + B = 12") {
		t.Errorf("queue %+v", queue)
	}
}

func TestABurstAboveCPlusBMakesTheQueueUrgent(t *testing.T) {
	run, analysis, stages := burstRun(40) // 20 per replica
	outputs := burstOutputs(8, 4)
	if err := checkBursts(run, analysis, stages, &outputs); err != nil {
		t.Fatal(err)
	}
	queue := outputs.Recommendations.Recommendations[0]
	if queue.IsUrgent == nil || !*queue.IsUrgent || !strings.Contains(queue.Evidence[0].Text, "above C + B = 12: with these settings requests would be dropped") {
		t.Errorf("queue %+v", queue)
	}
}
