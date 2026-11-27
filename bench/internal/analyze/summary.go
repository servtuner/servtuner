package analyze

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/stats"
	"github.com/servtuner/servtuner/bench/internal/storage"
)

// SummarizeBundle computes summary.json from requests.parquet and phases.json
// in the store, never from in-memory state, so that `servtuner analyze` gives the
// same answer (bench/CLAUDE.md, step 6).
//
// Stage validity covers the terms that need only the raw requests:
// achieved RPS against offered, and send lag. is_client_bound is always false
// until the bench pod's own CPU and memory are collected; max_sustainable_rps
// is omitted until the SLO evaluation exists, because it is defined as the
// highest valid stage whose SLOs hold.
//
// The request records come from records (ParquetRecords streams them from the
// bundle directory) and are read one stage at a time, so a large run is never
// held in memory whole.
func SummarizeBundle(ctx context.Context, store storage.BundleStore, records input.RecordSource, thresholds spec.StageValidity) (spec.Summary, error) {
	phasesBytes, err := readAll(ctx, store, bundle.PhasesJSON)
	if err != nil {
		return spec.Summary{}, err
	}
	phases, err := spec.Parse[spec.Phases](spec.SchemaPhases, phasesBytes)
	if err != nil {
		return spec.Summary{}, fmt.Errorf("read %s: %w", bundle.PhasesJSON, err)
	}
	maxSendLag, err := thresholds.MaxSendLagP99.Std()
	if err != nil {
		return spec.Summary{}, fmt.Errorf("stage validity max_send_lag_p99: %w", err)
	}

	summary := spec.Summary{Stages: []spec.StageSummary{}}
	for _, phase := range phases.Phases {
		if phase.Kind != spec.PhaseKindStage || phase.StageIndex == nil || phase.OfferedRps == nil {
			continue
		}
		window, err := windowOf(phase)
		if err != nil {
			return spec.Summary{}, err
		}
		rows, err := rowsOfStage(records, *phase.StageIndex)
		if err != nil {
			return spec.Summary{}, err
		}
		stage := summarizeStage(*phase.StageIndex, *phase.OfferedRps, window, rows)
		stage.IsValid = isStageValid(stage, thresholds.MinAchievedRpsRatio, maxSendLag)
		summary.Stages = append(summary.Stages, stage)
	}
	return summary, nil
}

func readAll(ctx context.Context, store storage.BundleStore, name string) ([]byte, error) {
	reader, err := store.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return content, nil
}

func windowOf(phase spec.Phase) (time.Duration, error) {
	start, err := phase.Start.Time()
	if err != nil {
		return 0, fmt.Errorf("phase %s start: %w", phase.Name, err)
	}
	end, err := phase.End.Time()
	if err != nil {
		return 0, fmt.Errorf("phase %s end: %w", phase.Name, err)
	}
	return end.Sub(start), nil
}

func summarizeStage(index int, offeredRPS float64, window time.Duration, rows []engine.RequestRow) spec.StageSummary {
	stage := spec.StageSummary{StageIndex: index, OfferedRps: offeredRPS, Requests: len(rows)}
	var sendLags []float64
	for _, row := range rows {
		if row.Error != nil || row.Status == 0 || row.Status >= 400 {
			stage.Errors++
		}
		if lag, known := aggregate.SendLag(row); known {
			sendLags = append(sendLags, lag)
		}
	}
	if window > 0 {
		stage.AchievedRps = float64(len(rows)) / window.Seconds()
	}
	if len(sendLags) > 0 {
		p99 := stats.Percentile(sendLags, 0.99)
		stage.SendLagP99Seconds = &p99
	}
	return stage
}

func isStageValid(stage spec.StageSummary, minAchievedRatio float64, maxSendLag time.Duration) bool {
	if stage.Requests == 0 || stage.OfferedRps <= 0 || stage.IsClientBound {
		return false
	}
	if stage.AchievedRps/stage.OfferedRps < minAchievedRatio {
		return false
	}
	return stage.SendLagP99Seconds == nil || *stage.SendLagP99Seconds <= maxSendLag.Seconds()
}
