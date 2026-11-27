# CLAUDE.md — api (control plane)

## Purpose

The API is the system of record for runs and experiments. It:
- accepts RunSpecs,
- launches `bench` through a **Runner**,
- tracks the status of each run,
- indexes summaries for querying and comparison,
- and serves bundle contents to the frontend.

It never generates load and never queries Prometheus itself. Those are `bench`
concerns.

## Design

- **Python 3.14 + FastAPI**, managed with `uv`. Versions are pinned
  exactly in `pyproject.toml` and `uv.lock`, and boto3 must be ≥ 1.36 for the
  checksum options. Pydantic v2 models for the RunSpec,
  bundle files and events are **generated** from `../schema/json`. They are
  never hand-edited, and API-only models wrap them. FastAPI's OpenAPI document
  is the contract with the frontend.
- **Formatting:** Black (`just format`; `just lint` runs `black --check`),
  line length 100, with its config in `pyproject.toml`; ruff only lints.
- **Layout:** `app.py` only assembles the app. Routes live in `routes/`, one
  `APIRouter` per domain (runs, planning, experiments, schedules, targets,
  managed targets, hosts, tuning), with the shared service dependency and
  error mappings in `routes/common.py`; user routes live in `auth/`. A route
  module stays thin: it parses, calls a service, and maps errors to HTTP.
  `RunService` (`service.py`) owns runs and the background loops; its
  sub-services are `schedules` (`schedule_service.py`), `experiments`,
  `scenarios`, `targets`, `managed_targets`, `hosts` and `tuning`. The errors
  routes map live in `errors.py`. Domains with several parts are packages
  (`deployer/`, `managed_targets/`, `tuning/`, `kubernetes_runner/`,
  `settings/`, `records/`, `resources/`, `targets/`); callers import from the
  submodule that defines a name, with no re-export layer.
  Keep modules short. When one passes about 400 lines, split it by concern.
- `bench` is Go, so the API never imports bench code. It talks to bench only
  through the CLI contract (the Runner), the bundle files and status events.
- **Postgres** holds the tables `runs` (id, name, experiment, status, target
  type, knobs JSONB, labels JSONB, timing, bundle URI, spec hash), `run_events`,
  `experiments`, `experiment_analyses`, `scenarios`, `schedules`, and `run_metrics`. `run_metrics` is long-format
  (run_id, stage, metric, stat, value), so comparisons work without schema
  changes. It is derived from each run's `analysis.json`
  (`indexing.py` holds the naming) when the runner reports a final state,
  and a startup backfill indexes finished runs that have no rows yet. Migrations use Alembic. On the cluster, Postgres is a CNPG `Cluster`.
- **Bundle store** (pluggable):
  - `file` is the default, reading the shared data directory.
  - `s3` covers any S3-compatible service, with boto3 configured from the same
    values as bench. On AWS it uses the default credential chain, so EKS Pod
    Identity works with no keys. Postgres
  stores the store name plus key, never an endpoint. Bundles are written by
  `bench`. The API reads
  `summary.json` and `manifest.json` to index them, and proxies or presigns
  access to raw files.

### Runner interface

```python
class Runner(Protocol):
    def launch(self, run: Run, spec: ResolvedSpec) -> RunHandle: ...
    def status(self, handle: RunHandle) -> RunnerStatus: ...   # authoritative final state
    def cancel(self, handle: RunHandle) -> None: ...
    def logs(self, handle: RunHandle) -> Iterator[str]: ...
```

- `KubernetesJobRunner` (`kubernetes_runner/`, lightkube) creates a Job running
  `servtuner run …`, then a ConfigMap (the spec) and a Secret (the run token) owned
  by the Job, so `ttlSecondsAfterFinished` removes all three. Rules:
  - `backoffLimit: 0`: a failed benchmark is a result, and a retry would write
    into the same bundle.
  - Guaranteed QoS (requests equal limits), anti-affinity from every workload in
    the RunSpec (`required`, or `preferred` on one-node clusters), and pod
    affinity to the api's node when the data volume is ReadWriteOnce.
  - Every Secret the RunSpec names is mounted at `/etc/servtuner/secrets/<name>`,
    bench's default. A missing one fails the launch, because a pod with a
    missing Secret volume waits forever instead of failing.
  - Status comes from the Job's `Complete`/`Failed` condition, with the bench
    container's exit code and reason (`OOMKilled`). A pod that has not started
    within `SERVTUNER_BENCH_START_TIMEOUT_SECONDS` (unschedulable, image pull
    failing) fails the run with the scheduler's or kubelet's reason.
  - The Job name is `bench-<run id, lower case>`, so a run's Job can be found
    from its id.
- `SubprocessRunner` runs the same `servtuner run …` command locally, writing to a
  local bundle directory. It is meant for development, a single-node install,
  or CI. In `mtls` mode it reuses the api's own certificate as bench's client
  certificate; the Kubernetes runner uses `SERVTUNER_TLS_BENCH_SECRET`, and trusts
  the api through `--tls-ca-file` with the `ca.crt` of `SERVTUNER_TLS_CA_SECRET`.
  `SERVTUNER_ENGINE_OVERRIDE=null` (dev and e2e only, never set by the chart) makes it
  pass `--engine-override null`, so a run needs no real load engine. `just
  e2e-local-mtls` and `just e2e-local-platform` use it (`e2e/local.py`).

- **Sharded runs** (`engine.shards` N > 1): the Kubernetes runner
  makes the Job `Indexed` with N completions and parallelism, passes
  `--shard-index=$(JOB_COMPLETION_INDEX)` (from the completion-index
  annotation) and prefers to spread the pods over nodes; the subprocess runner
  starts N processes. The leader's (index 0) exit code and log are the run's;
  the subprocess runner stops leftover followers when the leader ends.

The runner is chosen by config. Nothing above the Runner layer knows which
runner is in use.

### Run lifecycle

`pending → launching → running(phase) → collecting → succeeded | failed | cancelled`

At startup the api watches every run left unfinished by an earlier process
(`RunService._rewatch_unfinished`): the Kubernetes runner finds its Job, and the
subprocess runner, whose child ended with the old process, reports it failed
and says why.

`bench` POSTs phase events with its run token. The runner's final status always
wins, so a pod killed by OOM becomes `failed` even if it never reported.

### Endpoints (v1)

```
POST   /runs                      create and launch (body: RunSpec)
GET    /runs?experiment=&target_type=&status=&label=   list and filter; each run carries its usable
                                  slo_max_rps from run_metrics (one query for the page)
GET    /runs/{id}                 metadata + summary
GET    /runs/{id}/events          status timeline
POST   /runs/{id}/events          bench → api (run token auth)
GET    /runs/{id}/delete-impact   what deleting it would remove and what blocks it; deletes nothing
DELETE /runs/{id}                 a user's delete only: the bundle, the analyses that included it, then the
                                  row; 409 {blocks} while it is unfinished, a schedule's baseline, run by an active
                                  session, or under a saved scenario; a retry finishes a half-done delete
POST   /runs/{id}/cancel          the runner stops bench and the run is cancelled at once; a finished run is unchanged
GET    /runs/{id}/files           the bundle's files and sizes
GET    /runs/{id}/files/{path}    bundle file, streamed; or 307 to a presigned GET with storage.s3.presign
GET    /runs/{id}/logs
GET    /runs/{id}/analysis, /recommendations, /capacity, /charts/client_timeseries, /charts/histograms
                                  the typed analysis outputs: 404 until analysed, 502 if a file breaks its schema
GET    /compare?runs=a,b,c        runs aligned by knob and stage from run_metrics (aligns, never ranks)
GET    /experiments               and /experiments/{name}: runs, varied knobs, comparison, latest analysis
GET    /experiments/{name}/delete-impact  and DELETE /experiments/{name}?delete_scenarios=: a user's delete only
                                 : its runs (each blocked as a run is), analyses, finished tuning sessions and
                                  row; 409 {blocks} while a session of it is active; scenarios on its runs block
                                  unless delete_scenarios; managed targets and schedules are never touched
POST   /experiments/{name}/analyze  {objective?, run_ids?}: servtuner compare over the succeeded, analysed
                                  runs; kept in experiment_analyses
POST   /specs/validate            dry-run of `servtuner validate`
GET    /run-defaults              {prometheus?, infra_pricing?, token_pricing?, offline, image_registry?}: the install's
                                  Prometheus and price book as RunSpec blocks (New run fills them in; tuning sessions use
                                  them), whether it is offline and the registry its images come from
GET    /price-book                and PUT: the install's price book: source none | static | aws, discount,
                                  node types, per-GPU prices, aws_region; the response carries the infra_pricing block
                                  new runs and Kubernetes managed targets' runs (run_analysis) get; hosts keep their own;
                                  datasheet_url, or GET/PUT/DELETE /price-book/datasheet: the token-price datasheet the
                                  operator supplies, checked as bench reads it; offline refuses source aws
GET    /runs/{id}/datasheet       the uploaded datasheet for that run's bench: run token only, like POST .../events
GET    /workload-presets          `servtuner presets`: the preset catalog bench carries, read once (503 without a bench binary)
GET    /knobs                     `servtuner knobs`: every vLLM flag that is a knob, with vLLM's own description and limits
GET    /search-spaces             `servtuner spaces`: the starting search spaces, read once, the same way
GET    /managed-targets           and POST, GET/PUT/DELETE /managed-targets/{id}: vLLM templates servtuner deploys
                                  (each carries run_labels, the label its runs and its sessions' runs get); only in SERVTUNER_TUNING_NAMESPACES; one per namespace
                                  spec.model_source: hub (default), mirror (HF_ENDPOINT), pvc (a seeded claim, never
                                  deleted) or object_store (vLLM streams s3/gs/az weights; keys by Secret name);
                                  an offline install sets FLASHINFER_NO_DOWNLOAD on its pods
POST   /managed-targets/{id}/deploy  {knobs}: apply with these vLLM knobs (202); GET .../readiness polls once
                                  (pending | ready | infeasible | failed); POST .../scale-down frees the GPU
                                  and stamps deployment.scaled_down_at (a deploy clears it);
                                  503 without the kubernetes runner, a bench binary and a tuning namespace
                                  PUT, DELETE, deploy and scale-down answer 409 while an active session uses the target
PUT    /managed-targets/{id}/credentials/{launcher_token|api_key}  a host target's token or vLLM key: {value}
                                  (kept in a Secret servtuner owns, servtuner-managed-<id>-<slot>) or {external: {name}};
                                  204, write-only; DELETE clears; the resource shows only where each lives
POST   /managed-targets/{id}/check-launcher  a host target's launcher with its token: ok (and what vLLM runs),
                                  unreachable, refused, no_token or error; read-only
GET    /managed-targets/{id}/logs?tail_lines=  {source, lines}: the end of vLLM's log (default 200, at most 1000):
                                  the newest pod's (a crash-looping one's last exit) or the host launcher's
GET    /hosts                     and POST, GET/PUT/DELETE /hosts/{id}: the machines host targets run on:
                                  hardware (typed, or read by the launcher) and a price (given $/h, or owned:
                                  purchase ÷ hours + watts × $/kWh, with its basis); DELETE 409 while a target names it
POST   /hosts/{id}/read-machine   the machine through the launcher of a host target on it (GET /machine), kept as
                                  the host's hardware; 422 when no target runs on it or the launcher is too old
POST   /tuning-sessions           {managed_target_id, preset, streaming, slos, space, budget, mode, ...}: start a
                                  session (409 while another is active); GET lists, GET /{id} one; POST /{id}/approve
                                  (manual mode) and /{id}/cancel. A background loop advances active sessions;
                                  reuse_from seeds it with earlier sessions' evidence (a host: same vLLM version)
POST   /managed-targets/{id}/boot-probe  {tokens_per_request}: readiness, plus the tuningBootResult once the
                                  boot ended (`servtuner boot-probe` when ready; the failure when infeasible)
POST   /plans                     {request, run_ids}: servtuner plan over the runs' capacity_model.json
POST   /scenarios/evaluate        the same body and errors as /plans: the workbench's recompute (overrides in request)
POST   /scenarios                 {name, note?, scenario: <plans body>}: checked like a plan body, then kept (201)
GET    /scenarios                 and /scenarios/{id}; DELETE /scenarios/{id}. The plan is never stored: it is
                                  recomputed from the bundles, so it follows reanalysis
POST   /schedules                 {name, spec, every_hours, start_at?, baseline_run_id?, regression_tolerance?}
GET    /schedules                 and /schedules/{id}: the schedule with its runs, each judged against the
                                  baseline's SLO-max (regressed / steady / improved); PATCH, DELETE (runs kept)
POST   /schedules/{id}/trigger    launch now; 409 while its last run is unfinished
GET    /targets                   saved targets: POST, GET/PUT/DELETE /targets/{id}; each may carry
                                  pricing (a host or node type by the hour, datasheet or manual tokens) and
                                  answers run_target and run_analysis, what New run copies, with pricing_problems
PUT    /targets/{id}/credentials/{slot}  write-only key (204, no body); DELETE clears it. No route
                                  ever returns a value: responses carry a masked preview only
POST   /targets/{id}/check        servtuner check-target with the stored key, or a one-time api_key
POST   /targets/check             check an unsaved target block (New run)
```

The frontend also needs analysis, chart, recommendation, capacity, patch,
snapshot-diff, follow-up, confirm-applied, capacity-model, plan and `/about`
endpoints. The api image includes the `servtuner` binary, so
`POST /scenarios/evaluate` can run `servtuner plan` synchronously (sub-second).
Plans and tuned recommendations are both **scenarios**: models, inputs and
overrides. Their routes are
listed above.

### Scheduled regression runs

`schedules.py` and `RunService.run_due_schedules`. A thread in the api wakes
every 30 s, claims due schedules with `FOR UPDATE SKIP LOCKED` and advances
them **before** launching, so two replicas never launch one slot. Runs carry
the label `servtuner.schedule=<id>`; the history is a label query. Rules:
- A slot that falls due while the schedule's last run is unfinished launches
  nothing (`last_note` says so): overlapping runs of one target measure each
  other.
- Slots missed while the api was down are skipped, never replayed.
- The verdict compares SLO-max RPS from `run_metrics` with the baseline's (an
  explicit run, else the schedule's first succeeded run with an SLO-max): a
  drop beyond `regression_tolerance` (default 10%) is a regression. Runs with
  no SLO-max or excluded from comparison are not judged.

### User auth

- **Every route passes one guard.** `auth/access.py` lists the access of every route (an
  action, `public` or `authenticated`); `auth/guard.py`'s app-wide dependency refuses an
  unlisted route, and `tests/test_auth_routes.py` fails the build if a route is missing or
  stale. Handlers read `request.state.principal`.
- **Roles are fixed sets of actions** (`auth/principal.py`): viewer reads; operator also
  runs, schedules, scenarios, tuning, target checks and logs; admin also target, host and
  managed-target writes, credentials, deletes, users. Routes check actions, never roles.
- **`SERVTUNER_AUTH_MODE`:** `local` (the default; fails closed: no setup token file, no admin)
  or `none` (checks nothing; `create_app`'s default, so tests that are not about auth keep
  their behaviour).
- **Methods** (`SERVTUNER_AUTH_METHODS`, in chain order): `local` (Argon2id passwords,
  `auth/passwords.py`; server-side sessions stored as SHA-256 in `user_sessions`, cookie
  `__Host-servtuner_session`) and `forward_header` (a proxy's subject mapped to a user through
  `user_identities`, exact match; trust is implicit, narrowed by the chart's
  NetworkPolicies).
- **The job route** (`POST /runs/{id}/events`) takes only a run token: a cookie gets 403, a
  forward header 400. The token is minted when the job launches (`RunService._launch`).
- **`created_by`** is stamped on every insert from the acting user the guard sets
  (`auth/attribution.py` and one `before_insert` hook in `database.py`); background threads
  have none.
- **Recovery:** `servtuner-api admin reset-password | set-role | enable <user>`.
- **Tests:** `tests/support/auth.py` builds an app with auth on (a setup token file, a clock
  to move, users made in the store); its client speaks https so the Secure cookie is sent.

```
GET    /auth/status               public: mode, methods, setup_required, the user and their actions,
                                  or the subject the proxy named that has no user
POST   /auth/login | /auth/logout public (same-origin check); local accounts only
POST   /auth/setup                public: X-Servtuner-Setup-Token; {username, password}, or {} to make the
                                  forwarded subject the admin; 409 once any admin exists
GET    /auth/me, POST /auth/password, GET /auth/sessions, DELETE /auth/sessions/{id},
POST   /auth/sessions/revoke-others   a signed-in user's own account
GET    /users, POST, GET/PATCH /users/{id}, POST /users/{id}/password (temporary, shown once),
POST/DELETE /users/{id}/identities, DELETE /users/{id}/sessions, GET /audit   users.admin
```

## Constraints

- RunSpecs are stored exactly as submitted. Any edit creates a new run, and a
  re-run is a new run with the same spec hash.
- Secrets are only referenced by name. Run tokens are scoped to a single run
  and expire.
- **Transport security per hop**. Each hop is `mtls` (the default)
  or `platform`, set in config and never inferred:
  - The API serves either mTLS (requiring client certs from bench and the
    ingress) or plain HTTP.
  - To Postgres it uses `verify-full` with a client cert, or `sslmode=disable`.
  - To the object store it uses mTLS, or plain HTTP.

  Rules:
  - Certificates are read from mounted `kubernetes.io/tls` Secret files, and
    reloaded when cert-manager rotates them.
  - CA trust for **outbound** connections (Postgres) comes from the combined
    bundle in `SSL_CERT_FILE`. The listener verifies **client** certificates
    against the private CA only (`SERVTUNER_TLS_CLIENT_CA_FILE`, default `ca.crt`
    beside `SERVTUNER_TLS_CERT_FILE`), never the combined bundle, which would
    admit any publicly issued certificate.
  - The per-run token for bench is required in both modes.
  - The `KubernetesJobRunner` passes each hop's mode and Secret names through
    to the Job.
  - Tests cover both modes, and rejecting a client with no certificate in
    `mtls` mode.
- RBAC: the API's ServiceAccount can manage Jobs, ConfigMaps and Secrets **only
  in the bench namespace**.
- Tests use a fake Runner and a temporary Postgres (testcontainers). The
  Kubernetes runner is tested against `kind` in CI only.
