package inferenceperf

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Pinned engine version. Pin exactly: inference-perf is
// 0.x and changes its output format between releases (bench/CLAUDE.md).
const Version = "0.7.0"

// engineOptionTokenizer is the one engine.options key with special meaning: the
// tokenizer name or local path inference-perf loads (see the package doc).
const engineOptionTokenizer = "tokenizer"

// versionPinned is the RunSpec value for "the version in the bench image".
const versionPinned = "pinned"

// Sizes of the token-length window around the mean, in standard deviations.
// inference-perf clamps lengths to [min, max], and the RunSpec distribution has
// no bounds, so the window is wide enough to clip well under 0.01% of samples.
const lengthWindowStandardDeviations = 4

// serverPathSuffix is the API prefix inference-perf appends itself: its
// base_url is the endpoint without /v1.
const serverPathSuffix = "/v1"

// nativeConfig is the YAML document inference-perf reads (config/config.py in
// inference-perf 0.7.0). It is rejected if it has a key the CLI does not know,
// so every field here exists in that release.
type nativeConfig struct {
	API       apiSection       `yaml:"api"`
	Data      dataSection      `yaml:"data"`
	Load      loadSection      `yaml:"load"`
	Server    serverSection    `yaml:"server"`
	Tokenizer tokenizerSection `yaml:"tokenizer"`
	Report    reportSection    `yaml:"report"`
	Storage   storageSection   `yaml:"storage"`
}

type apiSection struct {
	Type      string `yaml:"type"`
	Streaming bool   `yaml:"streaming"`
}

// dataSection is the random generator's lengths, or the shared_prefix
// generator's settings; the other generator's fields are left out (the CLI's
// config is strict).
type dataSection struct {
	Type               string               `yaml:"type"`
	InputDistribution  *distribution        `yaml:"input_distribution,omitempty"`
	OutputDistribution *distribution        `yaml:"output_distribution,omitempty"`
	SharedPrefix       *sharedPrefixSection `yaml:"shared_prefix,omitempty"`
}

// sharedPrefixSection is inference-perf 0.7.0's SharedPrefix config
// (config/datagen/config.py). seed stays unset, so each run draws fresh
// prefixes: like loadgen's run salt, no run is served from another run's
// prefix cache.
type sharedPrefixSection struct {
	NumGroups          int          `yaml:"num_groups"`
	NumPromptsPerGroup int          `yaml:"num_prompts_per_group"`
	SystemPromptLen    int          `yaml:"system_prompt_len"`
	QuestionLen        distribution `yaml:"question_len"`
	OutputLen          distribution `yaml:"output_len"`
}

// promptHeadroom over-provisions the shared-prefix prompts: inference-perf
// pre-generates them and cycles through them, so running out would repeat
// whole prompts and add full-prompt cache hits. Poisson arrivals can exceed
// the planned count, hence the extra 10%.
const promptHeadroom = 1.1

type distribution struct {
	Min    int     `yaml:"min"`
	Max    int     `yaml:"max"`
	Mean   float64 `yaml:"mean"`
	StdDev float64 `yaml:"std_dev"`
	Type   string  `yaml:"type"`
}

type loadSection struct {
	Type       string       `yaml:"type"`
	BaseSeed   int          `yaml:"base_seed"`
	Interval   float64      `yaml:"interval"`
	NumWorkers int          `yaml:"num_workers,omitempty"`
	Stages     []stageEntry `yaml:"stages"`
}

type stageEntry struct {
	Rate     float64 `yaml:"rate"`
	Duration int     `yaml:"duration"`
}

type serverSection struct {
	Type      string `yaml:"type"`
	ModelName string `yaml:"model_name"`
	BaseURL   string `yaml:"base_url"`
	CertPath  string `yaml:"cert_path,omitempty"`
	KeyPath   string `yaml:"key_path,omitempty"`
}

type tokenizerSection struct {
	PretrainedModelNameOrPath string `yaml:"pretrained_model_name_or_path"`
}

type reportSection struct {
	RequestLifecycle requestLifecycleSection `yaml:"request_lifecycle"`
	// Prometheus is always null: the harness collects server metrics itself.
	Prometheus *struct{} `yaml:"prometheus"`
}

type requestLifecycleSection struct {
	Summary          bool             `yaml:"summary"`
	PerStage         bool             `yaml:"per_stage"`
	PerRequest       bool             `yaml:"per_request"`
	PerAdapter       bool             `yaml:"per_adapter"`
	PerRequestFields perRequestFields `yaml:"per_request_fields"`
}

// perRequestFields keeps the per-request report small and free of prompts:
// the computed metrics are what normalize reads, the info block carries token
// counts and timestamps, and the payload and chunk bodies are not needed.
type perRequestFields struct {
	Request         bool `yaml:"request"`
	Response        bool `yaml:"response"`
	Info            bool `yaml:"info"`
	ResponseChunks  bool `yaml:"response_chunks"`
	ComputedMetrics bool `yaml:"computed_metrics"`
}

type storageSection struct {
	LocalStorage localStorage `yaml:"local_storage"`
}

type localStorage struct {
	Path string `yaml:"path"`
}

// configInputs is everything renderConfig needs besides the RunSpec.
type configInputs struct {
	// Phases are the planned phases, warmup first. Their index is the
	// inference-perf stage id (normalize.go relies on it).
	Phases []engine.Phase
	// OutputDir is where inference-perf writes its reports.
	OutputDir string
	// CertFile and KeyFile are set in mtls mode only.
	CertFile, KeyFile string
	// NumWorkers is the worker process count; 0 leaves inference-perf's
	// default (the CPU count).
	NumWorkers int
}

// renderConfig builds the inference-perf config for a spec that checkSpec
// accepted. The output is deterministic, so it is golden-tested.
func renderConfig(runSpec spec.RunSpec, inputs configInputs) ([]byte, error) {
	stages, err := renderStages(inputs.Phases)
	if err != nil {
		return nil, err
	}
	data, err := renderData(runSpec.Workload.Dataset, inputs.Phases)
	if err != nil {
		return nil, err
	}
	baseURL, err := serverBaseURL(runSpec.Target.Endpoint)
	if err != nil {
		return nil, err
	}
	tokenizer := runSpec.Target.Model
	if override, ok := runSpec.Engine.Options[engineOptionTokenizer].(string); ok {
		tokenizer = override
	}
	config := nativeConfig{
		API:  apiSection{Type: "completion", Streaming: runSpec.Workload.Streaming},
		Data: data,
		Load: loadSection{
			Type:       loadType(runSpec.Load.Pacing),
			BaseSeed:   runSpec.Workload.Seed,
			NumWorkers: inputs.NumWorkers,
			Stages:     stages,
		},
		Server: serverSection{
			Type:      "vllm",
			ModelName: runSpec.Target.Model,
			BaseURL:   baseURL,
			CertPath:  inputs.CertFile,
			KeyPath:   inputs.KeyFile,
		},
		Tokenizer: tokenizerSection{PretrainedModelNameOrPath: tokenizer},
		Report: reportSection{RequestLifecycle: requestLifecycleSection{
			Summary:    true,
			PerStage:   true,
			PerRequest: true,
			PerRequestFields: perRequestFields{
				Info:            true,
				ComputedMetrics: true,
			},
		}},
		Storage: storageSection{LocalStorage: localStorage{Path: inputs.OutputDir}},
	}
	rendered, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("render inference-perf config: %w", err)
	}
	return appendPassthrough(rendered, runSpec.Engine.Options)
}

// appendPassthrough merges engine.options into the rendered config (the
// engine-native passthrough). checkEngineOptions has already rejected every
// key the harness owns, so what is left names top-level sections the rendered
// config does not have, and the merge is an append of those sections.
func appendPassthrough(rendered []byte, options spec.EngineOptions) ([]byte, error) {
	passthrough := make(map[string]any)
	for key, value := range options {
		if key != engineOptionTokenizer {
			passthrough[key] = value
		}
	}
	if len(passthrough) == 0 {
		return rendered, nil
	}
	extra, err := yaml.Marshal(passthrough)
	if err != nil {
		return nil, fmt.Errorf("render engine.options: %w", err)
	}
	return append(rendered, extra...), nil
}

// loadType maps the RunSpec pacing (already checked by checkSpec) to
// inference-perf's load.type.
func loadType(pacing spec.Pacing) string {
	if pacing == spec.PacingPoisson {
		return "poisson"
	}
	return "constant"
}

// renderStages turns the planned phases into inference-perf stages. Warmup has
// no rate of its own, so it runs at the first stage's rate: the load the target
// sees while warming up is the load it is about to be measured at.
func renderStages(phases []engine.Phase) ([]stageEntry, error) {
	var firstStageRate float64
	for _, phase := range phases {
		if phase.Kind == spec.PhaseKindStage {
			firstStageRate = phase.OfferedRPS
			break
		}
	}
	if firstStageRate == 0 {
		return nil, errors.New("load.stages: needs at least one stage")
	}
	stages := make([]stageEntry, 0, len(phases))
	for _, phase := range phases {
		seconds, err := wholeSeconds(phase.Duration)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", phase.Name, err)
		}
		rate := phase.OfferedRPS
		if phase.Kind == spec.PhaseKindWarmup {
			rate = firstStageRate
		}
		stages = append(stages, stageEntry{Rate: rate, Duration: seconds})
	}
	return stages, nil
}

// wholeSeconds converts a duration to the integer seconds inference-perf's
// stages take. A fraction would silently change the load, so it is an error.
func wholeSeconds(duration time.Duration) (int, error) {
	if duration < time.Second || duration%time.Second != 0 {
		return 0, fmt.Errorf("duration %s must be a whole number of seconds (inference-perf stages are integer seconds)", duration)
	}
	return int(duration / time.Second), nil
}

// renderDistribution maps a RunSpec token distribution to inference-perf's:
// no spread is a fixed length, otherwise a normal distribution clamped to
// mean +/- lengthWindowStandardDeviations standard deviations (at least 1).
// renderData maps the dataset to the random generator, or to shared_prefix
// when the dataset has one. input_tokens is the whole prompt, so the
// per-request question is input_tokens less the prefix, at least one token.
func renderData(dataset *spec.Dataset, phases []engine.Phase) (dataSection, error) {
	outputLengths, err := renderDistribution("workload.dataset.output_tokens", dataset.OutputTokens)
	if err != nil {
		return dataSection{}, err
	}
	if dataset.SharedPrefix == nil {
		inputLengths, err := renderDistribution("workload.dataset.input_tokens", dataset.InputTokens)
		if err != nil {
			return dataSection{}, err
		}
		return dataSection{Type: "random", InputDistribution: &inputLengths, OutputDistribution: &outputLengths}, nil
	}
	prefix := dataset.SharedPrefix
	question := dataset.InputTokens
	question.Mean -= float64(prefix.Tokens)
	questionLengths, err := renderDistribution("workload.dataset.input_tokens less shared_prefix.tokens", question)
	if err != nil {
		return dataSection{}, err
	}
	planned := 0.0
	for _, phase := range phases {
		planned += phase.OfferedRPS * phase.Duration.Seconds()
	}
	perGroup := max(1, int(math.Ceil(planned*promptHeadroom/float64(prefix.Groups))))
	return dataSection{Type: "shared_prefix", SharedPrefix: &sharedPrefixSection{
		NumGroups:          prefix.Groups,
		NumPromptsPerGroup: perGroup,
		SystemPromptLen:    prefix.Tokens,
		QuestionLen:        questionLengths,
		OutputLen:          outputLengths,
	}}, nil
}

func renderDistribution(field string, tokens spec.TokenDistribution) (distribution, error) {
	if tokens.Stddev == 0 {
		if tokens.Mean != math.Trunc(tokens.Mean) {
			return distribution{}, fmt.Errorf("%s: mean %v with no stddev is a fixed length and must be a whole number of tokens", field, tokens.Mean)
		}
		length := int(tokens.Mean)
		return distribution{Min: length, Max: length, Mean: tokens.Mean, Type: "fixed"}, nil
	}
	window := lengthWindowStandardDeviations * tokens.Stddev
	return distribution{
		Min:    max(1, int(math.Floor(tokens.Mean-window))),
		Max:    int(math.Ceil(tokens.Mean + window)),
		Mean:   tokens.Mean,
		StdDev: tokens.Stddev,
		Type:   "normal",
	}, nil
}

// serverBaseURL strips the /v1 that inference-perf adds to every route.
func serverBaseURL(endpoint string) (string, error) {
	trimmed := strings.TrimRight(endpoint, "/")
	base, found := strings.CutSuffix(trimmed, serverPathSuffix)
	if !found {
		return "", fmt.Errorf("target.endpoint %q must end in %s: inference-perf appends %s/completions itself", endpoint, serverPathSuffix, serverPathSuffix)
	}
	return base, nil
}
