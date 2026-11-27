package quality

import (
	"sort"
	"strconv"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// CheckKnobs compares the RunSpec's declared knobs with the captured values:
// verified when equal, mismatch when they differ,
// unverified when the target value could not be captured. The result is sorted
// by knob name.
func CheckKnobs(declared spec.RunSpecKnobs, captured map[string]spec.KnobValue) []spec.KnobCheck {
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	var checks []spec.KnobCheck
	for _, name := range names {
		want := renderKnob(declared[name])
		check := spec.KnobCheck{Name: name, Declared: want, Status: spec.KnobStatusUnverified}
		if value, found := captured[name]; found {
			got := renderKnob(value)
			check.Actual = &got
			check.Status = spec.KnobStatusMismatch
			if knobsEqual(want, got) {
				check.Status = spec.KnobStatusVerified
			}
		}
		checks = append(checks, check)
	}
	return checks
}

// renderKnob renders a knob value as a string; whole numbers print without a
// decimal point.
func renderKnob(value spec.KnobValue) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

// knobsEqual compares as numbers when both sides are numbers ("256" equals
// "256.0"), as booleans when both are booleans ("True" equals "true"), and
// exactly otherwise.
func knobsEqual(declared, captured string) bool {
	if a, errA := strconv.ParseFloat(declared, 64); errA == nil {
		if b, errB := strconv.ParseFloat(captured, 64); errB == nil {
			return a == b
		}
	}
	if a, errA := strconv.ParseBool(declared); errA == nil {
		if b, errB := strconv.ParseBool(captured); errB == nil {
			return a == b
		}
	}
	return declared == captured
}
