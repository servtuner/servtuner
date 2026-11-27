package propose

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// Defaults of the search space; schema/json/search-space.schema.json lists them.
const (
	// DefaultNoiseFloor is the run-to-run noise: a challenger
	// must beat the leader by more than this to take over.
	DefaultNoiseFloor = 0.05
	// DefaultConfirmTop is how many configurations get the full ladder and repeats.
	DefaultConfirmTop = 3
	// DefaultConfirmRepeats is the repeats per confirmed configuration; three
	// give a Student t interval and a high-confidence comparison.
	DefaultConfirmRepeats = 3
)

// DefaultObjective is the objective ranked when the space names none.
const DefaultObjective = spec.ObjectiveNameSloRpsPerGpu

var (
	// defaultProbeRPS is the first ladder of a session, before any SLO-max is
	// known: geometric (doubling) from 1 to 128 requests per second, 8 rungs.
	defaultProbeRPS = []float64{1, 2, 4, 8, 16, 32, 64, 128}
	// defaultScreenFactors are the screening ladder's rungs as multiples of the
	// leader's SLO-max: 5 rungs straddling the knee.
	defaultScreenFactors = []float64{0.7, 0.85, 1.0, 1.15, 1.3}
	// defaultFullFactors are the confirmation ladder's rungs: 9 rungs, denser
	// near the knee.
	defaultFullFactors = []float64{0.5, 0.7, 0.85, 0.95, 1.0, 1.05, 1.15, 1.3, 1.5}
)

// SearchSpace is the search space (schema search-space.schema.json) with its
// defaults applied and its cross-field rules checked: the form the search
// reads.
type SearchSpace struct {
	Objective spec.ObjectiveName
	// NoiseFloor is a fraction.
	NoiseFloor float64
	// MinConcurrency is the concurrency the preset needs the KV cache to hold;
	// 0 means no check.
	MinConcurrency float64
	// Base knobs are set on every configuration and never tuned.
	Base    Knobs
	Knobs   []KnobSpec
	Ladder  LadderSpec
	Confirm ConfirmSpec
}

// KnobSpec is one tuned knob, as the schema describes it.
type KnobSpec struct{ spec.TuningKnobSpec }

// LadderSpec holds the ladders' shapes: the probe rates, and the multiples of
// the leader's SLO-max of the screening and confirmation ladders.
type LadderSpec struct {
	ProbeRPS, ScreenFactors, FullFactors []float64
}

// ConfirmSpec shapes the confirmation phase.
type ConfirmSpec struct{ Top, Repeats int }

// ParseSearchSpace reads a space given as YAML or JSON: it validates it
// against the schema, applies the defaults and checks the rules the schema
// cannot express.
func ParseSearchSpace(document []byte) (SearchSpace, error) {
	asJSON, err := spec.ToJSON(document)
	if err != nil {
		return SearchSpace{}, fmt.Errorf("read the search space: %w", err)
	}
	parsed, err := spec.Parse[spec.SearchSpace](spec.SchemaSearchSpace, asJSON)
	if err != nil {
		return SearchSpace{}, fmt.Errorf("invalid search space: %w", err)
	}
	space := resolve(parsed)
	if err := space.validate(); err != nil {
		return SearchSpace{}, err
	}
	return space, nil
}

// resolve applies the schema's documented defaults.
func resolve(parsed spec.SearchSpace) SearchSpace {
	space := SearchSpace{
		Objective: DefaultObjective, NoiseFloor: DefaultNoiseFloor, Base: Knobs(parsed.Base).clone(),
		Ladder:  LadderSpec{ProbeRPS: defaultProbeRPS, ScreenFactors: defaultScreenFactors, FullFactors: defaultFullFactors},
		Confirm: ConfirmSpec{Top: DefaultConfirmTop, Repeats: DefaultConfirmRepeats},
	}
	if parsed.Objective != nil {
		space.Objective = *parsed.Objective
	}
	if parsed.NoiseFloor != nil {
		space.NoiseFloor = *parsed.NoiseFloor
	}
	if parsed.MinConcurrency != nil {
		space.MinConcurrency = *parsed.MinConcurrency
	}
	for _, knob := range parsed.Knobs {
		space.Knobs = append(space.Knobs, KnobSpec{knob})
	}
	if ladder := parsed.Ladder; ladder != nil {
		space.Ladder.ProbeRPS = firstNonEmpty(ladder.ProbeRps, space.Ladder.ProbeRPS)
		space.Ladder.ScreenFactors = firstNonEmpty(ladder.ScreenFactors, space.Ladder.ScreenFactors)
		space.Ladder.FullFactors = firstNonEmpty(ladder.FullFactors, space.Ladder.FullFactors)
	}
	if confirm := parsed.Confirm; confirm != nil {
		if confirm.Top != nil {
			space.Confirm.Top = *confirm.Top
		}
		if confirm.Repeats != nil {
			space.Confirm.Repeats = *confirm.Repeats
		}
	}
	return space
}

func firstNonEmpty(given, fallback []float64) []float64 {
	if len(given) > 0 {
		return given
	}
	return fallback
}

func (s SearchSpace) validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	seen := map[string]bool{}
	for _, knob := range s.Knobs {
		for _, problem := range knob.problems() {
			add("knob %q: %s", knob.Name, problem)
		}
		if seen[knob.Name] {
			add("knob %q is listed twice", knob.Name)
		}
		seen[knob.Name] = true
		if _, isBase := s.Base[knob.Name]; isBase {
			add("knob %q is both in base and tuned", knob.Name)
		}
	}
	for name := range s.Base {
		if !target.IsVLLMKnob(name) {
			add("base knob %q is not a vLLM knob (target/knobs.go)", name)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New("invalid search space: " + strings.Join(problems, "; "))
}

func isWhole(value KnobValue) bool {
	number, ok := value.(float64)
	return ok && number == math.Trunc(number)
}

func (k KnobSpec) problems() []string {
	var problems []string
	if !target.IsVLLMKnob(k.Name) {
		problems = append(problems, "not a vLLM knob (target/knobs.go)")
	}
	if k.Tier == TierFeasibility && len(k.Values) == 0 {
		problems = append(problems, "a feasibility knob needs values: the boot probes enumerate them")
	}
	if target.IsWholeNumberKnob(k.Name) { // vLLM rejects 64.5 sequences
		candidates := append([]KnobValue{}, k.Values...)
		for _, bound := range []*float64{k.Min, k.Max} {
			if bound != nil {
				candidates = append(candidates, *bound)
			}
		}
		if k.Default != nil {
			candidates = append(candidates, k.Default)
		}
		for _, value := range candidates {
			if !isWhole(value) {
				problems = append(problems, fmt.Sprintf("%v is not a whole number", value))
			}
		}
	}
	hasBounds := k.Min != nil || k.Max != nil
	switch {
	case len(k.Values) == 0 && !hasBounds:
		problems = append(problems, "needs values or bounds")
	case len(k.Values) == 0 && !target.IsRangeTunedKnob(k.Name):
		problems = append(problems, "needs values: only max_num_seqs, max_num_batched_tokens and max_model_len take bounds")
	case len(k.Values) > 0 && hasBounds:
		problems = append(problems, "give values or bounds, not both")
	case hasBounds && (k.Min == nil || k.Max == nil || *k.Min > *k.Max):
		problems = append(problems, "bounds need min and max, with min not above max")
	}
	if target.IsRangeTunedKnob(k.Name) {
		for _, value := range k.Values {
			if _, ok := value.(float64); !ok {
				problems = append(problems, fmt.Sprintf("value %v is not a number", value))
			}
		}
	}
	return problems
}

// knob returns the space's entry for a knob name.
func (s SearchSpace) knob(name string) (KnobSpec, bool) {
	for _, knob := range s.Knobs {
		if knob.Name == name {
			return knob, true
		}
	}
	return KnobSpec{}, false
}

func (s SearchSpace) knobsOfTier(tier Tier) []KnobSpec {
	var knobs []KnobSpec
	for _, knob := range s.Knobs {
		if knob.Tier == tier {
			knobs = append(knobs, knob)
		}
	}
	return knobs
}

// orderedValues are the knob's values with its default first, the rest in
// the order listed.
func (k KnobSpec) orderedValues() []KnobValue {
	if k.Default == nil {
		return k.Values
	}
	ordered := []KnobValue{k.Default}
	for _, value := range k.Values {
		if !sameValue(value, k.Default) {
			ordered = append(ordered, value)
		}
	}
	return ordered
}

// step moves a numeric knob from current, up (+1) or down (-1). A target is
// where the advisor wants to land; a ceiling caps an upward move (C_kv).
// With listed values it picks an allowed value (the one nearest the target
// on the right side of current, or the adjacent one without a target); with
// bounds it moves to the target, or doubles or halves, clamped and rounded.
// It returns false when there is nowhere to go.
func (k KnobSpec) step(current float64, direction int, target, ceiling *float64) (float64, bool) {
	if len(k.Values) > 0 {
		return k.stepAmongValues(current, direction, target, ceiling)
	}
	desired := current * 2
	if direction < 0 {
		desired = current / 2
	}
	if target != nil {
		desired = *target
	}
	desired = math.Min(math.Max(desired, *k.Min), *k.Max)
	if ceiling != nil && direction > 0 {
		desired = math.Min(desired, math.Floor(*ceiling))
	}
	desired = math.Round(desired)
	if float64(direction)*(desired-current) <= 0 {
		return 0, false
	}
	return desired, true
}

func (k KnobSpec) stepAmongValues(current float64, direction int, target, ceiling *float64) (float64, bool) {
	var choices []float64
	for _, value := range k.Values {
		number := value.(float64) // validated: advisor-managed values are numbers
		if float64(direction)*(number-current) <= 0 {
			continue
		}
		if direction > 0 && ((target != nil && number > *target) || (ceiling != nil && number > *ceiling)) {
			continue
		}
		if direction < 0 && target != nil && number < *target {
			continue
		}
		choices = append(choices, number)
	}
	if len(choices) == 0 {
		return 0, false
	}
	sort.Float64s(choices)
	// Toward a target, the allowed value nearest it; with none, the adjacent one.
	nearest := choices[0]
	if (target != nil && direction > 0) || (target == nil && direction < 0) {
		nearest = choices[len(choices)-1]
	}
	return nearest, true
}
