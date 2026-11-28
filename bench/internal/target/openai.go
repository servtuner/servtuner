package target

import (
	"context"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// modelsAdapter is the adapter of a target whose only self-description is the
// OpenAI model list: any OpenAI-compatible server, and LiteLLM (litellm.go adds
// its command-line knobs). Their version and configuration endpoints are not
// read; the workload's image tag in the snapshot records the version.
type modelsAdapter struct {
	kubeReader
	catalogs []string
}

func newOpenAICompatibleAdapter() TargetAdapter {
	return modelsAdapter{kubeReader{spec.TargetTypeOpenaiCompatible}, []string{CatalogKubernetes}}
}

// newSGLangAdapter is SGLang's adapter until its own exists: the model list,
// the Kubernetes snapshot and the sglang catalog. The SGLang adapter (server
// info and knobs) replaces it.
func newSGLangAdapter() TargetAdapter {
	return modelsAdapter{kubeReader{spec.TargetTypeSglang}, []string{CatalogSGLang, CatalogKubernetes}}
}

// Info implements TargetAdapter.
func (a modelsAdapter) Info(ctx context.Context, endpoint Endpoint) (Info, error) {
	info := Info{Knobs: Knobs{}}
	err := fetchModels(ctx, endpoint, &info)
	return info, err
}

// KnobsFromWorkload implements TargetAdapter. No knob of these target types is
// read from container args.
func (modelsAdapter) KnobsFromWorkload(spec.Workload_2) Knobs { return Knobs{} }

// DefaultCatalogs implements TargetAdapter.
func (a modelsAdapter) DefaultCatalogs() []string { return append([]string(nil), a.catalogs...) }
