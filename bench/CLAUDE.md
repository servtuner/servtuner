# CLAUDE.md — bench (load-testing tool)

## Purpose

`bench` executes **one benchmark run** end to end and writes its run bundle.
It doesn't care whether it runs as a Kubernetes Job pod or as a local
subprocess: the command, inputs and outputs are identical. It must also work
with no API present, for local runs and CI.

`bench` is a **single Go binary**. It contains:
- the harness,
- the built-in **loadgen** engine, which is RPS-focused,
- and an adapter that drives **inference-perf** as a pinned external
  subprocess.

## CLI

```
servtuner run      --spec spec.yaml --out <bundle dir | s3://bucket/prefix> [--run-id ULID]   # s3: written locally, uploaded manifest last; --object-store-tls required
               [--api-url URL --run-token-file PATH --api-tls true|false
                [--tls-cert-file PATH --tls-key-file PATH] [--tls-ca-file PATH]]
               [--shard-index I]   # engine.shards N: 0 (default) leads; 1..N-1 follow, with the leader's --run-id and --out
               [--offline]         # no internet: no public datasheet fallback, AWS prices refused; recorded in environment.json
servtuner validate --spec spec.yaml [--strict] [--json]  # schema check, TLS rules, stage-length warnings; --json prints {valid,name,errors,warnings}
servtuner analyze  <bundle>          # recompute analysis.json from raw data (quality gate, aggregates with bootstrap CIs, capacity model),
                                 # plus recommendations.json, capacity.json/.csv, capacity_model.json and patches/
servtuner plan --input plan-request.json --models capacity_model.json... [--out plan.json]   # the capacity planner
servtuner compare --bundles <bundle>,<bundle>... [--objective NAME] [--out comparison.json]   # experiment comparison
servtuner propose --space space.yaml [--bundles <bundle>,...] [--infeasible boots.yaml] [--out proposal.json]
                                 # tuning loop: the next configurations to boot or run, or a done verdict;
                                 # pure: files in, JSON out, byte-identical for the same inputs, no clock or network
servtuner presets                    # the embedded workload presets as a JSON array (the api serves it at GET /workload-presets)
servtuner spaces                     # the embedded starting search spaces (schema/search-spaces) as a JSON array (GET /search-spaces)
servtuner knobs [--server sglang]   # every vllm serve (or SGLang server) flag that is a knob (KnobCatalog; one value, not servtuner's own): label, kind, default, limits and vLLM's own
                                 # description from internal/target/vllm_catalog.json (generated: just vllm-catalog; GET /knobs)
servtuner vllm-args --knobs <knobs.json>  # the vLLM container args for a knob set, as JSON, from the knob table
servtuner server-args --server vllm|sglang --knobs <knobs.json>  # the same for either server (SGLang: its catalog); the api's deployer uses it
servtuner boot-probe --input <boot-probe-request.json> [--secrets-dir DIR]  # a booted vLLM's num_gpu_blocks and c_kv, as a tuningBootResult
servtuner launcher [--config F] [--listen ADDR --token-file F --tls true|false [--client-auth required|optional|none]] [--api-key-file F] [--vllm PATH] [--sglang PYTHON] [--log-dir D]
                                 # runs vllm serve, or SGLang (PUT /server {server, args, env}), for the tuning loop on a host outside Kubernetes (internal/launcher);
                                 # flags over a YAML config (--config, $SERVTUNER_LAUNCHER_CONFIG, $XDG_CONFIG_HOME/servtuner/launcher.yaml,
                                 # /etc/servtuner/launcher.yaml), secret files defaulting to $CREDENTIALS_DIRECTORY (deploy/launcher/)
servtuner catalogs [--target-type T] # list PromQL catalogs and the queries they expand to (not built yet)
servtuner plan     --input plan-request.json --models <capacity_model.json…>  # capacity plan (pure computation, no I/O beyond files)
```

Contract with the api's runner (the api builds this command line):
- `--out` is the bundle directory itself (the api passes `<dataDir>/runs/<run-id>`).
  Without it, bench uses `<data dir>/runs/<run id>`, where the data dir is
  `$SERVTUNER_DATA_DIR`, else `$XDG_DATA_HOME/servtuner`, else `~/.local/share/servtuner`
  (one function, `storage.DataDir`). `s3://` is not built yet and is rejected.
- `--run-id` is a ULID and defaults to a fresh one; it is recorded in the manifest.
- The spec may be YAML or JSON. `spec.yaml` in the bundle is the submitted file
  byte for byte; it is read only through `spec.ParseRunSpec`.
- Status events go to `POST {api-url}/runs/{run-id}/events` with
  `Authorization: Bearer <token from --run-token-file>` and a body valid against
  `events.schema.json`. A delivery failure is logged and never fails the run:
  the exit status is authoritative.
- `--api-tls` (`true` or `false`) is required with `--api-url` and never
  inferred. `true` needs an `https://` URL, and presents a client certificate
  when `--tls-cert-file` and `--tls-key-file` are given; `false` needs an
  `http://` URL and forbids the cert flags. Trust is the system pool, which Go
  extends from `SSL_CERT_FILE`. `--object-store-tls` works the same way.
- `servtuner validate` and the resolve step of `servtuner run` share one rule set
  (`internal/validate`).
  - Stage length is checked against `max(10 x I, 60s)`, and warmup against
    `max(2 x I, 30s)`.
  - I is the slowest relevant scrape interval, **discovered** from the
    RunSpec's Prometheus (`/api/v1/targets`), or **assumed** to be 5s when
    there is none or it is unreachable. The source is recorded.
  - `servtuner run` writes the warnings to `phases.json`: `warnings` for stage
    rules and `scrape_warnings` for a missing scrape job. `servtuner validate`
    prints them. A warning exits 0; `--strict` turns warnings into errors.
- Engines are registered in `internal/cli/engines.go`. The RunSpec's
  `engine.type` picks one (`loadgen`, `inference-perf`). The hidden flag
  `--engine-override null` selects an engine that offers no load, for tests.
- Target credentials (the client certificate Secret, `target.auth`) are read from the
  mounted Secrets only when an engine that talks to the target is built. A
  missing Secret fails the run at resolve with exit 1.
- With no cluster, the target snapshot is skipped and the reason recorded.
  With no Prometheus, collection is skipped and recorded in `queries.json`.
  The run still writes `analysis.json`, graded degraded.

Exit codes: `0` succeeded · `1` run failed · `2` invalid spec (also invalid
flags) · `3` target unreachable · `4` bundle write failed. When several apply,
`4` wins.

## Run pipeline

1. **Resolve.** Load the RunSpec, validate it against the JSON Schema in
   `../schema`, apply defaults, write `spec.yaml` and `spec.resolved.json`, and
   allocate the bundle.
2. **Snapshot.** Through the Kubernetes API (client-go, read-only RBAC), capture
   the target workload: pods, images and digests, args, env (redacted),
   resources, replicas, node labels (instance type, GPU) and HPA. Also capture
   target-reported info (for example vLLM `/v1/models`, `/version`, and
   `/server_info` when dev mode exposes it; vLLM scheduler flags otherwise come
   from the container args) and any
   configured config sources. Skip cleanly when no cluster is reachable
   (subprocess mode). Probe optional catalogs such as `dcgm` for their marker
   series, and read effective scrape intervals from `/api/v1/targets`.
3. **Load.** Drive the `Engine` through warmup and then each stage, recording
   the exact UTC window of every phase in `phases.json`. Engine-native output
   goes to `engine/` untouched.
4. **Collect.** After the load ends, wait a settle delay of at least 2× the
   slowest relevant scrape interval. Then run range queries for each detected
   catalog over the run window, and store the raw results and the exact
   queries. An absent optional catalog is skipped and recorded, never an error.
5. **Normalize.** Convert engine output into `requests.parquet` with the
   schema's request record. Fix definitional differences here, for example
   whether ITL includes TTFT. The loadgen engine already writes the native
   record, so this step is a pass-through for it.
6. **Summarize.** Compute `summary.json` from the raw files only, never from
   in-memory state, so that `servtuner analyze` gives the same answer.
7. **Finalize.** Write `manifest.json` with checksums and the final status.

Each step emits a status event. A failure in any step still
finalizes the bundle with whatever was collected and the error.

## Proxies

- **The hop to the target** (loadgen and the TLS relay) ignores
  `HTTPS_PROXY`/`NO_PROXY`. A proxy in that path would add its own latency
  and connection behaviour to every measurement.
- **servtuner's own connections** (Prometheus, the api, object store) use clients
  cloned from Go's default transport. They honour the proxy environment
  variables, as any cluster client would.

## Design

```
bench/                       # Go module
  cmd/servtuner/                 # CLI (cobra or stdlib flag)
  internal/
    pipeline/                # the 7 steps above; no engine- or target-specific code
    spec/                    # types generated from ../schema/json + defaults + validation
    engine/
      engine.go              # Engine interface: Prepare(spec) → Run(ctx, phases, sink) → native output dir
      loadgen/               # built-in RPS engine
        pacer/               #   vegeta Pacer reuse + Poisson pacer
        sse/                 #   streaming parser: first generated token (TTFT, any kind), first content, per-chunk times;
                             #   TTFB comes from the worker's httptrace, TPOT from usage
      inferenceperf/         # renders inference-perf config, runs the pinned CLI, parses its reports
    target/                  # TargetAdapter: Snapshot(), Info(), DefaultCatalogs()
                             #   vllm, sglang, litellm, bifrost, openaicompatible
    prometheus/              # range queries, retries, /api/v1/targets, optional-catalog probing
      catalogs/*.yaml        # versioned PromQL (embedded with go:embed)
    tlsconfig/               # TLS on/off, client certificate, server clientAuth policy; trust = system pool + ca.crt
    storage/                 # BundleStore interface; backends: file (default, data dir), s3 (any S3-compatible)
    workload/                # seeded synthetic prompts, chars-per-token calibration, workload hash
    plan/                    # capacity planner over capacity_model.json
    launcher/                # servtuner launcher: start, stop and report on vllm serve on a plain host (the tuning loop)
    propose/                 # tuning loop's next-configuration choice: search space, boot results, tiers, advisor steps, grid, confirm
    analyze/                 # quality, aggregate, capacity, sizing, queueing, charts,
                             #   advisors/{vllm,sglang,bifrost,litellm,cross}, compare, report, rules/*.yaml
  testdata/                  # recorded engine outputs, Prometheus responses, specs
```

- **Engines** may only generate load and report raw results. They must not
  query Prometheus or write summaries, because those are harness concerns, and
  keeping them in the harness keeps engines interchangeable.
- **The loadgen engine runs in-process.** It writes per-request records through
  the `sink` while the run is in progress, and samples its own goroutine count,
  in-flight requests and send lag every second.
- **inference-perf is the only non-Go piece.** It is a pinned Python CLI
  installed in the image, isolated in its own virtualenv. `bench` writes its
  config YAML, runs it as a child process with the TLS settings for the run's
  mode, and parses its JSON reports. No Python code lives in this folder.
- **Adding a target type** means one `TargetAdapter` plus one catalog file.
  Catalog entries list alternate metric names for different target versions
  (vLLM renames metrics), and the collector records which name matched.
- The bench pod's own CPU and memory are always collected. Together with send
  lag, they set `client_bound`.
- **Which engine.** Use loadgen for routers and for any run whose offered rate
  exceeds what calibration showed inference-perf can sustain. Use
  inference-perf when dataset realism matters (vLLM).
- **Scrape intervals.** Record the effective interval per job in
  `prometheus/targets.json`. Query steps, `rate()` windows, minimum stage
  length, warmup and settle delay are derived from it, never hardcoded
  (a stage needs at least 10 scrapes of the slowest relevant job).
- **Capacity answers.** Max sustainable RPS at the SLO, RPS per replica, and
  stage validity are computed in `analyze/` from raw records.

## Constraints

- **Go only** in this folder, using the latest stable Go version and
  `go vet`/`staticcheck` clean.
- Keep the dependencies few and deliberate:
  - client-go,
  - vegeta (pacers only),
  - parquet-go,
  - the Prometheus HTTP API client,
  - the AWS SDK v2 S3 client, used generically against any S3-compatible
    endpoint, with path-style and checksum mode coming from config. Credentials
    use the SDK default chain (Pod Identity, IRSA) or a Secret, never anonymous
    access.
- Pin engine versions exactly. That covers the inference-perf version
  installed in the image, because engine CLIs and output formats change between
  releases.
- Types for the spec and bundle files are **generated** from `../schema/json`.
  Never edit generated files; CI fails if regenerating them changes anything.
- **Transport security**. Each outbound hop (target, Prometheus, API, object
  store) answers TLS's two questions (RunSpec `tls: {enabled, clientCert,
  caSecretRef}`, or the `--*-tls` flags):
  - Is it encrypted? `enabled: true` verifies the server over `https://`;
    `enabled: false` is plain `http://` (a mesh or an encrypted CNI encrypts
    it, or it is a local test) and takes no certificate settings.
  - Client certificate: presented exactly when one is named (`clientCert`),
    and the run's files must match the spec (`tlsconfig.SpecHop`). A private
    CA comes from `caSecretRef`'s `ca.crt`.
  - Servers servtuner runs (the launcher) set `clientAuth`: `required`,
    `optional` (a certificate that is sent is verified) or `none`
    (`tlsconfig.ServerConfig`).

  Rules:
  - Nothing is inferred. `servtuner validate` rejects mixing TLS and plain
    settings on one connection, and every hop's `{tls, client_cert}` is
    written to `environment.json`.
  - servtuner 0.1 RunSpecs (`servtuner.dev/v1`, `tls.mode`) are rejected with
    their replacement; bundles 0.1 wrote are upgraded when read
    (`spec/legacy.go`), never rewritten.
  - Go clients trust the system pool plus `ca.crt`.
  - For the inference-perf child process, `bench` passes `cert_path`/`key_path`
    and writes a combined CA bundle (system plus `ca.crt`) to a temp file. It
    then sets `SSL_CERT_FILE` on the child process only.
  - Skipping verification is never an option.
  - Each client is tested with and without TLS and with and without a client
    certificate: it must refuse an untrusted server, and a server that requires
    a certificate must refuse it without one. Each server policy is tested
    against no certificate, a good one and an untrusted one (`optional`
    refuses the untrusted one). The tests
    use generated certificates and `httptest`, the same pattern as the
    bifrost-benchmarking change.
- Never write secrets into the bundle or logs. Redact env vars by default, with
  an allowlist.
- Tests run offline against `testdata/`. There is an end-to-end test against
  the mock target from `deploy/`.

## Kubernetes runtime

- Image `servtuner-bench`: the static `servtuner` binary, plus inference-perf at a
  pinned version (0.7.0, which needs Python ≥ 3.12) in a venv at
  `/opt/inference-perf`. The two are kept in separate layers. The runtime base
  is `python:3.12-slim` (glibc), not Alpine, because the venv needs glibc.
  The image is about 880 MB.
- It runs as a Job with `backoffLimit: 0`, because a retried benchmark is a
  different run. The spec is mounted from a ConfigMap, and the run token from a
  Secret.
- It is scheduled away from target nodes (anti-affinity) with guaranteed QoS,
  so the client doesn't compete with the target.
- Its ServiceAccount has read-only access to pods, deployments, statefulsets,
  HPAs, nodes and configmaps in the target namespaces.
- With a client certificate, its Secret (`kubernetes.io/tls`, from
  cert-manager or named by the user) is mounted read-only. `bench` builds the
  CA bundle itself, so it needs no init container. Without TLS, nothing is
  mounted.
