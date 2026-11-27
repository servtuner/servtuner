package propose

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
)

// RunSpec labels the caller sets on the runs it makes from a proposal.
// propose reads them back from spec.resolved.json: a bundle without a ladder
// label cannot be placed in the search and is refused.
const (
	// LabelLadder is the ladder kind of the run: probe, screen or full.
	LabelLadder = "servtuner.dev/tuning-ladder"
	// LabelConfig is the proposal's config_id.
	LabelConfig = "servtuner.dev/tuning-config"
)

// ErrInvalidInput marks an input the caller must fix: a bundle without the
// ladder label, or bundles that did not measure the same thing.
var ErrInvalidInput = errors.New("invalid input")

func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// ladderOf reads the run's ladder kind from its RunSpec labels.
func ladderOf(run compare.Run) (LadderKind, error) {
	value, ok := run.Spec.Labels[LabelLadder]
	if !ok {
		return "", invalidInput("run %s has no %s label: set it to probe, screen or full, as the proposal says", run.ID, LabelLadder)
	}
	switch kind := LadderKind(value); kind {
	case LadderProbe, LadderScreen, LadderFull:
		return kind, nil
	}
	return "", invalidInput("run %s: label %s is %q, not probe, screen or full", run.ID, LabelLadder, value)
}

// comparedField is something every run of a session must share for their
// objectives to be ranked together; the ladder alone
// may differ, and the label says which it is.
type comparedField struct {
	name  string
	value func(compare.Run) string
}

var comparedFields = []comparedField{
	{"target type", func(r compare.Run) string { return string(r.Spec.Target.Type) }},
	{"target model", func(r compare.Run) string { return r.Spec.Target.Model }},
	{"workload hash", func(r compare.Run) string {
		if r.Model == nil {
			return ""
		}
		return r.Model.Identity.WorkloadHash
	}},
	{"workload preset", func(r compare.Run) string { return encoded(r.Spec.WorkloadPreset) }},
	{"arrival process", func(r compare.Run) string { return string(r.Spec.Load.Pacing) }},
	{"SLOs", func(r compare.Run) string { return encoded(r.Spec.Slos) }},
	{"GPU count", func(r compare.Run) string {
		if replica := r.Analysis.Capacity.PerReplica; replica != nil && replica.TotalGpus != nil {
			return fmt.Sprint(*replica.TotalGpus)
		}
		return "unknown"
	}},
}

func encoded(value any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("unencodable: %v", err)
	}
	return string(bytes)
}

// checkComparable refuses bundles that measured different things, naming the
// first run that differs from the earliest one and the field. runs are in run
// id order.
func checkComparable(runs []Evidence) error {
	if len(runs) < 2 {
		return nil
	}
	first := runs[0].Run
	for _, other := range runs[1:] {
		for _, field := range comparedFields {
			if want, got := field.value(first), field.value(other.Run); want != got {
				return invalidInput("run %s has a different %s from run %s (%s, not %s): runs of one session must measure the same thing",
					other.Run.ID, field.name, first.ID, got, want)
			}
		}
	}
	return nil
}
