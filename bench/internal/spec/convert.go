package spec

import (
	"fmt"
	"time"
)

// Std converts the schema's Go-style duration string ("90s", "1m30s") to a
// time.Duration. The schema's pattern is looser than time.ParseDuration for
// some inputs, so this can still fail.
func (d Duration) Std() (time.Duration, error) {
	parsed, err := time.ParseDuration(string(d))
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", string(d), err)
	}
	return parsed, nil
}

// NewUTCTimestamp formats t as the schema's one timestamp representation: RFC
// 3339 in UTC with a Z suffix.
func NewUTCTimestamp(t time.Time) UtcTimestamp {
	return UtcTimestamp(t.UTC().Format(time.RFC3339Nano))
}

// Time parses the timestamp. The schema's pattern does not reject impossible
// dates, so this is where they are caught (schema/CLAUDE.md).
func (t UtcTimestamp) Time() (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, string(t))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", string(t), err)
	}
	return parsed, nil
}

// analysisProbe is the smallest RunSpec that carries an analysis block with
// every sub-block empty. Parsing it makes the schema, not this code, supply
// the defaults.
const analysisProbe = `{
  "apiVersion": "servtuner.dev/v2", "kind": "RunSpec", "name": "defaults-probe",
  "target": {"type": "vllm", "endpoint": "http://probe", "model": "probe", "tls": {"enabled": false}},
  "engine": {"type": "loadgen"},
  "workload": {"streaming": false, "dataset": {"type": "synthetic", "input_tokens": {"mean": 1}, "output_tokens": {"mean": 1}}},
  "load": {"stages": [{"rate": 1, "duration": "1s"}]},
  "analysis": {"stage_validity": {}, "headroom": {}, "limits": {}}
}`

// DefaultAnalysis returns the analysis block with the schema's declared
// defaults applied, read through ParseRunSpec like every document.
func DefaultAnalysis() (Analysis, error) {
	probe, err := ParseRunSpec([]byte(analysisProbe))
	if err != nil {
		return Analysis{}, fmt.Errorf("apply analysis defaults: %w", err)
	}
	return *probe.Analysis, nil
}

// DefaultStageValidity returns the stage validity thresholds with the schema's
// declared defaults applied.
func DefaultStageValidity() (StageValidity, error) {
	defaults, err := DefaultAnalysis()
	if err != nil {
		return StageValidity{}, err
	}
	return *defaults.StageValidity, nil
}

// ResolveAnalysis returns a RunSpec's analysis block with every default
// filled in: the schema defaults what is inside a block that is present, and
// this fills the blocks (or the whole analysis) that are absent.
func ResolveAnalysis(declared *Analysis) (Analysis, error) {
	defaults, err := DefaultAnalysis()
	if err != nil || declared == nil {
		return defaults, err
	}
	resolved := *declared
	if resolved.StageValidity == nil {
		resolved.StageValidity = defaults.StageValidity
	}
	if resolved.Headroom == nil {
		resolved.Headroom = defaults.Headroom
	}
	if resolved.Limits == nil {
		resolved.Limits = defaults.Limits
	}
	return resolved, nil
}
