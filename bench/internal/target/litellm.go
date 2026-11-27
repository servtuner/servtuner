package target

import (
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// LiteLLM knobs (verified on the v1.108.3 image): the worker count is the CLI flag --num_workers, or the env
// var NUM_WORKERS click reads it from; it defaults to 1. The router settings
// (num_retries, allowed_fails, cooldown_time, routing_strategy) live in the
// proxy's config.yaml, which bench does not capture, so those knobs stay
// unverified.
const (
	KnobLiteLLMWorkers = "litellm.num_workers"
	litellmWorkersFlag = "--num_workers"
	litellmWorkersEnv  = "NUM_WORKERS"
)

type litellmAdapter struct{ modelsAdapter }

func newLiteLLMAdapter() TargetAdapter {
	return litellmAdapter{modelsAdapter{kubeReader{spec.TargetTypeLitellm}, []string{CatalogLiteLLM, CatalogKubernetes}}}
}

// KnobsFromWorkload implements TargetAdapter: the worker count every pod runs.
// The flag wins over the env var, as click resolves it.
func (litellmAdapter) KnobsFromWorkload(workload spec.Workload_2) Knobs {
	var perPod []Knobs
	for _, pod := range workload.Pods {
		knobs := Knobs{}
		for _, container := range pod.Containers {
			if value, ok := envValue(container.Env, litellmWorkersEnv); ok {
				if parsed, ok := parseKnobValue(value, knobNumber); ok {
					knobs[KnobLiteLLMWorkers] = parsed
				}
			}
			if text, ok := flagValue(container.Args, []string{litellmWorkersFlag}); ok {
				if parsed, ok := parseKnobValue(text, knobNumber); ok {
					knobs[KnobLiteLLMWorkers] = parsed
				}
			}
		}
		perPod = append(perPod, knobs)
	}
	return requireOnEvery(perPod)
}

// envValue finds an unredacted env var in a snapshot container: an EnvEntry
// while capturing, a decoded JSON object when read back from the bundle.
func envValue(entries []spec.EnvVar, name string) (string, bool) {
	for _, entry := range entries {
		switch typed := entry.(type) {
		case EnvEntry:
			if typed.Name == name && !typed.IsRedacted && typed.Value != nil {
				return *typed.Value, true
			}
		case map[string]any:
			value, isString := typed["value"].(string)
			redacted, _ := typed["is_redacted"].(bool)
			if typed["name"] == name && isString && !redacted {
				return value, true
			}
		}
	}
	return "", false
}
