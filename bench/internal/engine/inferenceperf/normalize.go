package inferenceperf

// Normalization of inference-perf 0.7.0 output into request rows.
//
// Source of the formats (inference-perf 0.7.0, PyPI sdist/wheel, read 2026-10-29):
//   - inference_perf/reportgen/base.py: build_per_request_lifecycle_entry
//     (per_request_lifecycle_metrics.json), compute_request_latency_metrics
//     (the TTFT and ITL definitions) and the per-stage report names
//     stage_<id>_lifecycle_metrics.json and inference-perf.partial.stage_<id>.yaml.
//   - inference_perf/apis/base.py: RequestLifecycleMetric and InferenceInfo.
//   - inference_perf/client/modelserver/openai_client.py: how start_time,
//     end_time and the error type are recorded.
//
// What the report does and does not carry:
//   - start_time and end_time are time.perf_counter() values: monotonic, with
//     an arbitrary origin, so they are not UTC. scheduled_time and stage_id are
//     recorded in memory but NOT written to the per-request report.
//   - Each stage's UTC start is in the partial report (run.time.start, taken
//     from time.time() at the stage's start).
//
// Consequences, all deliberate:
//   - stage is recovered from order: stages run one after another, so the
//     requests sorted by start_time fall into stages in order, and the counts
//     come from each stage report's load_summary.count.
//   - sent_at is approximate: the stage's UTC start plus the request's
//     start_time offset from the first request of its stage. The error is the
//     first request's own schedule delay (about a millisecond).
//   - scheduled_at and send_lag are unknown. inference-perf reports the
//     schedule delay only as a per-stage distribution, never per request.
//     The row leaves both nil (null in Parquet): an unmistakable "unknown",
//     never a 0 that would claim a perfect schedule. Stage validity therefore cannot be judged from this engine's
//     rows; use its stage reports' schedule_delay instead.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Names of the native report files (see the source list above).
const (
	perRequestReportFile = "per_request_lifecycle_metrics.json"
	stageReportPattern   = "stage_%d_lifecycle_metrics.json"
	partialReportPattern = "inference-perf.partial.stage_%d.yaml"
)

const (
	// successStatus: inference-perf records a request as an error unless the
	// HTTP status was 200, so an entry without an error was a 200.
	successStatus     = 200
	httpErrorPrefix   = "HTTP Error "
	noResponseStatus  = 0 // the request record's "no response arrived"
	serverUsageOutput = "completion_tokens"
)

// perRequestEntry is the subset of a per-request report entry that is read.
type perRequestEntry struct {
	StartTime       float64 `json:"start_time"`
	EndTime         float64 `json:"end_time"`
	ComputedMetrics *struct {
		TimeToFirstToken    *float64  `json:"time_to_first_token"`
		InterTokenLatencies []float64 `json:"inter_token_latencies"`
		InputTokens         *int64    `json:"input_tokens"`
		OutputTokens        *int64    `json:"output_tokens"`
	} `json:"computed_metrics"`
	Info *struct {
		RequestMetrics struct {
			Text struct {
				InputTokens int64 `json:"input_tokens"`
			} `json:"text"`
		} `json:"request_metrics"`
		ResponseMetrics *struct {
			OutputTokens int64              `json:"output_tokens"`
			ServerUsage  map[string]float64 `json:"server_usage"`
		} `json:"response_metrics"`
	} `json:"info"`
	Error *struct {
		ErrorType string `json:"error_type"`
	} `json:"error"`
}

type stageReport struct {
	LoadSummary struct {
		Count int `json:"count"`
	} `json:"load_summary"`
}

type partialReport struct {
	Run struct {
		Time struct {
			Start time.Time `yaml:"start"`
		} `yaml:"time"`
	} `yaml:"run"`
}

// normalizeDirectory reads the native output in dir. phases are the planned
// phases, warmup first: phase i is inference-perf stage i. It returns the
// window of every phase that has a report and the rows of the stage phases
// (warmup requests stay in the native output only).
func normalizeDirectory(dir string, phases []engine.Phase) ([]engine.Window, []engine.RequestRow, error) {
	windows, starts, err := readWindows(dir, phases)
	if err != nil {
		return nil, nil, err
	}
	entries, err := readPerRequest(filepath.Join(dir, perRequestReportFile))
	if err != nil {
		return windows, nil, err
	}
	stageOfEntry, err := assignStages(dir, entries, len(phases))
	if err != nil {
		return windows, nil, err
	}
	firstStart := firstStartPerStage(entries, stageOfEntry)
	var rows []engine.RequestRow
	for index, entry := range entries {
		phase := phases[stageOfEntry[index]]
		if phase.Kind != spec.PhaseKindStage {
			continue
		}
		offset := time.Duration((entry.StartTime - firstStart[stageOfEntry[index]]) * float64(time.Second))
		rows = append(rows, toRow(entry, starts[stageOfEntry[index]].Add(offset), *phase.StageIndex))
	}
	return windows, rows, nil
}

// readWindows returns one window per phase that has a partial report, and the
// UTC start of each. A phase's window ends when its planned duration has
// passed: the partial report's own end time also covers the tail of in-flight
// requests, which is not part of the offered load.
func readWindows(dir string, phases []engine.Phase) ([]engine.Window, map[int]time.Time, error) {
	var windows []engine.Window
	starts := make(map[int]time.Time)
	for index, phase := range phases {
		content, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf(partialReportPattern, index)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read partial report of %s: %w", phase.Name, err)
		}
		var report partialReport
		if err := yaml.Unmarshal(content, &report); err != nil {
			return nil, nil, fmt.Errorf("parse partial report of %s: %w", phase.Name, err)
		}
		start := report.Run.Time.Start.UTC()
		starts[index] = start
		windows = append(windows, engine.Window{Phase: phase, Start: start, End: start.Add(phase.Duration)})
	}
	return windows, starts, nil
}

func readPerRequest(path string) ([]perRequestEntry, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		// No report means no request was recorded (for example the CLI failed
		// before the first stage); the caller reports the CLI's own error.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	var entries []perRequestEntry
	if err := json.NewDecoder(file).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].StartTime < entries[j].StartTime })
	return entries, nil
}

// assignStages gives every entry (sorted by start) its inference-perf stage id
// from the per-stage request counts.
func assignStages(dir string, entries []perRequestEntry, stageCount int) ([]int, error) {
	stageOfEntry := make([]int, 0, len(entries))
	for stage := 0; stage < stageCount; stage++ {
		content, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf(stageReportPattern, stage)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read report of stage %d: %w", stage, err)
		}
		var report stageReport
		if err := json.Unmarshal(content, &report); err != nil {
			return nil, fmt.Errorf("parse report of stage %d: %w", stage, err)
		}
		for range report.LoadSummary.Count {
			stageOfEntry = append(stageOfEntry, stage)
		}
	}
	if len(stageOfEntry) != len(entries) {
		return nil, fmt.Errorf("stage reports count %d requests but %s has %d", len(stageOfEntry), perRequestReportFile, len(entries))
	}
	return stageOfEntry, nil
}

func firstStartPerStage(entries []perRequestEntry, stageOfEntry []int) map[int]float64 {
	first := make(map[int]float64)
	for index, entry := range entries {
		if _, seen := first[stageOfEntry[index]]; !seen {
			first[stageOfEntry[index]] = entry.StartTime
		}
	}
	return first
}

// toRow converts one entry. A failed request keeps its timing and error but
// has no token counts or first-token time: the CLI records zero tokens for it,
// which would be a measurement it never made.
func toRow(entry perRequestEntry, sentAt time.Time, stageIndex int) engine.RequestRow {
	stage := int64(stageIndex)
	row := engine.RequestRow{
		SentAt:     sentAt,
		ITLSeconds: []float64{},
		E2ESeconds: entry.EndTime - entry.StartTime,
		Status:     successStatus,
		Phase:      engine.RowPhaseStage,
		StageIndex: &stage,
	}
	if entry.Error != nil {
		errorClass := entry.Error.ErrorType
		row.Error = &errorClass
		row.Status = statusOfError(errorClass)
		return row
	}
	if entry.ComputedMetrics != nil {
		row.TTFTSeconds = entry.ComputedMetrics.TimeToFirstToken
		row.ITLSeconds = append(row.ITLSeconds, entry.ComputedMetrics.InterTokenLatencies...)
	}
	row.InputTokens, row.OutputTokens = tokenCounts(entry)
	return row
}

// statusOfError reads the HTTP status out of "HTTP Error 503"; any other error
// class means no response arrived.
func statusOfError(errorClass string) int64 {
	if code, isHTTP := strings.CutPrefix(errorClass, httpErrorPrefix); isHTTP {
		if status, err := strconv.ParseInt(code, 10, 64); err == nil {
			return status
		}
	}
	return noResponseStatus
}

// tokenCounts prefers the server's usage numbers, which are exact, over the
// CLI's own tokenizer count; the CLI already prefers the server for the input.
func tokenCounts(entry perRequestEntry) (input, output *int64) {
	if entry.Info == nil {
		return nil, nil
	}
	inputTokens := entry.Info.RequestMetrics.Text.InputTokens
	if usage := entry.Info.ResponseMetrics; usage != nil {
		outputTokens := usage.OutputTokens
		if reported, ok := usage.ServerUsage[serverUsageOutput]; ok {
			outputTokens = int64(reported)
		}
		output = &outputTokens
	}
	return &inputTokens, output
}
