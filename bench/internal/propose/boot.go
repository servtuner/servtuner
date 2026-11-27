package propose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// BootResults is the --infeasible file (schema boot-results.schema.json): the
// boot probes made so far.
type BootResults struct{ spec.BootResults }

// ParseBootResults reads the file as YAML or JSON and validates it.
func ParseBootResults(document []byte) (BootResults, error) {
	asJSON, err := spec.ToJSON(document)
	if err != nil {
		return BootResults{}, fmt.Errorf("read the boot results: %w", err)
	}
	parsed, err := spec.Parse[spec.BootResults](spec.SchemaBootResults, asJSON)
	if err != nil {
		return BootResults{}, fmt.Errorf("invalid boot results: %w", err)
	}
	for i, result := range parsed.Results {
		if len(result.Knobs) == 0 {
			return BootResults{}, fmt.Errorf("boot result %d has no knobs: it would match every configuration", i+1)
		}
	}
	return BootResults{parsed}, nil
}

// infeasibleReason is why a boot probe rules the knob set out: a probe of a
// subset of its knobs failed to boot.
func (b BootResults) infeasibleReason(knobs Knobs) (string, bool) {
	for _, result := range b.Results {
		if result.Outcome == spec.TuningBootOutcomeInfeasible && knobs.contains(Knobs(result.Knobs)) {
			probe := Knobs(result.Knobs).key()
			if result.Reason == nil || *result.Reason == "" {
				return "a boot probe of " + probe + " failed", true
			}
			return "a boot probe of " + probe + " failed: " + *result.Reason, true
		}
	}
	return "", false
}

// ckv is the smallest c_kv of the feasible probes whose knobs the set
// contains, or nil when none reported one.
func (b BootResults) ckv(knobs Knobs) *float64 {
	var least *float64
	for _, result := range b.Results {
		if result.Outcome == spec.TuningBootOutcomeFeasible && result.CKv != nil && knobs.contains(Knobs(result.Knobs)) {
			value := *result.CKv
			if least == nil || value < *least {
				least = &value
			}
		}
	}
	return least
}

// probeOf is the boot probe that covers a feasibility combination:
// its knobs are contained in the combination and name every feasibility knob.
func (b BootResults) probeOf(combination Knobs, feasibilityNames []string) (spec.TuningBootResult, bool) {
	for _, result := range b.Results {
		if !combination.contains(Knobs(result.Knobs)) {
			continue
		}
		named := true
		for _, name := range feasibilityNames {
			if _, ok := result.Knobs[name]; !ok {
				named = false
			}
		}
		if named {
			return result, true
		}
	}
	return spec.TuningBootResult{}, false
}

// capacityOnlyKnobs change nothing but the KV cache's size. On vLLM's extensible KV cache
// (0.34+), gpu_memory_utilization is only a cap: above a host-specific point the memory
// free after warmup sizes the cache, so higher values boot the same configuration.
var capacityOnlyKnobs = map[string]bool{"vllm.gpu_memory_utilization": true}

// sameCapacity are feasibility combinations that booted with the same KV cache and differ
// only in capacity-only knobs: one configuration under several names. The kept one has the
// lowest capacity-only values, so it asks the least of the GPU for the same result.
type sameCapacity struct {
	kept    Knobs
	blocks  int
	bound   string
	dropped []Knobs
}

// foldSameCapacity groups the probed, feasible combinations by their knobs other than the
// capacity-only ones and by num_gpu_blocks. Combinations without a block count stay apart.
func (b BootResults) foldSameCapacity(combinations []Knobs, feasibilityNames []string) []sameCapacity {
	groups := map[string]*sameCapacity{}
	var order []string
	for _, combination := range combinations {
		if _, infeasible := b.infeasibleReason(combination); infeasible {
			continue
		}
		result, probed := b.probeOf(combination, feasibilityNames)
		if !probed || result.Outcome != spec.TuningBootOutcomeFeasible || result.NumGpuBlocks == nil {
			continue
		}
		rest := Knobs{}
		for name, value := range combination {
			if !capacityOnlyKnobs[name] {
				rest[name] = value
			}
		}
		key := fmt.Sprintf("%s|%d", rest.key(), *result.NumGpuBlocks)
		group, seen := groups[key]
		if !seen {
			bound := ""
			if result.KvSizing != nil {
				bound = string(result.KvSizing.Bound)
			}
			groups[key] = &sameCapacity{kept: combination, blocks: *result.NumGpuBlocks, bound: bound}
			order = append(order, key)
			continue
		}
		if lowerCapacityKnobs(combination, group.kept) {
			group.dropped = append(group.dropped, group.kept)
			group.kept = combination
		} else {
			group.dropped = append(group.dropped, combination)
		}
	}
	var folds []sameCapacity
	for _, key := range order {
		if group := groups[key]; len(group.dropped) > 0 {
			folds = append(folds, *group)
		}
	}
	return folds
}

// lowerCapacityKnobs reports whether a's capacity-only knobs are numerically below b's,
// compared knob by knob in name order.
func lowerCapacityKnobs(a, b Knobs) bool {
	names := make([]string, 0, len(capacityOnlyKnobs))
	for name := range capacityOnlyKnobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		left, okLeft := a.number(name)
		right, okRight := b.number(name)
		if okLeft && okRight && left != right {
			return left < right
		}
	}
	return false
}

// capacityNames lists a combination's capacity-only knobs as name=value, for messages.
func capacityNames(knobs Knobs) string {
	var parts []string
	for name, value := range knobs {
		if capacityOnlyKnobs[name] {
			parts = append(parts, fmt.Sprintf("%s=%v", name, value))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
