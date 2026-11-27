# CLAUDE.md — servtuner

servtuner is a benchmarking platform for LLM serving stacks running on Kubernetes:
vLLM, LiteLLM, Bifrost and other LLM routers. Its purpose is to find the best
**configuration** for each application and the right **resource sizing** (CPU,
memory, GPU memory) for it, using evidence from repeatable benchmark runs.

User documentation lives at https://docs.servtuner.dev; `INSTALL.md` is the
operator's install guide and ships in every release archive.

## Components

| Folder | What it is | Runs as |
|---|---|---|
| `bench/` | The load-testing tool: **one Go binary**. Its harness drives the built-in **loadgen** engine (RPS-focused) or inference-perf (an external pinned CLI). It executes one run end to end and writes the run bundle. | A Kubernetes Job pod, **or** a local subprocess |
| `api/` | The control plane, in **Python with FastAPI**. It stores runs, launches `bench`, and serves results. | A Deployment |
| `frontend/` | A single-page app for creating, watching and comparing runs. | Static files served by a Deployment |
| `schema/` | The shared contract, in **JSON Schema**: the RunSpec, the bundle files and status events. | Go, pydantic and TS types are generated from it |
| `deploy/` | The Helm chart, local `kind` setup and mock targets. | — |

Each folder has its own `CLAUDE.md` that covers its purpose and design.

## Core concepts (shared vocabulary)

- **Target**: the system under test. A target has a type (`vllm`, `litellm`,
  `bifrost`, `openai-compatible`), an endpoint, and a reference to its
  Kubernetes workload so that its configuration and resources can be captured.
- **RunSpec**: the complete, immutable description of a benchmark. It covers
  the target, workload, load profile, engine, Prometheus queries and SLOs.
- **Run**: one execution of a RunSpec. It is identified by a ULID. A run records
  everything needed to reproduce and re-analyse it.
- **Run bundle**: the directory or object prefix that holds all of a run's raw
  data. Raw data is never thrown away; summaries are always derived from it.
- **Experiment**: a named group of runs that varies one or more **knobs**
  (target configuration options or load parameters) so the runs can be compared.
- **Engine**: the load generator that `bench` drives: loadgen (built in) for
  routers and high RPS, inference-perf for model servers. Other engines plug in
  through an adapter. When the target needs a client certificate, an engine
  without client-cert support goes through bench's loopback **TLS relay**.

## Architectural rules

1. **The API ↔ bench contract is a CLI plus files, not Kubernetes.** The API
   runs `servtuner run --spec spec.json --out <dir|s3://…>`. A Job pod and a local
   subprocess run exactly the same command, so supporting a new runner never
   requires a change to `bench`.
2. **Raw first.** `bench` writes raw per-request records and raw Prometheus
   series before it computes any summary. The API indexes summaries in
   Postgres, but the bundle is the source of truth. servtuner never throws raw
   data away by itself; only a user may delete a run or an experiment.
3. **Client-side metrics measure user experience; server-side metrics explain
   it.** TTFT, end-to-end latency and RPS come from the client. Prometheus
   explains why: queueing, KV cache, router overhead and resource usage.
4. **Never store secrets in a run.** The RunSpec references Kubernetes Secrets
   by name. Captured env vars and headers are redacted.
5. **All timestamps are UTC** and recorded with the clock source used.
   Prometheus queries use the run's recorded window, not wall-clock guesses.
6. **Never assume the network is encrypted.**
   - Every connection uses TLS or not (`tls.enabled`). Each component has one
     certificate and presents it on every connection with TLS; a server
     servtuner runs requires one, verifies one if sent, or asks for none
     (`clientAuth`: `required`, `optional`, `none`). servtuner's own
     connections default to TLS with client certificates required. A RunSpec
     target is someone else's server, so it names its own certificate
     (`clientCert`). Postgres sign-in is `postgres.auth`: certificate or password.
   - TLS off (plain on the wire, for a CNI or mesh that encrypts, or testing)
     is an explicit, per-connection opt-in. What each connection did is
     recorded in every run, and TLS settings never appear on a connection
     without TLS.
   - Every service has its own certificate. A managed vLLM is plain HTTP by
     default, with TLS (no client certificates yet) as its opt-in.
   - Certificates come from `kubernetes.io/tls` Secrets, issued by cert-manager
     or named by the user. Components read files only.
   - CA trust uses a combined system-plus-private bundle in `SSL_CERT_FILE`.
     There is no `insecure-skip-verify`.
   - Every client is tested with and without TLS and with and without a client
     certificate; every server policy against no, good and untrusted certificates.
7. **RPS numbers must be trustworthy.**
   - A stage counts only if achieved RPS matched offered RPS, send lag stayed
     low, and the client wasn't the bottleneck (`client_bound`).
   - The harness computes capacity answers across stages; engines never do.

## Commands

Toolchain pins are in `.tool-versions`; the Justfile and CI read them from there.
Root recipes run the same-named recipe of every component, in the order
`schema bench api frontend deploy`.

- `just setup`: toolchains and dependencies.
- `just gen`: regenerate everything generated from `schema/` and the api.
  `just gen-check` fails when that changes the tree (CI runs it).
- `just lint`, `just test`: every component, plus a jscpd duplication check.
- `just e2e-smoke`: the UI's Playwright smoke test against the real api and bench.
- `just e2e-local-tls-off` / `e2e-local-mutual-tls`: api plus the real bench binary, no cluster.
- `just e2e-tls-off` / `e2e-mutual-tls`: the chart on a kind cluster.
- `just images`, `just release <version>`, `just e2e-airgap <version>`: images,
  the air-gapped release archive, and its proof on a kind cluster with no egress.
- Releases are published by `.github/workflows/release.yml` from a `vX.Y.Z` tag;
  `RELEASING.md` has the steps. `docker-bake.hcl` defines the images for both
  `just images` and the release; `.goreleaser.yaml` builds the CLI.

## Conventions

- **Generated code is never edited by hand.** Change `schema/json/` (or the
  api's models for the OpenAPI document), then `just gen`.
- **Define things once.** A constant, a name or a rule has one home that every
  component reads; jscpd fails the lint on copied blocks.
- **Literal constants carry a comment** that says what the number is and where
  it comes from: `maxLogTail = 200 // lines the boot classifier reads`, never
  a bare `1 << 20`.
- **Every TLS setting is tested.** A new client gets tests with and without TLS
  and a client certificate; a new server, each `clientAuth` it supports.
- **No secrets, addresses or hostnames in committed files.** Values files name
  Kubernetes Secrets that the operator creates first; examples use placeholders
  and `*.localtest.me`.
- **Pin versions exactly**, including container images (by digest in
  `deploy/release/images.lock`). Ask before adding a dependency.
- Comments explain *why*. They never point at documents outside this
  repository, except https://docs.servtuner.dev.
