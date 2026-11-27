import type {
  Host,
  KnobCatalog,
  ListRunEventsResponse,
  ManagedTarget,
  RunDetail,
  RunResource,
  WorkloadPreset,
} from "../api/types";

// Typed against the generated schema: a contract change breaks the build here.
export const succeededRun: RunResource = {
  id: "01JTESTSUCCEEDED000000000A",
  name: "vllm-baseline",
  experiment: "max-num-seqs",
  status: "succeeded",
  target_type: "vllm",
  model: "example/model-8b",
  knobs: { max_num_seqs: 128 },
  labels: { team: "serving" },
  spec_hash: "sha256:aaaa",
  bundle_store: "default",
  bundle_key: "runs/01JTESTSUCCEEDED000000000A/",
  bundle_uri: "file:///data/runs/01JTESTSUCCEEDED000000000A/",
  created_at: "2026-10-28T12:00:00Z",
};

export const runningRun: RunResource = {
  ...succeededRun,
  id: "01JTESTRUNNING0000000000B",
  name: "bifrost-ramp",
  experiment: "router-overhead",
  status: "running",
  target_type: "bifrost",
  knobs: {},
  labels: {},
  created_at: "2026-10-28T13:30:00Z",
};

export const failedRun: RunResource = {
  ...succeededRun,
  id: "01JTESTFAILED00000000000C",
  name: "litellm-soak",
  experiment: null,
  status: "failed",
  target_type: "litellm",
  created_at: "2026-10-28T14:00:00Z",
};

export const succeededRunDetail: RunDetail = {
  ...succeededRun,
  started_at: "2026-10-28T12:00:05Z",
  finished_at: "2026-10-28T12:10:00Z",
  summary: {
    max_sustainable_rps: 10,
    stages: [
      {
        stage_index: 0,
        offered_rps: 5,
        achieved_rps: 4.99,
        requests: 1500,
        errors: 0,
        is_valid: true,
        is_client_bound: false,
        ttft_p95_seconds: 0.12,
        e2e_p95_seconds: 1.5,
      },
      {
        stage_index: 1,
        offered_rps: 20,
        achieved_rps: 11,
        requests: 3300,
        errors: 12,
        is_valid: false,
        is_client_bound: true,
        ttft_p95_seconds: null,
        e2e_p95_seconds: null,
      },
    ],
  },
};

export const runningRunDetail: RunDetail = { ...runningRun, phase: "stage-1", started_at: "2026-10-28T13:30:05Z" };

export const succeededRunEvents: ListRunEventsResponse = [
  {
    run_id: succeededRun.id,
    sequence: 1,
    time: "2026-10-28T12:05:00Z",
    step: "load",
    state: "running",
    phase: "stage-1",
    message: null,
  },
  {
    run_id: succeededRun.id,
    sequence: 0,
    time: "2026-10-28T12:00:10Z",
    step: "resolve",
    state: "running",
    phase: null,
    message: null,
  },
  {
    run_id: succeededRun.id,
    sequence: 2,
    time: "2026-10-28T12:09:59Z",
    step: "finalize",
    state: "succeeded",
    phase: null,
    message: "bundle written",
  },
];

// Workload presets as GET /workload-presets returns them (schema/presets/).
export const workloadPresets: WorkloadPreset[] = [
  {
    name: "chat-short",
    version: 1,
    description: "Single-turn chat, about 256 tokens in and 256 out. The balanced baseline.",
    engines: ["loadgen"],
    workload: {
      api: "chat",
      dataset: {
        type: "synthetic",
        input_tokens: { mean: 256, stddev: 64 },
        output_tokens: { mean: 256, stddev: 64 },
        padding_bytes: 0,
      },
    },
    slos: { ttft_p95: "500ms", itl_p95: "50ms" },
    rps_hint: [{ gpu_class: "rtx-3070-8gb", start_rps: 2 }],
  },
  {
    name: "rag-prefill",
    version: 1,
    description: "Retrieval-augmented chat.",
    engines: ["loadgen"],
    workload: {
      api: "chat",
      dataset: {
        type: "synthetic",
        input_tokens: { mean: 4096, stddev: 1024 },
        output_tokens: { mean: 256, stddev: 64 },
        shared_prefix: { tokens: 512, groups: 4 },
        padding_bytes: 0,
      },
    },
    slos: { ttft_p95: "2s", itl_p95: "60ms" },
    rps_hint: [
      { gpu_class: "rtx-3070-8gb", start_rps: 0.5 },
      { gpu_class: "a100-80gb", start_rps: 4 },
    ],
  },
];

/** A Kubernetes managed vLLM, never deployed. */
export const managedTarget: ManagedTarget = {
  id: "01JMANAGED0000000000000000A",
  name: "qwen",
  namespace: "servtuner-tune-qwen",
  spec: {
    placement: "kubernetes",
    image: "vllm/vllm-openai:v0.34.0",
    model: "Qwen/Qwen3-32B",
    gpus: 2,
    cpu: "16",
    memory: "128Gi",
    model_cache: { size: "200Gi" },
    transport: { enabled: false },
  },
  run_target: {
    type: "vllm",
    endpoint: "http://qwen.servtuner-tune-qwen.svc:8000/v1",
    model: "Qwen/Qwen3-32B",
    tls: { enabled: false },
    upstreams: [],
  } as ManagedTarget["run_target"],
  run_labels: { "servtuner.managed-target": "01jmanaged0000000000000000a" },
  created_at: "2026-11-23T10:00:00Z",
  updated_at: "2026-11-23T10:00:00Z",
};

/** A host the 3070 target runs on, read from its launcher and priced as owned hardware. */
export const host: Host = {
  id: "01JHOST0000000000000000000",
  name: "gpu-box",
  note: "the desk machine",
  hardware: {
    source: "launcher",
    read_at: "2026-11-25T18:00:00Z",
    gpus: [{ model: "NVIDIA GeForce RTX 3070", memory_bytes: 8589934592, driver_version: "581.57" }],
    cpu_model: "AMD Ryzen 7 5800X 8-Core Processor",
    cpu_cores: 16,
    memory_bytes: 34359738368,
  },
  price: { kind: "owned", purchase_usd: 500, amortization_hours: 8760, watts: 220, usd_per_kwh: 0.087 },
  usd_per_hour: 0.0762,
  price_basis: "500 USD ÷ 8760 h + 220 W ÷ 1000 × 0.087 USD/kWh = 0.07622 USD/h",
  target_ids: [],
  created_at: "2026-11-25T18:00:00Z",
  updated_at: "2026-11-25T18:00:00Z",
};

/** A slice of GET /knobs: one knob of each kind the deploy form edits. */
export const knobCatalog: KnobCatalog = {
  vllm_version: "v0.34.0",
  vllm_commit: "62d4bc3c7e33b1b6c906e233e6af2d3506c482ca",
  // A slice too: --max-model-len stands for the knobs this fixture leaves out.
  other_flags: ["--compilation-config", "--max-model-len", "--port", "--served-model-name", "-cc"],
  knobs: [
    {
      name: "vllm.max_num_seqs",
      label: "Max sequences",
      flag: "--max-num-seqs",
      aliases: [],
      negative_flags: [],
      group: "SchedulerConfig",
      kind: "integer",
      help: "Maximum number of sequences to be processed in a single iteration.\n\nThe default value here is mainly for convenience when testing.",
      limits: { ge: 1 },
      built_in: true,
      tuned_as_range: true,
    },
    {
      name: "vllm.gpu_memory_utilization",
      label: "GPU memory utilization",
      flag: "--gpu-memory-utilization",
      aliases: [],
      negative_flags: [],
      group: "CacheConfig",
      kind: "number",
      help: "The fraction of GPU memory to be used for the model executor.",
      limits: { gt: 0, le: 1 },
      built_in: true,
      tuned_as_range: false,
    },
    {
      name: "vllm.enable_prefix_caching",
      label: "Prefix caching",
      flag: "--enable-prefix-caching",
      aliases: [],
      negative_flags: ["--disable-prefix-caching"],
      group: "CacheConfig",
      kind: "boolean",
      help: "Whether to enable prefix caching.",
      built_in: true,
      tuned_as_range: false,
    },
    {
      name: "vllm.dtype",
      label: "Weight data type",
      flag: "--dtype",
      aliases: [],
      negative_flags: [],
      group: "ModelConfig",
      kind: "choice",
      default: "auto",
      choices: ["auto", "bfloat16", "float16"],
      help: "Data type for model weights and activations.",
      built_in: true,
      tuned_as_range: false,
    },
    {
      name: "vllm.kv_cache_dtype",
      label: "KV cache data type",
      flag: "--kv-cache-dtype",
      aliases: [],
      negative_flags: [],
      group: "CacheConfig",
      kind: "text",
      default: "auto",
      suggestions: ["auto", "fp8"],
      help: "Data type for kv cache storage.",
      built_in: true,
      tuned_as_range: false,
    },
    {
      name: "vllm.max_num_scheduled_tokens",
      label: "Max number of scheduled tokens",
      flag: "--max-num-scheduled-tokens",
      aliases: [],
      negative_flags: [],
      group: "SchedulerConfig",
      kind: "integer",
      help: "Maximum number of tokens scheduled per step.",
      built_in: false,
      tuned_as_range: false,
    },
  ],
};
