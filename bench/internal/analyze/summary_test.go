package analyze

import (
	"bytes"
	"context"
	"math"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"

	"github.com/parquet-go/parquet-go"
)

var stageStart = time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC)

// writeBundle stores phases.json (one 10s stage at 10 RPS) and requests.parquet.
func writeBundle(t *testing.T, rows []engine.RequestRow) *storage.FileStore {
	t.Helper()
	sink := bundle.NewRequestSink()
	for _, row := range rows {
		if err := sink.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	content, err := sink.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return writeBundleWithRequests(t, content)
}

// writeBundleWithRequests is writeBundle with requests.parquet given as bytes.
func writeBundleWithRequests(t *testing.T, content []byte) *storage.FileStore {
	t.Helper()
	ctx := context.Background()
	store := storage.NewFileStore(t.TempDir())
	writer := bundle.NewWriter(store)
	stageIndex := 0
	offered := 10.0
	phases := spec.Phases{Phases: []spec.Phase{
		{Name: "warmup", Kind: spec.PhaseKindWarmup, Start: spec.NewUTCTimestamp(stageStart.Add(-time.Second)), End: spec.NewUTCTimestamp(stageStart)},
		{Name: "stage-0", Kind: spec.PhaseKindStage, StageIndex: &stageIndex, OfferedRps: &offered, Start: spec.NewUTCTimestamp(stageStart), End: spec.NewUTCTimestamp(stageStart.Add(10 * time.Second))},
	}}
	if err := writer.WriteDocument(ctx, bundle.PhasesJSON, spec.SchemaPhases, phases); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteFile(ctx, bundle.RequestsParquet, content); err != nil {
		t.Fatal(err)
	}
	return store
}

func rowsWithLag(count int, lag float64, failures int) []engine.RequestRow {
	rows := make([]engine.RequestRow, count)
	for i := range rows {
		status := int64(200)
		if i < failures {
			status = 503
		}
		scheduled, sendLag, stage := stageStart, lag, int64(0)
		rows[i] = engine.RequestRow{ScheduledAt: &scheduled, SentAt: stageStart, SendLagSeconds: &sendLag, E2ESeconds: 1, Status: status, Phase: engine.RowPhaseStage, StageIndex: &stage}
	}
	return rows
}

func summarize(t *testing.T, rows []engine.RequestRow) spec.StageSummary {
	t.Helper()
	thresholds, err := spec.DefaultStageValidity()
	if err != nil {
		t.Fatal(err)
	}
	store := writeBundle(t, rows)
	summary, err := SummarizeBundle(context.Background(), store, ParquetRecords(store.Root()), thresholds)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Stages) != 1 {
		t.Fatalf("got %d stages, want 1 (warmup is excluded)", len(summary.Stages))
	}
	return summary.Stages[0]
}

func TestStageWithAllRequestsOnTimeIsValid(t *testing.T) {
	stage := summarize(t, rowsWithLag(100, 0.001, 2))
	if !stage.IsValid || stage.Requests != 100 || stage.Errors != 2 || stage.AchievedRps != 10 {
		t.Errorf("stage = %+v", stage)
	}
	if stage.SendLagP99Seconds == nil || *stage.SendLagP99Seconds != 0.001 {
		t.Errorf("send lag p99 = %v", stage.SendLagP99Seconds)
	}
}

func TestStageBelowAchievedRatioIsInvalid(t *testing.T) {
	if stage := summarize(t, rowsWithLag(90, 0.001, 0)); stage.IsValid {
		t.Errorf("90%% of offered rate was valid: %+v", stage)
	}
}

func TestStageWithHighSendLagIsInvalid(t *testing.T) {
	if stage := summarize(t, rowsWithLag(100, 0.2, 0)); stage.IsValid {
		t.Errorf("200ms send lag was valid: %+v", stage)
	}
}

func TestStageWithNoRequestsIsInvalid(t *testing.T) {
	stage := summarize(t, nil)
	if stage.IsValid || stage.Requests != 0 || stage.AchievedRps != 0 || stage.SendLagP99Seconds != nil {
		t.Errorf("stage = %+v", stage)
	}
}

func TestSummarizeFailsWithoutRawFiles(t *testing.T) {
	thresholds, _ := spec.DefaultStageValidity()
	empty := storage.NewFileStore(t.TempDir())
	if _, err := SummarizeBundle(context.Background(), empty, ParquetRecords(empty.Root()), thresholds); err == nil {
		t.Error("summary of an empty bundle succeeded")
	}
}

func TestWarmupAndStagelessRowsBelongToNoStage(t *testing.T) {
	zero, one := int64(0), int64(1)
	rows := input.SliceRecords{
		{Phase: engine.RowPhaseStage, StageIndex: &zero},
		{Phase: engine.RowPhaseWarmup, StageIndex: &zero},
		{Phase: engine.RowPhaseWarmup},
		{Phase: engine.RowPhaseStage, StageIndex: &one},
	}
	for _, index := range []int{0, 1} {
		stageRows, err := rowsOfStage(rows, index)
		if err != nil {
			t.Fatal(err)
		}
		if len(stageRows) != 1 {
			t.Errorf("stage %d has %d rows, want 1 (warmup rows excluded)", index, len(stageRows))
		}
	}
}

// oldRow is requests.parquet as written before the phase column: scheduled_at,
// send_lag_seconds and stage_index required, no phase.
type oldRow struct {
	ScheduledAt    time.Time `parquet:"scheduled_at,timestamp(nanosecond)"`
	SentAt         time.Time `parquet:"sent_at,timestamp(nanosecond)"`
	SendLagSeconds float64   `parquet:"send_lag_seconds"`
	ITLSeconds     []float64 `parquet:"itl_seconds,list"`
	E2ESeconds     float64   `parquet:"e2e_seconds"`
	Status         int64     `parquet:"status"`
	StageIndex     int64     `parquet:"stage_index"`
}

func TestOldShapeBundleCountsRowsAsStageRows(t *testing.T) {
	var buffer bytes.Buffer
	writer := parquet.NewGenericWriter[oldRow](&buffer)
	var old []oldRow
	for i := 0; i < 100; i++ {
		old = append(old, oldRow{ScheduledAt: stageStart, SentAt: stageStart, SendLagSeconds: 0.001, ITLSeconds: []float64{}, E2ESeconds: 1, Status: 200, StageIndex: 0})
	}
	// A legacy "unknown" row: zero time and NaN lag.
	old = append(old, oldRow{SentAt: stageStart, SendLagSeconds: math.NaN(), ITLSeconds: []float64{}, E2ESeconds: 1, Status: 200})
	if _, err := writer.Write(old); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	rows, err := bundle.ReadRequests(buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Phase != "" || rows[0].StageIndex == nil || *rows[0].StageIndex != 0 {
		t.Fatalf("old row read as phase %q, stage index %v", rows[0].Phase, rows[0].StageIndex)
	}

	thresholds, err := spec.DefaultStageValidity()
	if err != nil {
		t.Fatal(err)
	}
	store := writeBundleWithRequests(t, buffer.Bytes())
	summary, err := SummarizeBundle(context.Background(), store, ParquetRecords(store.Root()), thresholds)
	if err != nil {
		t.Fatal(err)
	}
	stage := summary.Stages[0]
	if len(summary.Stages) != 1 || stage.Requests != 101 {
		t.Fatalf("stages = %+v, want one stage counting all 101 old rows", summary.Stages)
	}
	if stage.SendLagP99Seconds == nil || *stage.SendLagP99Seconds != 0.001 {
		t.Errorf("send lag p99 = %v, want 0.001 (the legacy unknown row is skipped)", stage.SendLagP99Seconds)
	}
}

func TestSummarizeBundleLeavesWarmupAndStagelessRowsOut(t *testing.T) {
	zero := int64(0)
	rows := rowsWithLag(10, 0.001, 0)
	rows = append(rows,
		engine.RequestRow{SentAt: stageStart, E2ESeconds: 1, Status: 200, Phase: engine.RowPhaseWarmup, StageIndex: &zero},
		engine.RequestRow{SentAt: stageStart, E2ESeconds: 1, Status: 200, Phase: engine.RowPhaseWarmup},
		engine.RequestRow{SentAt: stageStart, E2ESeconds: 1, Status: 200, Phase: engine.RowPhaseStage},
	)
	if stage := summarize(t, rows); stage.Requests != 10 {
		t.Errorf("requests = %d, want 10 (warmup and index-less rows excluded)", stage.Requests)
	}
}
