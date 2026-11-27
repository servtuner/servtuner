package cli

import (
	"path/filepath"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/inferenceperf"
	"github.com/servtuner/servtuner/bench/internal/engine/loadgen"
	"github.com/servtuner/servtuner/bench/internal/engine/null"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// engineOverrideNull selects the null engine. The RunSpec schema has no null
// engine type, so it is offered through a hidden flag instead.
const engineOverrideNull = "null"

// newEngineRegistry registers every engine the binary has. The engine type of
// the RunSpec selects one (engineName); the null engine is reachable only
// through --engine-override.
func newEngineRegistry() *engine.Registry {
	registry := engine.NewRegistry()
	registry.Register(engineOverrideNull, func(inputs engine.Inputs) (engine.Engine, error) {
		return null.New(inputs.Now), nil
	})
	registry.Register(string(spec.EngineTypeLoadgen), func(inputs engine.Inputs) (engine.Engine, error) {
		credentials, err := inputs.Credentials()
		if err != nil {
			return nil, err
		}
		return loggingLoadgen{loadgen.New(loadgen.Options{
			RunID:          inputs.RunID,
			BearerToken:    credentials.BearerToken,
			ClientCertFile: credentials.CertFile,
			ClientKeyFile:  credentials.KeyFile,
			CAFile:         credentials.CAFile,
			ClientSink:     inputs.ClientSink,
			Now:            inputs.Now,
			ShardIndex:     inputs.ShardIndex,
			ShardCount:     inputs.ShardCount,
		})}, nil
	})
	registry.Register(string(spec.EngineTypeInferencePerf), func(inputs engine.Inputs) (engine.Engine, error) {
		credentials, err := inputs.Credentials()
		if err != nil {
			return nil, err
		}
		return inferenceperf.New(inferenceperf.Options{
			OutputDir:    filepath.Clean(inputs.OutputDir),
			CertFile:     credentials.CertFile,
			KeyFile:      credentials.KeyFile,
			CABundleFile: credentials.CABundleFile,
			NumWorkers:   inputs.NumWorkers,
		}), nil
	})
	return registry
}

// engineName is the registered name for a run: the override when given, else
// the RunSpec's engine type.
func engineName(override string, runSpec spec.RunSpec) string {
	if override != "" {
		return override
	}
	return string(runSpec.Engine.Type)
}

// loggingLoadgen exposes loadgen's counters to the pipeline's log.
type loggingLoadgen struct{ *loadgen.Engine }

// LogAttrs implements engine.StatsReporter.
func (e loggingLoadgen) LogAttrs() []any {
	stats := e.Stats()
	return []any{
		"warmup_requests", stats.WarmupRequests, "warmup_errors", stats.WarmupErrors, "cap_waits", stats.CapWaits,
		"unsent", stats.Unsent, "shed", stats.Shed, "row_queue_blocks", stats.RowQueueBlocks,
		"lost", stats.Lost, "abandoned", stats.Abandoned, "max_in_flight", e.MaxInFlight(),
	}
}
