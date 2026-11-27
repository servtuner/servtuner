package propose

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// batch collects the proposals of one step, never twice and never a
// configuration that has run or cannot be run.
type batch struct {
	state     *state
	proposals []Proposed
	seen      map[string]bool
	notes     []string
}

func newBatch(s *state) *batch { return &batch{state: s, seen: map[string]bool{}} }

// admit adds a run of the knob set unless it ran already, is already in the
// batch, or a boot probe rules it out.
func (b *batch) admit(knobs Knobs, rationale Rationale) {
	key := knobs.key()
	if b.seen[key] || b.state.tried(knobs) || !b.state.viable(knobs) {
		return
	}
	b.seen[key] = true
	b.proposals = append(b.proposals, Proposed{ConfigId: knobs.id(), Action: ActionRun, Knobs: spec.TuningKnobSet(knobs), Repeats: ptr(1), Rationale: rationale})
}

func (b *batch) note(format string, args ...any) {
	b.notes = append(b.notes, fmt.Sprintf(format, args...))
}

func formatValue(value KnobValue) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// feasibilityNames are the knobs of the feasibility tier, in space order.
func (s *state) feasibilityNames() []string {
	var names []string
	for _, knob := range s.space.knobsOfTier(TierFeasibility) {
		names = append(names, knob.Name)
	}
	return names
}

// feasibilityCombinations are the base knobs plus every combination of the
// feasibility knobs' values, each knob's default first, so the first
// surviving combination is the baseline's.
func (s *state) feasibilityCombinations() []Knobs {
	combinations := []Knobs{s.space.Base.clone()}
	for _, knob := range s.space.knobsOfTier(TierFeasibility) {
		var next []Knobs
		for _, partial := range combinations {
			for _, value := range knob.orderedValues() {
				next = append(next, partial.with(knob.Name, value))
			}
		}
		combinations = next
	}
	return combinations
}

// feasibilityStep returns the boot probes still to make and the combinations
// that survive: they boot, and their KV cache holds the preset's concurrency.
func (s *state) feasibilityStep() (probes []Proposed, survivors []Knobs) {
	names := s.feasibilityNames()
	if len(names) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, combination := range s.feasibilityCombinations() {
		if _, infeasible := s.boots.infeasibleReason(combination); infeasible {
			continue
		}
		// A dropped combination stands for the one kept in its place, so a dropped
		// default still puts its twin first and the baseline is the same configuration.
		if folded, ok := s.foldedInto(combination); ok {
			combination = folded.kept
		}
		if seen[combination.key()] {
			continue
		}
		seen[combination.key()] = true
		result, probed := s.boots.probeOf(combination, names)
		if !probed {
			probes = append(probes, Proposed{
				ConfigId: combination.id(), Action: ActionBootProbe, Knobs: spec.TuningKnobSet(combination),
				Rationale: Rationale{Kind: KindBootProbe, Tier: ptr(TierFeasibility), BasedOnRuns: []string{},
					Text: "boot with no load to learn whether it starts and how much KV cache it gets (c_kv)"},
			})
			continue
		}
		if result.Outcome == spec.TuningBootOutcomeFeasible && s.holdsConcurrency(combination) {
			survivors = append(survivors, combination)
		}
	}
	return probes, survivors
}

// holdsConcurrency is false when a feasible boot probe's c_kv is below the
// concurrency the preset needs.
func (s *state) holdsConcurrency(knobs Knobs) bool {
	if s.space.MinConcurrency <= 0 {
		return true
	}
	ckv := s.boots.ckv(knobs)
	return ckv == nil || *ckv >= s.space.MinConcurrency
}

// viable is false for a knob set a boot probe rules out: one that failed to
// boot, whose KV cache cannot hold the preset's concurrency, or whose
// feasibility knobs are not a surviving combination.
func (s *state) viable(knobs Knobs) bool {
	_, unviable := s.whyNotViable(knobs)
	return !unviable
}

// whyNotViable says why a knob set cannot be run, or false when it can.
func (s *state) whyNotViable(knobs Knobs) (string, bool) {
	if reason, infeasible := s.boots.infeasibleReason(knobs); infeasible {
		return reason, true
	}
	if reason, folded := s.sameCapacityReason(knobs); folded {
		return reason, true
	}
	if !s.holdsConcurrency(knobs) {
		return "its KV cache cannot hold the concurrency the preset needs", true
	}
	names := s.feasibilityNames()
	if len(names) == 0 {
		return "", false
	}
	_, survivors := s.feasibilityStep()
	for _, survivor := range survivors {
		same := true
		for _, name := range names {
			same = same && sameValue(survivor[name], knobs[name])
		}
		if same {
			return "", false
		}
	}
	return "its feasibility knobs are not a combination that booted and holds the concurrency", true
}

// baseline is the first configuration to run: the base knobs, the first
// surviving feasibility combination, and each other knob at its default (a
// structural knob with no default takes its first value; a scheduler or
// fine-tuning knob with none is left to vLLM).
func (s *state) baseline(survivors []Knobs) Knobs {
	knobs := s.space.Base.clone()
	if len(survivors) > 0 {
		knobs = survivors[0].clone()
	}
	for _, knob := range s.space.Knobs {
		switch {
		case knob.Tier == TierFeasibility:
		case knob.Default != nil:
			knobs[knob.Name] = knob.Default
		case knob.Tier == TierStructural && len(knob.Values) > 0:
			knobs[knob.Name] = knob.Values[0]
		}
	}
	return knobs
}

// abTest proposes every other allowed value of the knobs of the given tiers,
// one at a time, against the leader.
func (s *state) abTest(b *batch, tiers []Tier, kind RationaleKind, include func(KnobSpec) bool) {
	for _, knob := range s.space.Knobs {
		if !containsTier(tiers, knob.Tier) || !include(knob) {
			continue
		}
		// A knob the leader does not set has its default, if the space gives one.
		current, hasCurrent := s.leader.knobs[knob.Name]
		if !hasCurrent && knob.Default != nil {
			current, hasCurrent = knob.Default, true
		}
		for _, value := range knob.orderedValues() {
			if hasCurrent && sameValue(current, value) {
				continue
			}
			b.admit(s.leader.knobs.with(knob.Name, value), Rationale{
				Kind: kind, Tier: ptr(knob.Tier), Against: ptr(s.leader.id), BasedOnRuns: screeningIDs(s.leader),
				Text: fmt.Sprintf("A/B %s=%s against the leader %s", knob.Name, formatValue(value), s.leader.id),
			})
		}
	}
}

func containsTier(tiers []Tier, tier Tier) bool {
	for _, candidate := range tiers {
		if candidate == tier {
			return true
		}
	}
	return false
}

func everyKnob(KnobSpec) bool { return true }

// structuralStep screens every surviving value of the structural knobs, and
// of the feasibility knobs that survived the boot probes.
func (s *state) structuralStep() ([]Proposed, string, []string) {
	b := newBatch(s)
	s.abTest(b, []Tier{TierFeasibility, TierStructural}, KindStructural, everyKnob)
	return b.proposals, "every surviving value has been screened against the leader", b.notes
}

func (s *state) fineTuningStep() ([]Proposed, string, []string) {
	b := newBatch(s)
	vllmAdvice := s.adviceOf(s.leader)
	s.abTest(b, []Tier{TierFineTuning}, KindFineTuningAB, func(knob KnobSpec) bool {
		rule, gated := fineTuningGate[knob.Name]
		if !gated {
			return true
		}
		for _, advice := range vllmAdvice {
			if advice.Rule == rule {
				return true
			}
		}
		b.note("%s is screened only when %s fires on the leader; it did not", knob.Name, rule)
		return false
	})
	return b.proposals, "every fine-tuning value has been screened against the leader", b.notes
}

// sortedNotes keeps the notes in a fixed order, without repeats.
func sortedNotes(notes []string) []string {
	unique := map[string]bool{}
	result := []string{}
	for _, note := range notes {
		if !unique[note] {
			unique[note] = true
			result = append(result, note)
		}
	}
	sort.Strings(result)
	return result
}
