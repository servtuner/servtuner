package spec

import (
	"embed"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// presetsFS is a copy of schema/presets made by `just gen`, because go:embed
// cannot reach outside this module. It is generated, never edited.
//
//go:embed presets
var presetsFS embed.FS

const (
	presetsRoot = "presets"
	// presetSeparator joins a preset's name and version in workload.preset.
	presetSeparator = "@"
)

// ErrUnknownPreset marks a workload.preset that names no embedded preset, or no
// such version of it.
var ErrUnknownPreset = errors.New("unknown workload preset")

// ErrPresetEngine marks a preset that the RunSpec's engine cannot run.
var ErrPresetEngine = errors.New("workload preset does not support the engine")

// FormatPresetReference writes name and version as "name@version".
func FormatPresetReference(name string, version int) string {
	return name + presetSeparator + strconv.Itoa(version)
}

// SplitPresetReference splits "name@version". The schema pattern already
// guarantees the shape of a reference that came from a RunSpec.
func SplitPresetReference(reference string) (name string, version int, err error) {
	name, versionText, found := strings.Cut(reference, presetSeparator)
	if !found {
		return "", 0, fmt.Errorf("workload preset %q: want name@version", reference)
	}
	version, err = strconv.Atoi(versionText)
	if err != nil || version < 1 {
		return "", 0, fmt.Errorf("workload preset %q: version must be a positive integer", reference)
	}
	return name, version, nil
}

// WorkloadPresets returns every embedded preset, sorted by name then version.
// Each file is validated against the preset schema, and its name and version
// must match its file name.
func WorkloadPresets() ([]WorkloadPreset, error) {
	return loadCatalog(presetsFS, presetsRoot, SchemaWorkloadPreset,
		func(preset WorkloadPreset) (string, int) { return preset.Name, preset.Version })
}

// LookupWorkloadPreset finds one preset by name and version. The error lists
// what exists, so a typo is easy to fix.
func LookupWorkloadPreset(name string, version int) (WorkloadPreset, error) {
	presets, err := WorkloadPresets()
	if err != nil {
		return WorkloadPreset{}, err
	}
	var versions, names []string
	for _, preset := range presets {
		if preset.Name == name {
			if preset.Version == version {
				return preset, nil
			}
			versions = append(versions, strconv.Itoa(preset.Version))
		}
		if !slices.Contains(names, preset.Name) {
			names = append(names, preset.Name)
		}
	}
	if len(versions) > 0 {
		return WorkloadPreset{}, fmt.Errorf("%w: %s has no version %d (available: %s)", ErrUnknownPreset, name, version, strings.Join(versions, ", "))
	}
	return WorkloadPreset{}, fmt.Errorf("%w: %q (available: %s)", ErrUnknownPreset, name, strings.Join(names, ", "))
}

// ExpandPreset replaces workload.preset with the preset's workload, so that
// everything downstream sees one explicit workload: the dataset, conversation
// and api come from the preset, streaming and seed stay the RunSpec's, and
// workload_preset records which preset it was. A RunSpec without a preset is
// returned unchanged. The preset name never enters the workload hash.
func ExpandPreset(runSpec RunSpec) (RunSpec, error) {
	if runSpec.Workload.Preset == nil {
		return runSpec, nil
	}
	reference := string(*runSpec.Workload.Preset)
	name, version, err := SplitPresetReference(reference)
	if err != nil {
		return RunSpec{}, err
	}
	preset, err := LookupWorkloadPreset(name, version)
	if err != nil {
		return RunSpec{}, err
	}
	if !slices.Contains(preset.Engines, runSpec.Engine.Type) {
		return RunSpec{}, fmt.Errorf("%w: %s runs on %v, not %s", ErrPresetEngine, reference, preset.Engines, runSpec.Engine.Type)
	}
	presetAPI := preset.Workload.Api
	if presetAPI == "" {
		presetAPI = WorkloadApiChat
	}
	// The api default is written back by the generated types, so an api
	// beside a preset can only be checked here: it must be the preset's own.
	if runSpec.Workload.Api != "" && runSpec.Workload.Api != presetAPI {
		return RunSpec{}, fmt.Errorf("workload.api %s conflicts with preset %s, which uses %s: a preset cannot be partly overridden", runSpec.Workload.Api, reference, presetAPI)
	}
	dataset := preset.Workload.Dataset
	expanded := runSpec
	expanded.Workload.Preset = nil
	expanded.Workload.Dataset = &dataset
	expanded.Workload.Api = presetAPI
	expanded.Workload.Conversation = nil
	if preset.Workload.Conversation != nil {
		conversation := *preset.Workload.Conversation
		expanded.Workload.Conversation = &conversation
	}
	expanded.WorkloadPreset = &PresetRef{Name: name, Version: version}
	return expanded, nil
}
