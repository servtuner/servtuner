package propose

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/advisors/vllm"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// fineTuningGate names the advisor rule that must fire on the leader before a
// fine-tuning knob is screened: api_server_count only helps a frontend-bound
// target (V7).
var fineTuningGate = map[string]string{
	target.KnobVLLMAPIServerCount: vllm.RuleFrontendBound,
}

// advisorMove is what an advisor rule asks of a knob.
type advisorMove struct {
	knob      string
	direction int
	// useAdvised: the rule gives the value to move to (its recommended), not
	// only a direction.
	useAdvised bool
	// capByCKV: the move up is bounded by the KV cache (C_kv).
	capByCKV bool
}

// advisorMoves are the rules the scheduler tier follows:
// V1 raises max_num_seqs, V2 lowers it, V3 lowers max_model_len, V4 raises
// max_num_batched_tokens and V5 lowers it.
var advisorMoves = map[string]advisorMove{
	vllm.RuleSeqCapBound:   {knob: target.KnobVLLMMaxNumSeqs, direction: +1, useAdvised: true, capByCKV: true},
	vllm.RuleKVBound:       {knob: target.KnobVLLMMaxNumSeqs, direction: -1},
	vllm.RuleModelLenSlack: {knob: target.KnobVLLMMaxModelLen, direction: -1, useAdvised: true},
	vllm.RulePrefillHeavy:  {knob: target.KnobVLLMMaxNumBatchedTokens, direction: +1},
	vllm.RuleDecodeHeavy:   {knob: target.KnobVLLMMaxNumBatchedTokens, direction: -1},
}

// schedulerStep follows the advisors on the leader, then takes coordinate
// steps on max_num_seqs and max_num_batched_tokens around it, then A/Bs the
// other scheduler knobs (chunked prefill, gpu_memory_utilization).
func (s *state) schedulerStep() ([]Proposed, string, []string) {
	const converged = "no advisor move is left on the leader, every coordinate step around it has run and the other scheduler knobs are screened"
	b := newBatch(s)
	s.followAdvisors(b)
	if len(b.proposals) > 0 {
		return b.proposals, converged, b.notes
	}
	s.coordinateStep(b)
	if len(b.proposals) > 0 {
		return b.proposals, converged, b.notes
	}
	s.abTest(b, []Tier{TierScheduler}, KindSchedulerAB, func(knob KnobSpec) bool { return !target.IsRangeTunedKnob(knob.Name) })
	return b.proposals, converged, b.notes
}

func (s *state) followAdvisors(b *batch) {
	leader := s.leader
	for _, advice := range s.adviceOf(leader) {
		move, ok := advisorMoves[advice.Rule]
		if !ok || advice.Knob != move.knob {
			continue
		}
		knob, inSpace := s.space.knob(move.knob)
		if !inSpace {
			b.note("%s fired on the leader but %s is not in the search space", advice.Rule, move.knob)
			continue
		}
		current, ok := s.currentValue(leader, knob)
		if !ok {
			b.note("%s fired on the leader but %s has no current value: set it in base or give it a default", advice.Rule, move.knob)
			continue
		}
		var advised *float64
		if move.useAdvised {
			advised = advice.Recommended
		}
		var ceiling *float64
		if move.capByCKV {
			ceiling = s.ceiling(leader)
		}
		next, ok := knob.step(current, move.direction, advised, ceiling)
		if !ok {
			b.note("%s fired on the leader but %s is already at the edge of what the search space allows", advice.Rule, move.knob)
			continue
		}
		moved := leader.knobs.with(move.knob, next)
		if why, unviable := s.whyNotViable(moved); unviable && !s.tried(moved) {
			b.note("%s fired on the leader: %s to %s, but %s", advice.Rule, move.knob, formatValue(next), why)
			continue
		}
		b.admit(moved, Rationale{
			Kind: KindAdvisor, Tier: ptr(TierScheduler), Advisor: ptr(advice.Rule), Against: ptr(leader.id), BasedOnRuns: screeningIDs(leader),
			Text: fmt.Sprintf("%s fired on the leader: move %s from %s to %s", advice.Rule, move.knob, formatValue(current), formatValue(next)),
		})
	}
}

// currentValue is the knob's value on the configuration, or its default.
func (s *state) currentValue(config *configuration, knob KnobSpec) (float64, bool) {
	if value, ok := config.knobs.number(knob.Name); ok {
		return value, true
	}
	value, ok := knob.Default.(float64)
	return value, ok
}

// ceiling is the most max_num_seqs may reach on a configuration: the C_kv of
// the boot probe and the maximum of its V1 and V2 advice (the requests the
// measured KV usage could hold), the smaller. The advisor step and the grid
// both use it.
func (s *state) ceiling(config *configuration) *float64 {
	ceiling := s.boots.ckv(config.knobs)
	for _, advice := range s.adviceOf(config) {
		isKVRule := advice.Rule == vllm.RuleSeqCapBound || advice.Rule == vllm.RuleKVBound
		if isKVRule && advice.Knob == target.KnobVLLMMaxNumSeqs && advice.Max != nil && (ceiling == nil || *advice.Max < *ceiling) {
			ceiling = advice.Max
		}
	}
	return ceiling
}

// coordinateKnobs are the knobs the coordinate step moves, in order: concurrency first, since it
// moves capacity most, then the prefill chunk.
var coordinateKnobs = []string{target.KnobVLLMMaxNumSeqs, target.KnobVLLMMaxNumBatchedTokens}

// coordinateStep proposes one step of a coordinate search around the leader, once the advisors
// are spent (it replaced a 3x3 grid, 2026-11-24, which spent 8 runs and tried cells in a fixed
// order, down first). For each knob in coordinateKnobs it tries one neighbour of the leader's
// value: first in the direction the evidence points (on from a neighbour the leader beat, else
// up), then the other way once that has lost. A knob whose two neighbours have run, or lie
// outside the space, is converged and the next knob is tried. A winning step makes a new leader
// and the search goes on from it. One run per call: the next step depends on its result.
func (s *state) coordinateStep(b *batch) {
	// Both knobs, as the grid this replaced required: a space tuning only one of them leaves it
	// to the advisors.
	for _, name := range coordinateKnobs {
		if _, inSpace := s.space.knob(name); !inSpace {
			return
		}
	}
	// A leader with no SLO-max scores 0, and a step must beat it by the noise floor, so no step
	// could ever take the lead: it would only spend runs.
	if !s.leader.hasSLOMax {
		b.note("no coordinate steps: the leader met no SLO on its ladder, so a step could not be judged against it")
		return
	}
	ceiling := s.ceiling(s.leader)
	for _, name := range coordinateKnobs {
		knob, _ := s.space.knob(name)
		at, known := s.currentValue(s.leader, knob)
		if !known {
			b.note("the coordinate step skips %s: it has no current value; set it in base or give it a default", name)
			continue
		}
		var knobCeiling *float64
		if name == target.KnobVLLMMaxNumSeqs {
			knobCeiling = ceiling
		}
		for _, direction := range s.directions(knob, at) {
			next, ok := knob.step(at, direction, nil, knobCeiling)
			if !ok {
				continue
			}
			moved := s.leader.knobs.with(name, next)
			if s.tried(moved) {
				continue
			}
			if why, unviable := s.whyNotViable(moved); unviable {
				b.note("the coordinate step skips %s=%s: %s", name, formatValue(next), why)
				continue
			}
			cell := GridCell{MaxNumSeqs: PositionCentre, MaxNumBatchedTokens: PositionCentre}
			position := PositionUp
			if direction < 0 {
				position = PositionDown
			}
			if name == target.KnobVLLMMaxNumSeqs {
				cell.MaxNumSeqs = position
			} else {
				cell.MaxNumBatchedTokens = position
			}
			b.admit(moved, Rationale{
				Kind: KindGrid, Tier: ptr(TierScheduler), GridCell: &cell, Against: ptr(s.leader.id), BasedOnRuns: screeningIDs(s.leader),
				Text: fmt.Sprintf("coordinate step: %s from %s to %s (%s) around the leader %s, %s", name, formatValue(at), formatValue(next),
					position, s.leader.id, s.whyDirection(knob, at, direction)),
			})
			return
		}
	}
}

// directions orders the two steps of a knob: the way the leader was reached first. When a
// screened configuration that differs from the leader only in this knob sits below it (the
// leader beat it), up continues the climb; when only ones above it exist, down does. With no
// such neighbour the step goes up first.
func (s *state) directions(knob KnobSpec, at float64) []int {
	below, above := s.neighbours(knob, at)
	if above && !below {
		return []int{-1, +1}
	}
	return []int{+1, -1}
}

// neighbours reports whether screened configurations that differ from the leader only in this
// knob exist below and above its value.
func (s *state) neighbours(knob KnobSpec, at float64) (below, above bool) {
	for _, config := range s.screened {
		if config == s.leader || !sameExcept(config.knobs, s.leader.knobs, knob.Name) {
			continue
		}
		if value, ok := s.currentValue(config, knob); ok {
			below = below || value < at
			above = above || value > at
		}
	}
	return below, above
}

func (s *state) whyDirection(knob KnobSpec, at float64, direction int) string {
	below, above := s.neighbours(knob, at)
	switch {
	case direction > 0 && below && !above:
		return "continuing up from a lower value the leader beat"
	case direction < 0 && above && !below:
		return "continuing down from a higher value the leader beat"
	case direction > 0 && !below && !above:
		return "no neighbour has run yet, so up first"
	default:
		return "the other direction has already run and lost"
	}
}

// sameExcept reports whether two knob sets differ in at most the named knob.
func sameExcept(a, b Knobs, name string) bool {
	return without(a, name).key() == without(b, name).key()
}

func without(knobs Knobs, name string) Knobs {
	copied := knobs.clone()
	delete(copied, name)
	return copied
}
