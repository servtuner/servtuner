# servtuner chart

Render tests: `deploy/helm/test.sh` (offline; contacts no cluster). Values are documented in `values.yaml`.

## Ingress

`ingress.enabled` renders two ingress-nginx Ingresses for `ingress.host`: the UI under `frontend.basePath` goes to
the frontend, and the api path (prefix stripped) goes to the api. The api path is `<basePath>/api` by default (for
example `/servtuner/api`), so the image's relative `config.json` default (`api`, resolved against the base URI) reaches it
under any context path, including one a proxy adds with `X-Forwarded-Prefix`; `config.json` is not overridden. Setting
`frontend.apiBaseUrl`, or an explicit `ingress.apiPath` (never `/`), writes `config.json` from the chart. When the ingress -> api hop uses
TLS, the api Ingress uses backend TLS: `backend-protocol: HTTPS`, `proxy-ssl-secret` is the chart's ingress
certificate Secret (it must carry `ca.crt`; with `tls.source: secret` that is your responsibility),
`proxy-ssl-verify: "on"`, and `proxy-ssl-name` is the api Service's `<name>.<namespace>.svc` certificate SAN. Without
TLS it is plain HTTP. No snippet annotations are used. The api certificate also carries the SAN
`localhost`, because api settings give the SubprocessRunner's bench `https://localhost:<port>`; with `tls.source: secret`
your api certificate must include `localhost` too while the SubprocessRunner is used. `ingress.annotations` may not
contain `backend-protocol` or `proxy-ssl-*` (the chart owns them), and an explicit `ingress.apiPath` may not equal or
prefix the UI path.

## Run Secrets with the subprocess runner

bench reads the Secrets a RunSpec names (`target.auth`, `target.admin`, an upstream key) from
`/etc/servtuner/secrets/<name>/<key>`. The Kubernetes runner mounts them into each Job. With
`runner: subprocess`, bench runs inside the api pod, so list them in `subprocess.secrets` and the
chart mounts each one read-only there (mode 0440, the pod's `fsGroup`):

```yaml
runner: subprocess
subprocess:
  secrets: [vllm-key]      # Secrets in the release namespace; names only
```

A run that names a Secret not listed fails at resolve with "secret unavailable". Rendering fails
when `subprocess.secrets` is set under another runner.

## User auth and NetworkPolicies

`auth.*` and `networkPolicy.*`:
- `auth.mode: local` (the default) checks every route. Create the first admin with the setup
  token, which `NOTES.txt` shows how to read; `auth.setupToken.existingSecret` names your own
  Secret instead of the one the chart creates and keeps. `auth.mode: none` checks nothing.
- `auth.methods` lists `local` (passwords and a session cookie) and `forward_header` (a proxy
  in front names the user in `auth.forwardHeader.userHeader`). The api trusts that header from
  whatever reaches it, so the proxy must overwrite client copies.
  - Example, oauth2-proxy with ingress-nginx: annotate the api Ingress with
    `nginx.ingress.kubernetes.io/auth-url: http://oauth2-proxy.<ns>.svc/oauth2/auth`,
    `auth-signin: https://<host>/oauth2/start?rd=$escaped_request_uri` and
    `auth-response-headers: X-Auth-Request-User`, and set
    `auth.forwardHeader.userHeader: X-Auth-Request-User`.
  - Client certificates: let the ingress verify them and pass the subject DN in the user header.
- **Gateway API** (`gateway.*`; ingress-nginx is retired): HTTPRoutes attached to
  `gateway.parentRefs`, for `gateway.hostname`, in place of the Ingresses. With TLS, Envoy
  Gateway's `Backend` presents the chart's ingress certificate to the api and a `BackendTLSPolicy`
  checks the api's name (install Envoy Gateway with `enableBackend`). Forward-header auth goes through
  Envoy's external authorization: `gateway.envoy.extAuth` renders a `SecurityPolicy` whose
  `headersToBackend` replaces any client copy.
- `networkPolicy.enabled` (default true) lets only the ingress controller
  (`networkPolicy.ingressController`) and bench Jobs reach the api's port, and nothing reach a
  bench pod. It needs a CNI that enforces NetworkPolicy.
- Recovery: `kubectl exec deploy/<release>-api -- servtuner-api admin reset-password <user>`
  (also `set-role` and `enable`).

## Air-gapped and mirrored installs

`global.imageRegistry` replaces the registry host of every image the chart renders (api, frontend,
bench Jobs, the CA init container, `postgres.cnpg.imageName`) and keeps the repository path, the way
`hauler store copy` names images. With it set, `postgres.cnpg.imageName` is required (the operator's
default image names a public registry). `global.imagePullSecrets` reaches every pod, bench Jobs
included. `offline: true` stops the public datasheet fallback and refuses AWS Price List pricing.
The chart has no subcharts: cert-manager, CloudNativePG, Envoy Gateway and Prometheus are separate
releases (`deploy/helmfile.yaml`). The whole procedure is in `INSTALL.md`.

## Environment contract

`templates/configmap.yaml` renders the api's configuration. The api reads these variables, and its
`KubernetesJobRunner` passes the relevant ones through to bench Jobs. api and bench adopt these names.

| Name | Meaning | Values | Read by |
|---|---|---|---|
| `SERVTUNER_BASE_PATH` | context path the UI is hosted under (`frontend.basePath`); validated at start, an invalid value stops the container. Served under this path and also at `/`, so an ingress may pass the prefix through or strip it | `/` (default), `/servtuner/`, `/tools/servtuner/ui/` | frontend |
| `SERVTUNER_SCHEMA_DIR` | directory of the RunSpec and event schemas (`schema/json`); the api image sets it, the chart does not | path | api |
| `SERVTUNER_API_PORT` | api listen port | integer | api |
| `SERVTUNER_SECRET_STORE` | where saved targets' keys live (`secretStore`): Secrets in the runs namespace, files under the data directory (local development only), or nowhere | `kubernetes` (chart default), `file`, `none` (the api's own default) | api |
| `SERVTUNER_RUN_SECRETS_DIR` | where a subprocess run or a target check gets the Secrets it names, as files for `servtuner run --secrets-dir`; deleted when the run or check ends. The chart mounts a memory-backed `emptyDir` there | path; default `<data dir>/run-secrets` | api |
| `SERVTUNER_RUNNER` | runner implementation. The api's own default is `subprocess` (dev, single node, CI); the chart sets `kubernetes` | `subprocess`, `kubernetes` | api |
| `SERVTUNER_BENCH_BINARY` | path to the `servtuner` binary; required for the `subprocess` runner | path | api (runner) |
| `SERVTUNER_API_URL` | URL bench uses to reach the api. Subprocess runner: default `https://localhost:<port>` (`http://` without TLS). Kubernetes runner: required; the chart sets the api Service, `<scheme>://<fullname>-api.<namespace>.svc:<port>` | URL | api (runner) |
| `SERVTUNER_ENGINE_OVERRIDE` | **dev and e2e only, never set by the chart.** Passed to bench as `--engine-override`, so a run needs no real load engine | `null` | api (runner) |
| `SERVTUNER_RUNS_NAMESPACE` | namespace for bench Jobs, ConfigMaps, Secrets | namespace name (release namespace by default) | api |
| `SERVTUNER_BENCH_SERVICE_ACCOUNT` | ServiceAccount for bench Jobs | name | api (runner) |
| `SERVTUNER_BENCH_IMAGE` | bench image for Jobs | image reference | api (runner) |
| `SERVTUNER_IMAGE_REGISTRY` | `global.imageRegistry` without a trailing slash, or empty; offered as the registry of managed vLLM images | host[:port][/path] | api |
| `SERVTUNER_OFFLINE` | `offline`: no public datasheet fallback, AWS pricing refused; passed to bench Jobs | `true` / `false` | api, bench |
| `SERVTUNER_BENCH_CPU`, `_MEMORY` | the bench pod's requests, equal to its limits (Guaranteed QoS); `bench.resources` | Kubernetes quantities; default `2`, `2Gi` | api (runner) |
| `SERVTUNER_BENCH_TARGET_ANTI_AFFINITY` | keep the bench pod off the nodes of the workloads under test (`target.workload` and every upstream's); `bench.targetAntiAffinity` | `required` (default), `preferred` (single-node clusters) | api (runner) |
| `SERVTUNER_BENCH_JOB_TTL_SECONDS` | `ttlSecondsAfterFinished` of bench Jobs; the spec ConfigMap and token Secret are owned by the Job and go with it | seconds; default `3600` | api (runner) |
| `SERVTUNER_BENCH_START_TIMEOUT_SECONDS` | a bench pod that has not started by then (unschedulable, image pull failing) fails the run with the reason, and its Job is deleted | seconds; default `300` | api (runner) |
| `SERVTUNER_IMAGE_PULL_SECRETS` | `global.imagePullSecrets` and `imagePullSecrets` names for bench pods, comma-separated, each once | names | api (runner) |
| `SERVTUNER_DATA_FS_GROUP` | the bench pod's `fsGroup`, so it can write the data volume (`api.fsGroup`) | integer; default `10001` | api (runner) |
| `SERVTUNER_PROMETHEUS_URL` | Prometheus base URL, used as a RunSpec default | URL | api, bench |
| `SERVTUNER_TUNING_MONITOR` | How Prometheus finds a managed vLLM: `podmonitor` (Prometheus Operator) or `scrape-annotations` (community chart), from `tuning.monitor` | `podmonitor` \| `scrape-annotations` | api |
| `SERVTUNER_AUTH_MODE` | User auth: `local` checks every route; `none` checks nothing, the explicit opt-out | `local` (default) \| `none` | api |
| `SERVTUNER_AUTH_METHODS` | How users sign in under `local`, in chain order | comma list of `local`, `forward_header`; default `local` | api |
| `SERVTUNER_AUTH_SETUP_TOKEN_FILE` | The setup token that creates the first admin (read from a Secret file, never stored); without it no admin can be created | path | api |
| `SERVTUNER_AUTH_SESSION_IDLE_SECONDS` | A session ends after this long unused | seconds; default 43200 | api |
| `SERVTUNER_AUTH_SESSION_ABSOLUTE_SECONDS` | A session ends this long after sign-in, whatever its use | seconds; default 604800 | api |
| `SERVTUNER_AUTH_COOKIE_SECURE` | `false` only for a plain-HTTP install not on localhost: the cookie then loses `Secure` and the `__Host-` prefix | `true` (default) \| `false` | api |
| `SERVTUNER_AUTH_LOCKOUT_MAX_FAILURES` | Failed logins per username before it is locked | integer; default 5 | api |
| `SERVTUNER_AUTH_LOCKOUT_WINDOW_SECONDS` | The window failures are counted in, and how long the lock lasts | seconds; default 900 | api |
| `SERVTUNER_AUTH_FORWARD_USER_HEADER` | The header the trusted proxy names the user in (forward_header) | header name; default `X-Forwarded-User` | api |
| `SERVTUNER_AUTH_FORWARD_DISPLAY_NAME_HEADER` | Optional header with a new user's display name | header name | api |
| `SERVTUNER_AUTH_FORWARD_EMAIL_HEADER` | Optional header with a new user's email | header name | api |
| `SERVTUNER_AUTH_FORWARD_PROVISIONING` | What a forwarded subject with no servtuner user gets | `refuse` (default) \| `viewer` | api |
| `SERVTUNER_TUNING_NAMESPACES` | Namespaces where managed vLLM targets may be stored and deployed, from `rbac.tuning.namespaces`; unset grants none | comma-separated names | api |
| `SERVTUNER_STORAGE_BACKEND` | bundle store | `file`, `s3` | api, bench |
| `SERVTUNER_DATA_DIR` | data directory (file backend) | path | api, bench |
| `SERVTUNER_DATA_CLAIM` | PVC to mount in Jobs at the data directory (file backend) | claim name | api (runner) |
| `SERVTUNER_STORAGE_NODE_PINNED` | data volume is RWO, so Jobs must share the api's node | `true`, `false` | api (runner); recorded as `storage.node_pinned` |
| `SERVTUNER_API_COMPONENT_LABEL` | `key=value` label selecting the api pod, for the runner's RWO pod affinity | label selector | api (runner) |
| `SERVTUNER_S3_ENDPOINT`, `_REGION`, `_BUCKET`, `_PREFIX` | S3 location (s3 backend; endpoint empty for AWS) | strings | api, bench |
| `SERVTUNER_S3_FORCE_PATH_STYLE` | path-style addressing | `true`, `false` | api, bench |
| `SERVTUNER_S3_CHECKSUM_MODE` | checksum behaviour | `when_required`, `when_supported` | api, bench |
| `SERVTUNER_S3_PART_SIZE_MIB` | multipart part size for uploads (`storage.s3.partSizeMiB`) | whole MiB, at least 5; default 16. A larger file goes up in parts, aborted on failure | bench (the api passes it to Jobs) |
| `SERVTUNER_S3_PRESIGN` | download links (`storage.s3.presign`) | `true`: `GET /runs/{id}/files/{path}` answers 307 to a 5-minute presigned GET (SigV4); `false` (default): the api streams the file | api |
| `SERVTUNER_S3_CREDENTIALS_SECRET`, `_ACCESS_KEY_KEY`, `_SECRET_KEY_KEY` | the Secret (in the runs namespace) and the two keys bench Jobs read `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` from, by reference; credentials source `secret` only | Secret name, key names | api (runner) |
| `SERVTUNER_S3_CREDENTIALS_SOURCE` | credential source | `secret` (keys in `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`), `ambient` (default credential chain) | api, bench |
| `SERVTUNER_TLS_ENABLED` | whether hops use TLS: each hop's default, never inferred | `true`, `false` | api |
| `SERVTUNER_TLS_CLIENT_AUTH` | the api listener's client-certificate policy (`ingressApi`, `benchApi`); bench presents its certificate unless it is `none` | `required` (default), `optional`, `none` | api; runner |
| `SERVTUNER_TLS_HOP_BENCH_API_ENABLED`, `_BENCH_PROMETHEUS_ENABLED`, `_INGRESS_API_ENABLED`, `_OBJECT_STORE_ENABLED`, `_POSTGRES_ENABLED` | whether each hop uses TLS | `true`, `false` | api; runner passes bench's hops to Jobs |
| `SERVTUNER_TLS_HOP_BENCH_PROMETHEUS_CLIENT_CERT`, `_OBJECT_STORE_CLIENT_CERT`, `_POSTGRES_CLIENT_CERT` | whether servtuner presents its client certificate to a server it does not run; `true` by default with TLS | `true`, `false` | api; runner |
| `SERVTUNER_TLS_CERT_FILE`, `SERVTUNER_TLS_KEY_FILE` | api certificate and key (mounted `kubernetes.io/tls` Secret) | path; set only when a hop uses the api's certificate | api |
| `SERVTUNER_TLS_CLIENT_CA_FILE` | private CA the api listener verifies client certificates against (never the combined bundle, which admits public CAs). Default: `ca.crt` beside `SERVTUNER_TLS_CERT_FILE`. The chart sets it when the api listener checks client certificates (TLS, and `tls.clientAuth` not `none`), pointing at the `ca.crt` of the CA Secret (`tls.caSecretRef`, else the api certificate Secret) | path; only when client certificates are checked | api |
| `SERVTUNER_TLS_BENCH_SECRET` | name of the bench client-certificate Secret, in the runs namespace | Secret name; set only when needed | api (runner) |
| `SERVTUNER_TLS_CA_SECRET` | Secret in the runs namespace whose `ca.crt` verifies the api for bench (`servtuner run --tls-ca-file`): `tls.caSecretRef`, else the bench Secret (cert-manager writes the issuing CA into it). With `tls.caSecretRef` and a separate runs namespace, that Secret must exist in the runs namespace too | Secret name; only when the bench-api hop uses TLS | api (runner) |
| `POD_NAMESPACE` | the api pod's namespace (downward API), for the RWO pod affinity to the api pod | namespace | api (runner) |
| `SSL_CERT_FILE` | combined system + private CA bundle built by the init container | `/etc/servtuner/ca/bundle.crt`; set only when some hop uses TLS | api (standard) |
| `SERVTUNER_DATABASE_HOST`, `_PORT`, `_NAME`, `_USER` | Postgres connection | strings | api |
| `SERVTUNER_DATABASE_SSLMODE` | Postgres TLS | `verify-full` (client cert), `disable` | api |
| `SERVTUNER_DATABASE_PASSWORD` | database password; set only when the postgres hop presents no client certificate (without TLS, or with `tls.hops.postgres.clientCert: false`) | from a Secret | api |

### Request headers

The frontend container (not an env var, so not in the table above) reads one request header:

| Header | Meaning |
|---|---|
| `X-Forwarded-Prefix` | Context path from a proxy that strips its prefix. For that request it overrides `SERVTUNER_BASE_PATH`. The rule is in `frontend/nginx.conf.template`: each segment is 1 to 64 characters of `A-Z a-z 0-9 . _ ~ -` and is not `.` or `..`. An invalid value, or a duplicated header, is ignored and the env path is used. |
