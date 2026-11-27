package plan

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// scaleOutTolerance is the linearity rule: per-replica RPS within
// 10% across replica counts verifies linear scale-out.
const scaleOutTolerance = 0.10

// scaleOutGroup is one configuration's models: the same target type, knobs,
// workload, hardware and arrival process, possibly at several replica counts.
type scaleOutGroup struct {
	models []spec.RunCapacityModel
}

// mergeScaleOut folds models of one configuration into one, planned from the
// member with the lowest per-replica SLO-max (the conservative choice), with
// its scale-out evidence set from all of them: verified when at least two
// replica counts were measured, every member found its limit, and their
// per-replica SLO-max agrees within 10%. Each merge reports a check; the
// returned map gives the run id of the model a check belongs to.
func mergeScaleOut(models []spec.RunCapacityModel, thresholds []threshold) ([]spec.RunCapacityModel, map[string]spec.PlanCheck) {
	var order []string
	groups := map[string]*scaleOutGroup{}
	for _, model := range models {
		key := configurationKey(model)
		if groups[key] == nil {
			groups[key] = &scaleOutGroup{}
			order = append(order, key)
		}
		groups[key].models = append(groups[key].models, model)
	}
	merged := make([]spec.RunCapacityModel, 0, len(order))
	checks := map[string]spec.PlanCheck{}
	for _, key := range order {
		folded, check, ok := groups[key].merge(thresholds)
		merged = append(merged, folded...)
		if ok {
			checks[folded[0].RunId] = check
		}
	}
	return merged, checks
}

type measuredModel struct {
	model    spec.RunCapacityModel
	capacity capacity
}

// merge returns the group's tiers: one for its measurable members, first,
// then each member that cannot be planned on its own, so it stays visible as
// a blocked tier with its reason.
func (g *scaleOutGroup) merge(thresholds []threshold) ([]spec.RunCapacityModel, spec.PlanCheck, bool) {
	if len(g.models) == 1 {
		return g.models, spec.PlanCheck{}, false
	}
	var measured []measuredModel
	var unmeasured []spec.RunCapacityModel
	for _, model := range g.models {
		if model.ReadyReplicas < 1 || model.Identity.QualityGrade == spec.QualityGradeUnusable {
			unmeasured = append(unmeasured, model)
			continue
		}
		found, err := sloMaxPerReplica(model, thresholds)
		if err != nil {
			unmeasured = append(unmeasured, model)
			continue
		}
		measured = append(measured, measuredModel{model, found})
	}
	if len(measured) < 2 {
		return g.models, spec.PlanCheck{}, false
	}
	sort.SliceStable(measured, func(i, j int) bool { return measured[i].capacity.rps < measured[j].capacity.rps })
	lowest := measured[0]
	counts := []int{}
	anyAtLeast := false
	for _, one := range measured {
		if !slices.Contains(counts, one.model.ReadyReplicas) {
			counts = append(counts, one.model.ReadyReplicas)
		}
		anyAtLeast = anyAtLeast || one.capacity.isAtLeast
	}
	sort.Ints(counts)
	highest := measured[len(measured)-1].capacity.rps
	spread := (highest - lowest.capacity.rps) / highest
	result := lowest.model
	result.ScaleOut = spec.RunCapacityModelScaleOut{ReplicaCounts: counts}
	evidence := describeMeasured(measured)
	check := spec.PlanCheck{Status: spec.PlanCheckStatusWarning}
	switch {
	case len(counts) < 2:
		check.Text = fmt.Sprintf("%d runs of one configuration, all at %d replica(s): planned with the lowest per-replica SLO-max (%s). Run at another replica count to verify scale-out.",
			len(measured), counts[0], evidence)
	case anyAtLeast:
		check.Text = fmt.Sprintf("Scale-out cannot be verified: a run never failed its SLOs, so its per-replica SLO-max is only a floor (%s). Extend its ladder.", evidence)
	case spread <= scaleOutTolerance:
		result.ScaleOut.IsVerified = true
		check.Status = spec.PlanCheckStatusOk
		check.Text = fmt.Sprintf("Linear scale-out verified: per-replica SLO-max within %.0f%% across %v replicas (%s).", 100*scaleOutTolerance, counts, evidence)
	default:
		check.Status = spec.PlanCheckStatusViolatesMeasured
		check.Text = fmt.Sprintf("Linear scale-out does not hold: per-replica SLO-max differs by %.0f%% across %v replicas (%s). Planned with the lowest; a shared bottleneck (upstream, node, network) may be limiting.",
			100*spread, counts, evidence)
	}
	return append([]spec.RunCapacityModel{result}, unmeasured...), check, true
}

func describeMeasured(measured []measuredModel) string {
	parts := make([]string, 0, len(measured))
	for _, one := range measured {
		floor := ""
		if one.capacity.isAtLeast {
			floor = "≥ "
		}
		parts = append(parts, fmt.Sprintf("%s%s RPS per replica at %d (run %s)", floor, derive.FormatNumber(roundTo(one.capacity.rps, 3)), one.model.ReadyReplicas, one.model.RunId))
	}
	return strings.Join(parts, "; ")
}

func roundTo(value float64, digits int) float64 {
	scale := math.Pow(10, float64(digits))
	return math.Round(value*scale) / scale
}

// configurationKey identifies a configuration apart from its replica count.
func configurationKey(model spec.RunCapacityModel) string {
	identity := model.Identity
	knobs, _ := json.Marshal(identity.Knobs) // map keys marshal sorted
	hardware := ""
	if identity.HardwareProfileId != nil {
		hardware = *identity.HardwareProfileId
	}
	return strings.Join([]string{string(identity.TargetType), string(knobs), identity.WorkloadHash, hardware, string(identity.ArrivalProcess)}, "\x00")
}
