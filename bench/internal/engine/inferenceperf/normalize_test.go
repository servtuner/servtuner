package inferenceperf

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// fixturePhases match testdata/inferenceperf/native-platform: its first
// inference-perf stage (5 rps, 1s) plays the warmup, then two stages.
func fixturePhases() []engine.Phase {
	zero, one := 0, 1
	return []engine.Phase{
		{Name: "warmup", Kind: spec.PhaseKindWarmup, Duration: time.Second},
		{Name: "stage-0", Kind: spec.PhaseKindStage, StageIndex: &zero, OfferedRPS: 5, Duration: 2 * time.Second},
		{Name: "stage-1", Kind: spec.PhaseKindStage, StageIndex: &one, OfferedRPS: 10, Duration: 2 * time.Second},
	}
}

func normalizedFixture(t *testing.T) ([]engine.Window, []engine.RequestRow) {
	t.Helper()
	windows, rows, err := normalizeDirectory(filepath.Join(testdataDir, "native-platform"), fixturePhases())
	if err != nil {
		t.Fatalf("normalizeDirectory: %v", err)
	}
	return windows, rows
}

func TestNormalizeWindows(t *testing.T) {
	windows, _ := normalizedFixture(t)
	if len(windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(windows))
	}
	wantStart := time.Date(2026, 10, 29, 21, 18, 21, 414243000, time.UTC)
	if got := windows[1]; !got.Start.Equal(wantStart) || got.End.Sub(got.Start) != 2*time.Second {
		t.Errorf("stage-0 window = %v to %v, want start %v lasting its planned 2s", got.Start, got.End, wantStart)
	}
}

func TestNormalizeAssignsStagesAndSkipsWarmup(t *testing.T) {
	_, rows := normalizedFixture(t)
	// The fixture recorded 5, 10 and 20 requests; the 5 warmup requests stay
	// in the native output only.
	counts := map[int64]int{}
	for _, row := range rows {
		counts[*row.StageIndex]++
	}
	if len(rows) != 30 || counts[0] != 10 || counts[1] != 20 {
		t.Errorf("rows = %d, per stage = %v; want 30 with 10 and 20", len(rows), counts)
	}
}

func TestNormalizeSuccessRow(t *testing.T) {
	_, rows := normalizedFixture(t)
	var checked bool
	for _, row := range rows {
		if row.Error != nil || row.TTFTSeconds == nil {
			continue
		}
		checked = true
		if row.Status != 200 || row.TTFBSeconds != nil || row.Worker != nil {
			t.Errorf("status/ttfb/worker = %d/%v/%v", row.Status, row.TTFBSeconds, row.Worker)
		}
		if row.InputTokens == nil || row.OutputTokens == nil || *row.OutputTokens < 4 {
			t.Errorf("tokens = %v/%v", row.InputTokens, row.OutputTokens)
		}
		if len(row.ITLSeconds) != int(*row.OutputTokens)-1 {
			t.Errorf("itl count %d for %d output tokens", len(row.ITLSeconds), *row.OutputTokens)
		}
		// The stand-in server waits 20ms before its first token, then 5ms between tokens.
		if *row.TTFTSeconds < 0.02 || *row.TTFTSeconds > 0.5 || row.E2ESeconds < *row.TTFTSeconds {
			t.Errorf("ttft %v e2e %v", *row.TTFTSeconds, row.E2ESeconds)
		}
		break
	}
	if !checked {
		t.Fatal("no successful row in the fixture")
	}
}

func TestNormalizeFailedRow(t *testing.T) {
	_, rows := normalizedFixture(t)
	var failed int
	for _, row := range rows {
		if row.Error == nil {
			continue
		}
		failed++
		if row.Status != 503 || *row.Error != "HTTP Error 503" {
			t.Errorf("failed row status %d error %q", row.Status, *row.Error)
		}
		if row.TTFTSeconds != nil || row.InputTokens != nil || row.OutputTokens != nil || len(row.ITLSeconds) != 0 {
			t.Errorf("failed row carries measurements it never made: %+v", row)
		}
		if row.E2ESeconds <= 0 {
			t.Errorf("failed row e2e = %v", row.E2ESeconds)
		}
	}
	if failed == 0 {
		t.Fatal("fixture should contain 503 responses")
	}
}

func TestNormalizeLeavesSchedulingUnknown(t *testing.T) {
	_, rows := normalizedFixture(t)
	for _, row := range rows {
		if row.SendLagSeconds != nil || row.ScheduledAt != nil {
			t.Fatalf("scheduled_at = %v, send lag = %v, want both nil (unknown), never 0", row.ScheduledAt, row.SendLagSeconds)
		}
		if row.Phase != engine.RowPhaseStage || row.StageIndex == nil {
			t.Fatalf("phase = %q, stage index = %v, want a stage row with an index", row.Phase, row.StageIndex)
		}
	}
}

func TestNormalizeSentAtIsAnchoredToTheStageStart(t *testing.T) {
	windows, rows := normalizedFixture(t)
	for _, row := range rows {
		window := windows[*row.StageIndex+1]
		if row.SentAt.Before(window.Start) || row.SentAt.After(window.End.Add(time.Second)) {
			t.Fatalf("stage %d sent_at %v outside %v..%v", *row.StageIndex, row.SentAt, window.Start, window.End)
		}
	}
}

func TestStatusOfError(t *testing.T) {
	for errorClass, want := range map[string]int64{
		"HTTP Error 429":       429,
		"ClientConnectorError": 0,
		"TimeoutError":         0,
		"HTTP Error x":         0,
	} {
		if got := statusOfError(errorClass); got != want {
			t.Errorf("statusOfError(%q) = %d, want %d", errorClass, got, want)
		}
	}
}

func TestNormalizeWithoutReports(t *testing.T) {
	windows, rows, err := normalizeDirectory(t.TempDir(), fixturePhases())
	if err != nil || len(windows) != 0 || len(rows) != 0 {
		t.Errorf("empty dir: windows %d rows %d err %v", len(windows), len(rows), err)
	}
}
