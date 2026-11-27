package bifrost

import (
	"fmt"
	"slices"

	"github.com/goccy/go-yaml"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// RenderPatch writes patches/bifrost.yaml: the
// recommended concurrency and buffer size for each provider the run went
// through, in Bifrost's config.json shape. It is nil when there is nothing to
// change.
func RenderPatch(knobs map[string]float64, config Config, runSpec spec.RunSpec, runID string) ([]byte, error) {
	if len(knobs) == 0 {
		return nil, nil
	}
	sizing := map[string]int{}
	if concurrency, ok := knobs[target.KnobBifrostConcurrency]; ok {
		sizing["concurrency"] = int(concurrency)
	}
	if bufferSize, ok := knobs[target.KnobBifrostBufferSize]; ok {
		sizing["buffer_size"] = int(bufferSize)
	}
	providers := map[string]map[string]map[string]int{}
	for _, name := range providersOf(config, runSpec) {
		providers[name] = map[string]map[string]int{"concurrency_and_buffer_size": sizing}
	}
	body, err := yaml.Marshal(map[string]any{"providers": providers})
	if err != nil {
		return nil, fmt.Errorf("render patches/bifrost.yaml: %w", err)
	}
	header := fmt.Sprintf("# servtuner Bifrost patch: run %s. Merge into config.json (or the chart's provider values).\n"+
		"# Each value's derivation and caveats are in recommendations.json (rules B1, B2).\n", runID)
	return append([]byte(header), body...), nil
}

// providersOf picks the providers the measured traffic went through: those
// named like a RunSpec upstream, else the only provider, else every provider.
func providersOf(config Config, runSpec spec.RunSpec) []string {
	var names, matching []string
	for _, provider := range config.Providers {
		names = append(names, provider.Name)
		for _, upstream := range runSpec.Target.Upstreams {
			if upstream.Name == provider.Name {
				matching = append(matching, provider.Name)
			}
		}
	}
	if len(matching) > 0 {
		names = matching
	}
	slices.Sort(names)
	return slices.Compact(names)
}
