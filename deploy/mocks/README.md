# Mock target on a cluster

`chart/` (published as `oci://ghcr.io/servtuner/charts/servtuner-mock`) is a small Helm chart that runs `openai-mock` in namespace `servtuner-mock`, with TLS (`tls.enabled`) and client
certificates `required`, `optional` or `none` (`tls.clientAuth`), or as plain HTTP. `test.sh` renders each combination offline
(`KUBECONFIG=/dev/null`, no cluster contact). `examples/` has RunSpecs for mock-direct (with a client certificate, and
without TLS) and Bifrost to mock; all validate with `servtuner validate`.

Nothing here is applied automatically. The steps below are for whoever operates the cluster. All images,
registries and hostnames are placeholders.

## How /metrics is exposed

The mock has a `--metrics-listen` flag (`OPENAI_MOCK_METRICS_LISTEN`). When set, `/metrics` is served there in
**plain HTTP, with or without TLS on the main listener**, and is removed from the main listener; empty keeps it on the main listener. The
reason: the mock's counters are not sensitive, and Prometheus cannot present a client certificate per target,
so a mock that requires client certificates would otherwise be unscrapable. The main listener keeps its own settings (it
still refuses clients without a certificate when they are required). The chart sets it to port 9100 either way, so there is one scrape path:
the pod annotations are `servtuner.dev/metrics-port: "9100"`, `servtuner.dev/metrics-scheme: http`, `servtuner.dev/metrics-path: /metrics`.

Pods carry the label `servtuner.dev/scrape=true`.

## 1. Install the mock

Prerequisite: the `servtuner-ca` ClusterIssuer is Ready (with TLS; `deploy/helmfile/manifests/issuer/cluster-issuer.yaml`).

Each release publishes the chart and its image (`ghcr.io/servtuner/servtuner-mock:<version>`, signed like servtuner's
own images), and its Release carries a `servtuner-mock` binary for one machine:

```
helm install openai-mock oci://ghcr.io/servtuner/charts/servtuner-mock --version <version> \
  -n servtuner-mock --create-namespace --set tls.enabled=true --set tls.clientAuth=required
```

From this checkout instead, with a local image:

```
just images                                  # builds servtuner-mock:<tag> (SERVTUNER_IMAGE_TAG, default e2e)
# make the image pullable by the cluster (push to your registry, or `kind load docker-image`), then:
helm install openai-mock deploy/mocks/chart -n servtuner-mock --create-namespace \
  --set tls.enabled=true --set tls.clientAuth=required \
  --set image.repository=<registry>/servtuner-mock --set image.tag=<tag>
```

For plain HTTP use `--set tls.enabled=false`; `tls.clientAuth` also takes `optional` (a certificate that is sent is
verified) and `none`. One release per namespace; changing these is `helm upgrade` with the new values. Mock behaviour (`mock.ttft`, `mock.itl`, `mock.outputTokens`, ...) is in `chart/values.yaml`.
Endpoints: `https://openai-mock.servtuner-mock.svc:8000/v1` (TLS) or `http://openai-mock.servtuner-mock.svc:8000/v1` (plain HTTP).
The mock serves `POST /v1/chat/completions` and `POST /v1/completions` (the legacy text API that inference-perf
uses), both streaming and non-streaming, plus `GET /v1/models`, `/healthz` and `/metrics`. Both inference paths
share one generation core, so the TTFT/ITL knobs, token accounting and `openai_mock_*` metrics behave the
same; the metrics already carry a `path` label that tells them apart. On `/v1/completions`, an array `prompt`
uses only its first element, `min_tokens` and `ignore_eos` are accepted and ignored (as on chat), and the
`--role-only-first-chunk` knob sends an empty-text chunk before the TTFT delay.

### As vLLM (tuning)

The image also installs the binary as `/usr/local/bin/vllm`. Invoked as `vllm serve <model> --flag=value ...`
(what servtuner's deployer runs for a managed target), it emulates vLLM's interface:

- it listens only after `VLLM_MOCK_BOOT_SECONDS` (default 3), as vLLM refuses connections until ready;
- a flag named in `VLLM_MOCK_UNKNOWN_FLAGS` (comma-separated) fails like vLLM's argument parser: `usage:` and
  `error: unrecognized arguments`, exit 2;
- args holding every `--flag=value` of a set in `VLLM_MOCK_INFEASIBLE` (sets separated by `;`, flags in a set
  by `,`) fail like a KV cache too small: `No available memory for the cache blocks`, then the generic
  `Engine core initialization failed` line last, exit 1;
- `--ssl-certfile` and `--ssl-keyfile` serve TLS asking for no client certificate;
- `/health`, plus vLLM's metric names: `vllm:cache_config_info` (num_gpu_blocks = `VLLM_MOCK_GPU_BLOCKS`,
  default 2048, x `--tensor-parallel-size` x `--gpu-memory-utilization` / 0.9), requests running and waiting,
  KV-cache usage, token and preemption counters;
- `--max-num-seqs` (default 256) caps requests in service, the rest queue, so the scheduler knob changes
  throughput. Latency and the other knobs still come from `OPENAI_MOCK_*`.

Every other flag is accepted and ignored: the mock emulates the interface, not the engine.

### As SGLang (observing)

`--emulate sglang` (`OPENAI_MOCK_EMULATE`, chart value `mock.emulate`) adds what servtuner reads from an SGLang
v0.5.23 server, so an `sglang` target runs on kind without a GPU:

- `/server_info` (and the deprecated `/get_server_info`): flat resolved server arguments, the launch command,
  `max_total_num_tokens`, `version` and `frontend`; `/model_info` (and `/get_model_info`), `/health`,
  `/health_generate`;
- SGLang's metric names and labels: running, queued, paused and retracted requests, `token_usage` and the KV
  token gauges, the KV pool size and context length (scheduler series: `model_name`, `engine_type`, `tp_rank`,
  `pp_rank`, `moe_ep_rank`), and the token, cached-token and abort counters (tokenizer series). The latency
  histograms are not emulated; the client measures latency.

Starting as `sglang serve` for a managed SGLang is not emulated yet.

### Fault knobs

Off by default. Each makes the mock misbehave the way a real target does, so every Bifrost and
cross-target advisor rule can be triggered on `kind`. Set them under `mock.faults`
in the chart, as `OPENAI_MOCK_*` variables, or as flags.

| Value (`mock.faults.*`) | Flag | Effect | Exercises |
|---|---|---|---|
| `extraLatency` | `--extra-latency` | delay before the first token of every request | latency budget (X1) |
| `latencyPerInFlight` | `--latency-per-in-flight` | that much more delay per other request in service: latency rises with load | upstream-bound (B3), queueing model |
| `maxConcurrency`, `overCapacity` | `--max-concurrency`, `--over-capacity` | at most N requests in service; beyond that `queue` (wait for a slot) or `reject` (429) | concurrency-bound and queueing (B1, B2) |
| `errorRate`, `errorStatus` | `--error-rate`, `--error-status` | that share of requests answered with the status, spread evenly (every 1/rate-th), so runs are reproducible | retries and errors (B4), the error SLO |
| `memoryPerRequestBytes` | `--memory-per-request-bytes` | bytes allocated and touched while each request is served | memory per in-flight request |
| `memoryRampBytesPerSecond`, `memoryRampMaxBytes` | `--memory-ramp-bytes-per-second`, `--memory-ramp-max-bytes` | retained bytes added every second up to the maximum: a leak | memory slope, B7 |

Metrics: `openai_mock_requests_queued`, `openai_mock_faults_total{kind="rejected"|"error"}` and
`openai_mock_memory_ramp_bytes`. `openai_mock_requests_in_flight` counts only admitted requests, not queued
ones. Give the pod a memory limit with headroom above the ramp and per-request memory.

## 2. Re-apply the Prometheus snippet

The live `prometheus` release also has an `opencost` job. **Merge, do not replace**: `extraScrapeConfigs` is a
single string, so a plain `-f` of `deploy/prometheus/servtuner-targets.values.yaml` would overwrite it.

```
helm get values prometheus -n monitoring -o yaml > live.yaml     # note the existing extraScrapeConfigs (opencost job)
# build merged.yaml: the opencost job followed by the servtuner-targets job from deploy/prometheus/servtuner-targets.values.yaml
helm upgrade prometheus prometheus-community/prometheus -n monitoring --reuse-values -f merged.yaml
```

The keep-regex is `vllm:.*|bifrost_.*|http_request.*|http_response.*|litellm_.*|openai_mock_.*`. The job sets the
`namespace` and `pod` labels on every series. Check afterwards: `up{job="servtuner-targets"} == 1`.

## 3. Install a Bifrost for servtuner

servtuner gets its own Bifrost, release `servtuner-bifrost` in namespace `servtuner-bifrost`, so no other Bifrost in the
cluster is touched. `deploy/kind/servtuner-bifrost.values.yaml` declares everything: the pod label
`servtuner.dev/scrape: "true"`, the `servtuner.dev/metrics-*` annotations (Bifrost serves `/metrics` on its main port 8080,
plain HTTP, default-on telemetry plugin), SQLite storage with no PVC, modest resources, and the mock as a custom
OpenAI-compatible provider named `servtuner-mock` (base URL `http://openai-mock.servtuner-mock.svc:8000`, no `/v1`:
Bifrost appends it; model `mock-model`; chat and text completions, streaming allowed; a placeholder key the mock
ignores). Nothing is added by hand through the UI or API.

The chart is the Bifrost checkout's `helm-charts/bifrost` (chart 2.1.44, app 1.5.12, image `docker.io/maximhq/bifrost`
from Docker Hub). It is not vendored here. Install the plain-HTTP mock first (step 1, `tls.enabled=false`). The
Bifrost-to-mock hop is plain HTTP, and `examples/bifrost-to-mock.json` declares it as an upstream with `tls: {enabled: false}`
(upstreams carry their own `tls`, never inferred).

```
helm install servtuner-bifrost <path to the bifrost chart> -n servtuner-bifrost --create-namespace \
  -f deploy/kind/servtuner-bifrost.values.yaml
deploy/kind/test-servtuner-bifrost.sh        # offline render checks; set BIFROST_CHART to override the chart path
```

Admin auth is **disabled** in this release (dev only; commented in the values file), so the snapshot step can read
`/api/config` and `/api/providers` and the shared `servtuner-targets` job can scrape `/metrics` without credentials.
Enabling it later needs a Secret, an admin credential reference in the RunSpec, and a separate authenticated
scrape job (`deploy/prometheus/servtuner-targets.values.yaml`).

Check: `kubectl get pod -n servtuner-bifrost -l servtuner.dev/scrape=true`, then `up{job="servtuner-targets",namespace="servtuner-bifrost"} == 1`.
`examples/bifrost-to-mock.json` addresses the provider as model `servtuner-mock/mock-model` at
`http://servtuner-bifrost.servtuner-bifrost.svc:8080/v1`.

## Examples

`examples/mock-direct-client-cert.json` (client certificate Secret `servtuner-bench-tls`, as issued by the servtuner chart),
`examples/mock-direct-tls-off.json`, `examples/bifrost-to-mock.json`. Validate with
`cd bench && go run ./cmd/servtuner validate --spec ../deploy/mocks/examples/<file>`.
