// Package compare is the experiment comparison: it
// groups analysed runs into configurations (runs with identical declared
// knobs are repeats), estimates every objective per configuration, ranks
// them, and recommends the winner only when its lead is significant.
//
// It is pure computation over already-loaded runs; reading bundles is the
// caller's job (analyze.LoadComparisonRun). The same runs always give the same
// document: configurations are ordered by their first run id and the pooled
// bootstrap is seeded from the configuration's run ids.
//
// Rules worth knowing:
//
//   - Configurations are keyed by the RunSpec's declared knobs, the
//     experiment's intent. Whether the target really ran them is the quality
//     gate's knob check; a run it excludes from comparison is listed in
//     excluded_runs and takes no part.
//   - Throughput objectives come from each run's SLO-max and use the variance
//     across repeats (a Student t 95% interval on the mean). Latency
//     objectives pool the configuration's per-request records and bootstrap
//     the percentile.
//   - A configuration wins only if its interval does not overlap the
//     runner-up's. When either has a single sample there is no interval, and
//     the lead must exceed twice the 5% noise floor.
//   - Different workloads, target types, arrival processes, ladders or SLOs
//     make the runs not comparable: they are still ranked, for inspection,
//     but nothing wins and nothing is recommended.
package compare

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Run is one analysed run, as the comparison needs it.
type Run struct {
	ID       string
	Spec     spec.RunSpec
	Analysis spec.AnalysisResult
	// Model is the run's capacity_model.json; nil when the bundle has none, and
	// the objectives that need it are then unavailable.
	Model *spec.RunCapacityModel
	// TTFT returns the time to first token of every request the stage
	// scheduled, warmup excluded. It is called only for the stages a latency
	// objective reads.
	TTFT func(stageIndex int) ([]float64, error)
}

// Ladder tolerance: offered rates within this fraction are the same stage.
const ladderRelativeTolerance = 1e-6

// DefaultObjective is the comparison's default for a target type: RPS per GPU
// for model servers, RPS per core for routers, and SLO-max RPS otherwise.
func DefaultObjective(targetType spec.TargetType) spec.ObjectiveName {
	switch targetType {
	case spec.TargetTypeVllm, spec.TargetTypeSglang:
		return spec.ObjectiveNameSloRpsPerGpu
	case spec.TargetTypeBifrost, spec.TargetTypeLitellm:
		return spec.ObjectiveNameSloRpsPerCore
	default:
		return spec.ObjectiveNameSloMaxRps
	}
}

// configuration is a group of repeats while the comparison is built.
type configuration struct {
	id    string
	knobs spec.RunSpecKnobs
	runs  []Run
}

// Compare builds comparison.json from the runs. An objective of "" picks the
// default for the runs' target type.
func Compare(runs []Run, objective spec.ObjectiveName, analyzerVersion string) (spec.Comparison, error) {
	if len(runs) == 0 {
		return spec.Comparison{}, fmt.Errorf("no runs to compare")
	}
	sorted := append([]Run(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].ID == sorted[i-1].ID {
			return spec.Comparison{}, fmt.Errorf("run %s is given more than once", sorted[i].ID)
		}
	}
	result := spec.Comparison{
		AnalyzerVersion: analyzerVersion,
		ExcludedRuns:    []spec.ExcludedRun{},
		Configurations:  []spec.KnobConfiguration{},
		Rankings:        []spec.ObjectiveRanking{},
		VariedKnobs:     []string{},
	}
	var included []Run
	for _, run := range sorted {
		if run.Analysis.Quality.IsExcludedFromComparison {
			reason := fmt.Sprintf("quality grade %s: excluded from comparison by the quality gate", run.Analysis.Quality.Grade)
			result.ExcludedRuns = append(result.ExcludedRuns, spec.ExcludedRun{RunId: run.ID, Reason: reason})
			continue
		}
		included = append(included, run)
	}
	if objective == "" {
		objective = DefaultObjective(sorted[0].Spec.Target.Type)
	}
	result.Objective = objective
	result.Experiment = sharedExperiment(sorted)
	result.Comparability = comparability(sorted, included)
	if len(included) == 0 {
		result.Comparability.IsComparable = false
		return result, nil
	}
	configurations, err := group(included)
	if err != nil {
		return spec.Comparison{}, err
	}
	result.VariedKnobs = variedKnobs(configurations)
	result.IsFactorial = isFactorial(configurations, result.VariedKnobs)
	if len(result.VariedKnobs) >= 2 && !result.IsFactorial {
		result.Comparability.Issues = append(result.Comparability.Issues, issue(spec.ComparisonIssueCodeNotFactorial,
			"not every combination of %s was run: effects are not claimed to be independent", strings.Join(result.VariedKnobs, ", ")))
	}
	if len(configurations) < 2 {
		result.Comparability.Issues = append(result.Comparability.Issues, issue(spec.ComparisonIssueCodeTooFewConfigurations,
			"only one configuration: there is nothing to compare it with"))
	}
	reference := referenceStage(included)
	result.ReferenceStageIndex = reference
	estimates, err := estimateAll(configurations, reference)
	if err != nil {
		return spec.Comparison{}, err
	}
	for i, config := range configurations {
		result.Configurations = append(result.Configurations, spec.KnobConfiguration{
			Id: config.id, Knobs: spec.KnobConfigurationKnobs(config.knobs), RunIds: runIDs(config.runs),
			Objectives: estimates[i].objectives, ReferenceTtftP95Seconds: estimates[i].referenceTTFT,
		})
	}
	judge := judgement{
		comparable:   result.Comparability.IsComparable,
		minRepeats:   minRepeats(configurations),
		fullCoverage: !hasIssue(result.Comparability, spec.ComparisonIssueCodeCoverageIncomplete),
	}
	for _, name := range objectiveOrder(objective) {
		if ranking, ok := rank(result.Configurations, name, judge); ok {
			result.Rankings = append(result.Rankings, ranking)
		}
	}
	result.ParetoConfigurationIds = pareto(result.Configurations, objective)
	for i := range result.Configurations {
		result.Configurations[i].IsPareto = contains(result.ParetoConfigurationIds, result.Configurations[i].Id)
	}
	result.Recommendation = recommend(result, configurations)
	return result, nil
}

// ServerKnob is the pseudo-knob that names a run's model server when a
// comparison spans vLLM and SGLang: the server becomes a compared dimension,
// a varied knob like any other.
const ServerKnob = "server"

// crossServerTypes are the target types one comparison may mix: model servers
// that serve the same models on the same GPUs. Routers never mix.
var crossServerTypes = map[spec.TargetType]bool{spec.TargetTypeVllm: true, spec.TargetTypeSglang: true}

// isCrossServer reports whether the runs mix model servers that may be
// compared with each other (comparability decides whether they may here).
func isCrossServer(runs []Run) bool {
	types := map[spec.TargetType]bool{}
	for _, run := range runs {
		if !crossServerTypes[run.Spec.Target.Type] {
			return false
		}
		types[run.Spec.Target.Type] = true
	}
	return len(types) > 1
}

// declaredKnobs are a run's knobs, plus ServerKnob when the comparison mixes
// model servers.
func declaredKnobs(run Run, crossServer bool) spec.RunSpecKnobs {
	if !crossServer {
		return run.Spec.Knobs
	}
	knobs := spec.RunSpecKnobs{ServerKnob: string(run.Spec.Target.Type)}
	for name, value := range run.Spec.Knobs {
		knobs[name] = value
	}
	return knobs
}

// group puts runs with identical declared knobs into one configuration,
// ordered by each configuration's first run id. When the runs mix vLLM and
// SGLang, the server is part of the knobs, so the two never share one.
func group(runs []Run) ([]configuration, error) {
	byKey := map[string]int{}
	var configurations []configuration
	crossServer := isCrossServer(runs)
	for _, run := range runs {
		declared := declaredKnobs(run, crossServer)
		key, err := knobKey(declared)
		if err != nil {
			return nil, fmt.Errorf("run %s: %w", run.ID, err)
		}
		index, ok := byKey[key]
		if !ok {
			index = len(configurations)
			byKey[key] = index
			knobs := spec.RunSpecKnobs{}
			for name, value := range declared {
				knobs[name] = value
			}
			configurations = append(configurations, configuration{id: fmt.Sprintf("c%d", index+1), knobs: knobs})
		}
		configurations[index].runs = append(configurations[index].runs, run)
	}
	return configurations, nil
}

// knobKey is a canonical encoding of the knobs: encoding/json sorts map keys,
// and a decoded number is a float64 whether it was written 256 or 256.0.
func knobKey(knobs spec.RunSpecKnobs) (string, error) {
	if knobs == nil {
		knobs = spec.RunSpecKnobs{}
	}
	encoded, err := json.Marshal(knobs)
	if err != nil {
		return "", fmt.Errorf("encode knobs: %w", err)
	}
	return string(encoded), nil
}

func valueKey(value spec.KnobValue) string {
	encoded, _ := json.Marshal(value) // knob values are strings, numbers or booleans
	return string(encoded)
}

// variedKnobs are the knobs whose value (or presence) differs between
// configurations, sorted.
func variedKnobs(configurations []configuration) []string {
	names := map[string]bool{}
	for _, config := range configurations {
		for name := range config.knobs {
			names[name] = true
		}
	}
	varied := []string{}
	for name := range names {
		values := map[string]bool{}
		for _, config := range configurations {
			value, ok := config.knobs[name]
			if !ok {
				values["<absent>"] = true
				continue
			}
			values[valueKey(value)] = true
		}
		if len(values) > 1 {
			varied = append(varied, name)
		}
	}
	sort.Strings(varied)
	return varied
}

// isFactorial reports whether every combination of the varied knobs' values
// was run. One varied knob is trivially factorial; none is not a design.
func isFactorial(configurations []configuration, varied []string) bool {
	if len(varied) == 0 {
		return false
	}
	combinations := 1
	seen := map[string]bool{}
	for _, name := range varied {
		values := map[string]bool{}
		for _, config := range configurations {
			values[knobValueOrAbsent(config.knobs, name)] = true
		}
		combinations *= len(values)
	}
	for _, config := range configurations {
		var parts []string
		for _, name := range varied {
			parts = append(parts, knobValueOrAbsent(config.knobs, name))
		}
		seen[strings.Join(parts, "\x00")] = true
	}
	return len(seen) == combinations
}

func knobValueOrAbsent(knobs spec.RunSpecKnobs, name string) string {
	if value, ok := knobs[name]; ok {
		return valueKey(value)
	}
	return "<absent>"
}

// comparability checks that the runs measured the same thing. It looks at
// every run, excluded ones too, because an excluded run that measured
// something else is still a sign the experiment was not run as designed.
func comparability(all, included []Run) spec.Comparability {
	result := spec.Comparability{IsComparable: true, Issues: []spec.ComparisonIssue{}}
	if len(included) == 0 {
		return result
	}
	breaking := func(code spec.ComparisonIssueCode, what string, value func(Run) string) {
		if values := distinct(included, value); len(values) > 1 {
			result.IsComparable = false
			result.Issues = append(result.Issues, issue(code, "%s differ between runs: %s", what, strings.Join(values, "; ")))
		}
	}
	if isCrossServer(included) {
		// vLLM against SGLang: comparable only on the same model and hardware
		// (the workload and SLOs are checked below, as for any comparison).
		breaking(spec.ComparisonIssueCodeTargetTypeDiffers, "models (vLLM and SGLang runs must serve the same model)", func(run Run) string { return run.Spec.Target.Model })
		isUnknown := func(run Run) bool { return gpuSetup(run) == unknownHardware }
		if values := distinct(included, gpuSetup); len(values) > 1 || slices.ContainsFunc(included, isUnknown) {
			result.IsComparable = false
			result.Issues = append(result.Issues, issue(spec.ComparisonIssueCodeHardwareDiffers,
				"vLLM and SGLang runs must run on the same, known hardware (GPU model and count): %s", strings.Join(values, "; ")))
		}
	} else {
		breaking(spec.ComparisonIssueCodeTargetTypeDiffers, "target types", func(run Run) string { return string(run.Spec.Target.Type) })
	}
	breaking(spec.ComparisonIssueCodeWorkloadDiffers, "workload hashes", workloadHash)
	breaking(spec.ComparisonIssueCodeArrivalDiffers, "arrival processes", func(run Run) string { return string(run.Spec.Load.Pacing) })
	breaking(spec.ComparisonIssueCodeSlosDiffer, "SLOs", func(run Run) string { return canonical(run.Spec.Slos) })
	if !laddersMatch(included) {
		result.IsComparable = false
		result.Issues = append(result.Issues, issue(spec.ComparisonIssueCodeLadderDiffers,
			"the runs offered different load at some stage: stages cannot be compared by index"))
	}
	if values := distinct(included, hardware); len(values) > 1 && !isCrossServer(included) {
		result.Issues = append(result.Issues, issue(spec.ComparisonIssueCodeHardwareDiffers,
			"hardware profiles differ between runs: %s (comparable only if the hardware is what the experiment varies)", strings.Join(values, "; ")))
	}
	if values := distinct(all, experimentName); len(values) > 1 {
		result.Issues = append(result.Issues, issue(spec.ComparisonIssueCodeExperimentDiffers,
			"the runs belong to different experiments: %s", strings.Join(values, "; ")))
	}
	if missing := missingCatalogs(included); len(missing) > 0 {
		result.Issues = append(result.Issues, issue(spec.ComparisonIssueCodeCoverageIncomplete,
			"catalogs not collected in some runs: %s", strings.Join(missing, ", ")))
	}
	if result.IsComparable {
		if hash := workloadHash(included[0]); hash != "" {
			result.WorkloadHash = &hash
		}
		// A comparison of vLLM against SGLang has no one target type; the
		// server is a varied knob instead.
		if !isCrossServer(included) {
			targetType := included[0].Spec.Target.Type
			result.TargetType = &targetType
		}
		result.WorkloadPreset = sharedPreset(included)
	}
	return result
}

// sharedPreset is the workload preset every run named, or nil when any run
// named none or another one.
func sharedPreset(runs []Run) *spec.PresetRef {
	first := runs[0].Spec.WorkloadPreset
	if first == nil {
		return nil
	}
	for _, run := range runs[1:] {
		if preset := run.Spec.WorkloadPreset; preset == nil || *preset != *first {
			return nil
		}
	}
	shared := *first
	return &shared
}

func workloadHash(run Run) string {
	if run.Model == nil {
		return ""
	}
	return run.Model.Identity.WorkloadHash
}

// unknownHardware is a run without a hardware profile.
const unknownHardware = "unknown"

// gpuSetup is what a vLLM-against-SGLang comparison needs to match: the GPU model and
// how many one replica uses, from a profile captured on Kubernetes or declared for
// a target outside it. Driver, CUDA, CPU and instance type may differ. Model names
// are compared without case or punctuation, so a node label (NVIDIA-A100-SXM4-80GB)
// matches the name nvidia-smi reports (NVIDIA A100-SXM4-80GB).
func gpuSetup(run Run) string {
	profile := run.Analysis.HardwareProfile
	if profile == nil || profile.Accelerator == acceleratorUnknown {
		return unknownHardware
	}
	count := 0
	if profile.GpuCountPerPod != nil {
		count = *profile.GpuCountPerPod
	}
	return fmt.Sprintf("%d x %s", count, normalizedAccelerator(profile.Accelerator))
}

// acceleratorUnknown is a profile's accelerator for GPUs no node label names.
const acceleratorUnknown = "unknown"

func normalizedAccelerator(name string) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, "-")
}

func hardware(run Run) string {
	if run.Analysis.HardwareProfileId == nil {
		return unknownHardware
	}
	return *run.Analysis.HardwareProfileId
}

func experimentName(run Run) string {
	if run.Spec.Experiment == nil {
		return "(none)"
	}
	return *run.Spec.Experiment
}

func sharedExperiment(runs []Run) *string {
	if values := distinct(runs, experimentName); len(values) == 1 && runs[0].Spec.Experiment != nil {
		name := *runs[0].Spec.Experiment
		return &name
	}
	return nil
}

func canonical(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("unencodable: %v", err)
	}
	return string(encoded)
}

// distinct returns the different values of value over runs, each with the
// first run id that has it, in run order.
func distinct(runs []Run, value func(Run) string) []string {
	seen := map[string]bool{}
	var values []string
	for _, run := range runs {
		v := value(run)
		if seen[v] {
			continue
		}
		seen[v] = true
		label := v
		if label == "" {
			label = "(none)"
		}
		values = append(values, fmt.Sprintf("%s (%s)", label, run.ID))
	}
	return values
}

// laddersMatch reports whether every run has the same stages offering the
// same RPS at each index.
func laddersMatch(runs []Run) bool {
	first := runs[0].Analysis.Stages
	for _, run := range runs[1:] {
		stages := run.Analysis.Stages
		if len(stages) != len(first) {
			return false
		}
		for i := range stages {
			a, b := stages[i].OfferedRps, first[i].OfferedRps
			if stages[i].StageIndex != first[i].StageIndex || math.Abs(a-b) > ladderRelativeTolerance*math.Max(math.Abs(a), math.Abs(b)) {
				return false
			}
		}
	}
	return true
}

func missingCatalogs(runs []Run) []string {
	missing := map[string]bool{}
	for _, run := range runs {
		if run.Analysis.Quality.Coverage == nil {
			continue
		}
		for _, catalog := range run.Analysis.Quality.Coverage.Catalogs {
			if !catalog.IsCollected {
				missing[string(catalog.Catalog)] = true
			}
		}
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// referenceStage is the highest stage index that is valid and not excluded in
// every run, or nil when there is none.
func referenceStage(runs []Run) *int {
	counts := map[int]int{}
	for _, run := range runs {
		for _, stage := range run.Analysis.Quality.Stages {
			if stage.IsValid && !stage.IsExcluded {
				counts[stage.StageIndex]++
			}
		}
	}
	best := -1
	for index, count := range counts {
		if count == len(runs) && index > best {
			best = index
		}
	}
	if best < 0 {
		return nil
	}
	return &best
}

func issue(code spec.ComparisonIssueCode, format string, args ...any) spec.ComparisonIssue {
	return spec.ComparisonIssue{Code: code, Message: fmt.Sprintf(format, args...)}
}

func hasIssue(comparability spec.Comparability, code spec.ComparisonIssueCode) bool {
	for _, found := range comparability.Issues {
		if found.Code == code {
			return true
		}
	}
	return false
}

func runIDs(runs []Run) []string {
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	return ids
}

func minRepeats(configurations []configuration) int {
	least := math.MaxInt
	for _, config := range configurations {
		least = min(least, len(config.runs))
	}
	return least
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
