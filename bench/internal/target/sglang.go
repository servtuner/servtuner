package target

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// SGLangServerInfoDocument is the redacted /server_info body, kept under
// target/config/ (the phase is added to the name): every resolved server
// argument, the launch command and the scheduler's startup facts.
const SGLangServerInfoDocument = "sglang-server-info.json"

// SGLang knob names: sglang.<ServerArgs field>. One definition each.
const (
	KnobSGLangTPSize                  = "sglang.tp_size"
	KnobSGLangDPSize                  = "sglang.dp_size"
	KnobSGLangPPSize                  = "sglang.pp_size"
	KnobSGLangMemFractionStatic       = "sglang.mem_fraction_static"
	KnobSGLangMaxRunningRequests      = "sglang.max_running_requests"
	KnobSGLangMaxQueuedRequests       = "sglang.max_queued_requests"
	KnobSGLangMaxTotalTokens          = "sglang.max_total_tokens"
	KnobSGLangChunkedPrefillSize      = "sglang.chunked_prefill_size"
	KnobSGLangMaxPrefillTokens        = "sglang.max_prefill_tokens"
	KnobSGLangSchedulePolicy          = "sglang.schedule_policy"
	KnobSGLangScheduleConservativness = "sglang.schedule_conservativeness"
	KnobSGLangDisableRadixCache       = "sglang.disable_radix_cache"
	KnobSGLangContextLength           = "sglang.context_length"
	KnobSGLangDtype                   = "sglang.dtype"
	KnobSGLangKVCacheDtype            = "sglang.kv_cache_dtype"
	KnobSGLangQuantization            = "sglang.quantization"
	KnobSGLangAttentionBackend        = "sglang.attention_backend"
	KnobSGLangPageSize                = "sglang.page_size"
	KnobSGLangStreamInterval          = "sglang.stream_interval"
	KnobSGLangSpecAlgorithm           = "sglang.speculative_algorithm"
	KnobSGLangSpecNumSteps            = "sglang.speculative_num_steps"
	KnobSGLangSpecNumDraftTokens      = "sglang.speculative_num_draft_tokens"
	KnobSGLangCudaGraphMaxBSDecode    = "sglang.cuda_graph_max_bs_decode"
	KnobSGLangEnableMetrics           = "sglang.enable_metrics"
)

// sglangFlag ties an SGLang flag to its knob and its /server_info field. The
// first flag is the long form (the field name with dashes); the rest are
// SGLang's aliases (checked against v0.5.23, srt/arg_groups/fields/). SGLang
// declares booleans as store_true, so in container args they are switches.
type sglangFlag struct {
	knob  string
	field string
	kind  knobKind
	flags []string
}

var sglangFlags = []sglangFlag{
	{KnobSGLangTPSize, "tp_size", knobNumber, []string{"--tp-size", "--tensor-parallel-size"}},
	{KnobSGLangDPSize, "dp_size", knobNumber, []string{"--dp-size", "--data-parallel-size"}},
	{KnobSGLangPPSize, "pp_size", knobNumber, []string{"--pp-size", "--pipeline-parallel-size"}},
	{KnobSGLangMemFractionStatic, "mem_fraction_static", knobNumber, []string{"--mem-fraction-static"}},
	{KnobSGLangMaxRunningRequests, "max_running_requests", knobNumber, []string{"--max-running-requests"}},
	{KnobSGLangMaxQueuedRequests, "max_queued_requests", knobNumber, []string{"--max-queued-requests"}},
	{KnobSGLangMaxTotalTokens, "max_total_tokens", knobNumber, []string{"--max-total-tokens"}},
	{KnobSGLangChunkedPrefillSize, "chunked_prefill_size", knobNumber, []string{"--chunked-prefill-size"}},
	{KnobSGLangMaxPrefillTokens, "max_prefill_tokens", knobNumber, []string{"--max-prefill-tokens"}},
	{KnobSGLangSchedulePolicy, "schedule_policy", knobText, []string{"--schedule-policy"}},
	{KnobSGLangScheduleConservativness, "schedule_conservativeness", knobNumber, []string{"--schedule-conservativeness"}},
	{KnobSGLangDisableRadixCache, "disable_radix_cache", knobSwitch, []string{"--disable-radix-cache"}},
	{KnobSGLangContextLength, "context_length", knobNumber, []string{"--context-length"}},
	{KnobSGLangDtype, "dtype", knobText, []string{"--dtype"}},
	{KnobSGLangKVCacheDtype, "kv_cache_dtype", knobText, []string{"--kv-cache-dtype"}},
	{KnobSGLangQuantization, "quantization", knobText, []string{"--quantization"}},
	{KnobSGLangAttentionBackend, "attention_backend", knobText, []string{"--attention-backend"}},
	{KnobSGLangPageSize, "page_size", knobNumber, []string{"--page-size"}},
	{KnobSGLangStreamInterval, "stream_interval", knobNumber, []string{"--stream-interval"}},
	{KnobSGLangSpecAlgorithm, "speculative_algorithm", knobText, []string{"--speculative-algorithm"}},
	{KnobSGLangSpecNumSteps, "speculative_num_steps", knobNumber, []string{"--speculative-num-steps"}},
	{KnobSGLangSpecNumDraftTokens, "speculative_num_draft_tokens", knobNumber, []string{"--speculative-num-draft-tokens"}},
	{KnobSGLangCudaGraphMaxBSDecode, "cuda_graph_max_bs_decode", knobNumber, []string{"--cuda-graph-max-bs-decode"}},
	{KnobSGLangEnableMetrics, "enable_metrics", knobSwitch, []string{"--enable-metrics"}},
}

type sglangAdapter struct {
	kubeReader
}

func newSGLangAdapter() TargetAdapter {
	return sglangAdapter{kubeReader{spec.TargetTypeSglang}}
}

// DefaultCatalogs implements TargetAdapter.
func (sglangAdapter) DefaultCatalogs() []string { return []string{CatalogSGLang, CatalogKubernetes} }

// KnobsFromWorkload implements TargetAdapter: the flags on the container
// command line, where the pods agree. They are the fallback: a --config file
// hides flags from the args, and /server_info's resolved values (Info) take
// precedence.
func (sglangAdapter) KnobsFromWorkload(workload spec.Workload_2) Knobs {
	return podKnobs(workload, sglangKnobsFromArgs)
}

// Info implements TargetAdapter: /v1/models, then /server_info (the
// deprecated /get_server_info on servers older than the rename). /server_info
// needs the API key when SGLang has one; a refusal is reported and the run
// keeps what it has.
func (sglangAdapter) Info(ctx context.Context, endpoint Endpoint) (Info, error) {
	info := Info{Knobs: Knobs{}, ConfigDocuments: map[string][]byte{}}
	var problems []error
	if err := fetchModels(ctx, endpoint, &info); err != nil {
		problems = append(problems, err)
	}
	document, err := endpoint.getJSON(ctx, "/server_info")
	if errors.Is(err, errNotFound) {
		document, err = endpoint.getJSON(ctx, "/get_server_info")
	}
	if err != nil {
		return info, errors.Join(append(problems, err)...)
	}
	serverInfo, _ := document.(map[string]any)
	if version, ok := serverInfo["version"].(string); ok && version != "" {
		info.TargetInfo.Version = &version
	}
	for _, flag := range sglangFlags {
		kind := flag.kind
		if kind == knobSwitch {
			kind = knobBoolean // a resolved value, true or false
		}
		if value, ok := knobFromJSON(serverInfo[flag.field], kind); ok {
			info.Knobs[flag.knob] = value
		}
	}
	if err := storeServerInfo(&info, serverInfo); err != nil {
		problems = append(problems, err)
	}
	return info, errors.Join(problems...)
}

// storeServerInfo keeps /server_info redacted. Fields are redacted by name;
// launch_command is one string whose flags may carry a key (--api-key), so its
// arguments go through RedactArgs as container args do.
func storeServerInfo(info *Info, serverInfo map[string]any) error {
	if serverInfo == nil {
		return nil
	}
	redacted, _ := RedactJSON(serverInfo).(map[string]any)
	if command, ok := serverInfo["launch_command"].(string); ok {
		redacted["launch_command"] = strings.Join(RedactArgs(strings.Fields(command)), " ")
	}
	encoded, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return err
	}
	info.ConfigDocuments[SGLangServerInfoDocument] = append(encoded, '\n')
	return nil
}

// sglangKnobsFromArgs reads the knob flags from a container's args, whichever
// entry point started SGLang (python3 -m sglang.launch_server or sglang serve):
// flags are matched by name, wherever they are.
func sglangKnobsFromArgs(args []string) Knobs {
	knobs := Knobs{}
	for _, flag := range sglangFlags {
		if flag.kind == knobSwitch {
			for _, argument := range args {
				if argument == flag.flags[0] {
					knobs[flag.knob] = true
				}
			}
			continue
		}
		text, found := flagValue(args, flag.flags)
		if !found {
			continue
		}
		if value, ok := parseKnobValue(text, flag.kind); ok {
			knobs[flag.knob] = value
		}
	}
	return knobs
}
