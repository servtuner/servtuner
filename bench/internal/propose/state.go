package propose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// configuration is every run of one declared knob set.
type configuration struct {
	id    string
	knobs Knobs
	// screening are the runs labelled with the probe or screen ladder; full
	// the runs labelled with the confirmation ladder (LabelLadder).
	screening, full []compare.Run
	// excluded: the quality gate excluded every screening run from comparison.
	excluded bool
	// contradictedFull are confirmation runs whose declared knobs the target
	// contradicted: the configuration cannot be confirmed as declared.
	contradictedFull []string
	// hasSLOMax: every screening run found a stage that met the SLOs. A
	// configuration that never did scores 0.
	hasSLOMax bool
	// score is the primary objective over the screening runs; sloMaxRPS the
	// SLO-max in requests per second, which places the next ladder, and
	// lowerBound says the ladder never saw the knee.
	score, sloMaxRPS float64
	lowerBound       bool
}

func (c *configuration) runIDs() []string {
	ids := make([]string, 0, len(c.screening)+len(c.full))
	for _, run := range c.screening {
		ids = append(ids, run.ID)
	}
	for _, run := range c.full {
		ids = append(ids, run.ID)
	}
	sort.Strings(ids)
	return ids
}

func screeningIDs(c *configuration) []string {
	ids := make([]string, len(c.screening))
	for i, run := range c.screening {
		ids[i] = run.ID
	}
	return ids
}

// state is what the bundles say so far.
type state struct {
	space SearchSpace
	boots BootResults
	// screened are the configurations with screening runs, in the order of
	// their first run (a ULID, so the order they ran in).
	screened []*configuration
	byKey    map[string]*configuration
	// leader is the configuration to beat: replaying the screening
	// configurations in run order, a challenger takes over only by more than
	// the noise floor, so noise does not move the leader.
	leader *configuration
	advice map[string][]Advice
	// notes are findings about the input, for the proposal's notes.
	notes []string
	// sameCapacity are the dropped feasibility combinations: each booted with the same KV
	// cache as a kept one and differs only in capacity-only knobs.
	sameCapacity []foldedCombination
	// unjudged is true when runs exist but no stage of any had an SLO result:
	// every percentile lacked the samples Rule A asks for, so "no SLO met"
	// would blame the target for a ladder too short or too slow.
	unjudged bool
}

// noSLOJudged is true when there are runs and none of their stages judged an SLO.
func noSLOJudged(evidence []Evidence) bool {
	for _, item := range evidence {
		for _, stage := range item.Run.Analysis.Stages {
			if len(stage.SloResults) > 0 {
				return false
			}
		}
	}
	return len(evidence) > 0
}

func newState(space SearchSpace, boots BootResults, evidence []Evidence) (*state, error) {
	s := &state{space: space, boots: boots, byKey: map[string]*configuration{}, advice: map[string][]Advice{}, unjudged: noSLOJudged(evidence)}
	s.foldSameCapacity()
	sorted := append([]Evidence(nil), evidence...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Run.ID < sorted[j].Run.ID })
	if err := checkComparable(sorted); err != nil {
		return nil, err
	}
	var created []*configuration
	configOf := map[string]*configuration{}
	for i, item := range sorted {
		if i > 0 && item.Run.ID == sorted[i-1].Run.ID {
			return nil, invalidInput("run %s is given more than once", item.Run.ID)
		}
		ladder, err := ladderOf(item.Run)
		if err != nil {
			return nil, err
		}
		knobs := fromSpecKnobs(item.Run.Spec.Knobs)
		config, ok := s.byKey[knobs.key()]
		if !ok {
			config = &configuration{id: knobs.id(), knobs: knobs}
			s.byKey[knobs.key()] = config
			created = append(created, config)
		}
		if contradicted := contradictedKnobs(item.Run); len(contradicted) > 0 {
			// The configuration was not what ran: it stays tried (never proposed
			// again as is), but the run is neither ranked nor read for advice.
			s.notes = append(s.notes, fmt.Sprintf("run %s is excluded from ranking and advice: %s", item.Run.ID, strings.Join(contradicted, "; ")))
			if ladder == LadderFull {
				config.contradictedFull = append(config.contradictedFull, item.Run.ID)
			}
			continue
		}
		if ladder == LadderFull {
			config.full = append(config.full, item.Run)
		} else {
			config.screening = append(config.screening, item.Run)
		}
		configOf[item.Run.ID] = config
		s.advice[item.Run.ID] = item.Advice
	}
	for _, config := range created {
		if len(config.screening) > 0 {
			s.screened = append(s.screened, config)
		}
	}
	if err := s.score(configOf); err != nil {
		return nil, err
	}
	s.chooseLeader()
	return s, nil
}

// contradictedKnobs lists the declared knobs the analysis found contradicted by
// the value the target reported (the knob check). Unverified
// knobs are left alone: vLLM does not expose every knob.
func contradictedKnobs(run compare.Run) []string {
	var contradicted []string
	for _, check := range run.Analysis.Quality.Knobs {
		if check.Status == spec.KnobStatusMismatch {
			actual := "unknown"
			if check.Actual != nil {
				actual = *check.Actual
			}
			contradicted = append(contradicted, fmt.Sprintf("%s declared %s, captured %s", check.Name, check.Declared, actual))
		}
	}
	return contradicted
}

// score fills each screening configuration's score from the comparison. The
// runs have different ladders, so the comparison calls them not comparable
// and names no winner; its per-configuration estimates are still computed,
// and those are what ranks here.
func (s *state) score(configOf map[string]*configuration) error {
	var runs []compare.Run
	for _, config := range s.screened {
		config.hasSLOMax = true
		for _, run := range config.screening {
			config.hasSLOMax = config.hasSLOMax && run.Analysis.Capacity.SloMax != nil
		}
		if config.hasSLOMax {
			runs = append(runs, config.screening...)
		}
	}
	if len(runs) == 0 {
		return nil
	}
	result, err := compare.Compare(runs, s.space.Objective, analyze.AnalyzerVersion)
	if err != nil {
		return err
	}
	scored := map[*configuration]bool{}
	for _, entry := range result.Configurations {
		config := configOf[entry.RunIds[0]]
		scored[config] = true
		objective, sloMax := estimateOf(entry, s.space.Objective), estimateOf(entry, spec.ObjectiveNameSloMaxRps)
		if objective.Value == nil {
			reason := "no value"
			if objective.UnavailableReason != nil {
				reason = *objective.UnavailableReason
			}
			return fmt.Errorf("objective %s is unavailable for configuration %s: %s", s.space.Objective, config.id, reason)
		}
		config.score = *objective.Value
		config.sloMaxRPS, config.lowerBound = *sloMax.Value, sloMax.IsLowerBound
	}
	for _, config := range s.screened {
		config.excluded = config.hasSLOMax && !scored[config]
	}
	return nil
}

func estimateOf(entry spec.KnobConfiguration, name spec.ObjectiveName) spec.ObjectiveInterval {
	for _, estimate := range entry.Objectives {
		if estimate.Objective == name {
			return estimate.Estimate
		}
	}
	return spec.ObjectiveInterval{}
}

func (s *state) chooseLeader() {
	for _, config := range s.screened {
		if config.excluded {
			continue
		}
		if s.leader == nil || config.score > s.leader.score*(1+s.space.NoiseFloor) {
			s.leader = config
		}
	}
}

// tried reports whether the knob set has a run, so it is never proposed again.
func (s *state) tried(knobs Knobs) bool {
	_, ok := s.byKey[knobs.key()]
	return ok
}

// adviceOf is the advice on a configuration: its screening runs' recommendations
// in run order, the first of each rule and knob.
func (s *state) adviceOf(config *configuration) []Advice {
	var advice []Advice
	seen := map[string]bool{}
	for _, run := range config.screening {
		for _, item := range s.advice[run.ID] {
			if key := item.Rule + "/" + item.Knob; !seen[key] {
				seen[key] = true
				advice = append(advice, item)
			}
		}
	}
	sort.SliceStable(advice, func(i, j int) bool { return advice[i].Rule < advice[j].Rule })
	return advice
}

// foldSameCapacity records the feasibility combinations that are one configuration under
// several names, and a note for each group.
func (s *state) foldSameCapacity() {
	names := s.feasibilityNames()
	if len(names) == 0 {
		return
	}
	for _, group := range s.boots.foldSameCapacity(s.feasibilityCombinations(), names) {
		sizedBy := ""
		if group.bound == string(spec.TuningKvSizingBoundMemory) {
			sizedBy = ", sized by the memory free after warmup"
		}
		var dropped []string
		for _, knobs := range group.dropped {
			dropped = append(dropped, capacityNames(knobs))
			s.sameCapacity = append(s.sameCapacity, foldedCombination{knobs: knobs, kept: group.kept, reason: fmt.Sprintf("it booted with the same KV cache as %s (%d blocks%s), so it is the same configuration", capacityNames(group.kept), group.blocks, sizedBy)})
		}
		sort.Strings(dropped)
		advice := ""
		if group.bound == string(spec.TuningKvSizingBoundMemory) {
			// vLLM 0.34+ defaults to 1.0 and says to leave it unset unless something it cannot
			// measure shares the GPU.
			advice = "; vLLM's default (1.0, the flag left out) sizes the cache the same way, so leave --gpu-memory-utilization out unless something else shares this GPU"
		}
		s.notes = append(s.notes, fmt.Sprintf("%s booted with the same KV cache as %s (%d blocks%s): treated as one configuration, keeping the lower value; above that point gpu_memory_utilization has no effect on this host%s",
			strings.Join(dropped, "; "), capacityNames(group.kept), group.blocks, sizedBy, advice))
	}
}

// foldedCombination is a feasibility combination dropped as the same configuration as another.
type foldedCombination struct {
	knobs, kept Knobs
	reason      string
}

// sameCapacityReason is why a knob set is not run when its feasibility knobs are a dropped
// combination's.
func (s *state) sameCapacityReason(knobs Knobs) (string, bool) {
	if folded, ok := s.foldedInto(knobs); ok {
		return folded.reason, true
	}
	return "", false
}

func (s *state) foldedInto(knobs Knobs) (foldedCombination, bool) {
	for _, folded := range s.sameCapacity {
		if knobs.contains(folded.knobs) {
			return folded, true
		}
	}
	return foldedCombination{}, false
}
