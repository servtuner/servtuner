# CLAUDE.md — deploy

## Purpose

Everything needed to run servtuner on Kubernetes and to develop it locally:
- the Helm chart,
- RBAC,
- a `kind` dev environment,
- and **mock targets**, so that the full pipeline can be exercised without
  GPUs.

## Layout

```
helm/servtuner/             # api, frontend, (optional) CNPG Cluster + dev RustFS, RBAC,
                         #   cert-manager Certificates, CA bundle ConfigMap, data-dir PVC (file backend)
aws/                     # example IAM policies (bench: S3 + pricing:GetProducts; api) + PodIdentityAssociation / ACK examples
pricing/                 # example static infra price table ConfigMap (incl. owned-hardware amortization helper)
dcgm/                    # optional: custom counters CSV ConfigMap (adds SM_ACTIVE/SM_OCCUPANCY, clock events)
                         #   + values snippets for collect interval 5000ms (GPU Operator and standalone chart)
  templates/monitors/    # Prometheus Operator: ServiceMonitor/PodMonitor per target type, interval: 5s
                         #   (rendered only if monitoring.coreos.com/v1 exists)
prometheus/              # community `prometheus` chart: extraScrapeConfigs snippet (servtuner-targets 5s job,
                         #   optional fast cAdvisor job); targets opt in with label servtuner.dev/scrape=true
                         #   (litellm /metrics: bearer key from a Secret; bifrost with auth_config: basic auth from a Secret;
                         #   the bifrost chart ships no ServiceMonitor, so these cover it)
launcher/                # servtuner launcher as a systemd user service on a GPU host outside Kubernetes
kind/                    # cluster config + kube-prometheus-stack values for local dev
mocks/
  openai-mock/           # OpenAI-compatible streaming mock with configurable TTFT/ITL/latency; exposes /metrics
  examples/              # RunSpecs: direct mock, bifrost→mock, litellm→mock, vllm (GPU clusters)
```

## Design

- Namespaces: `servtuner` (api and frontend). Bench Jobs run in the same
  namespace by default (`namespaces.runs` empty), or in a separate one such as
  `servtuner-runs`. The chart resolves the effective runs namespace in one helper.
  Target namespaces belong to the user.
- RBAC follows least privilege:
  - The API manages Jobs, ConfigMaps and Secrets in the runs namespace only.
  - The bench ServiceAccount has read-only access to workloads and nodes in the
    target namespaces listed in values.
- **Storage**:
  - **Default: the `file` backend** on a PVC mounted at `storage.file.dataDir`
    in the api pod and every bench Job.
    - `ReadWriteMany` storage is required for multi-node scheduling.
    - With `ReadWriteOnce` only, bench Jobs are pinned to the api's node with
      pod affinity, and the chart prints a warning.
    - Rendering fails with `file` storage and no PVC.
    - **One claim, one namespace.** A PersistentVolume binds to a single claim
      and an RWO volume can't be shared across namespaces, so `pvc.create`
      renders ONE claim in the release namespace, and rendering fails if the
      runs namespace differs. With a separate runs namespace, use
      `existingClaim` with `accessMode: ReadWriteMany`, and provide a
      same-named claim in both namespaces on the same RWX storage (for example
      two static PVs on one NFS export). With `s3`, separate namespaces are fine.
    - The `SERVTUNER_*` environment contract with api and bench is documented in
      `helm/servtuner/README.md`.
  - **AWS S3 with EKS Pod Identity:**
    - The chart creates the `servtuner-api` and `servtuner-bench` ServiceAccounts
      and prints the namespace and ServiceAccount pairs that need a
      `PodIdentityAssociation`.
    - It optionally renders ACK `PodIdentityAssociation` resources.
    - It ships example least-privilege IAM policies for each role in
      `deploy/aws/`.
- **Postgres** comes from the **CloudNativePG (CNPG) operator**. The chart
  renders a CNPG `Cluster` (`postgres.mode: cnpg`) or points at an existing
  database (`postgres.mode: external`).
  - The CNPG operator is a cluster prerequisite and is not bundled. The chart
    fails rendering in `cnpg` mode when the `postgresql.cnpg.io` CRDs are
    missing.
  - In `mtls` mode the CNPG `Cluster` is configured for client-certificate
    auth, with certs from the same cert-manager or user-Secret source.
  - **No Bitnami charts or images anywhere.**
- The dev object store is optional: `storage.dev.deploy` is `rustfs` or `none`
  (the default), using the official RustFS chart at a pinned version. MinIO
  isn't offered, because its community distribution is archived.
  - In production, point `storage.s3.*` at any S3-compatible service,
    including AWS S3 with ambient IAM credentials.
  - Nothing in the templates is specific to one store's vendor.
- The Prometheus URL is a chart value and is passed into RunSpec defaults.
  Scrape intervals are **not** assumed; bench discovers them.
  - With the **Prometheus Operator**, the monitor templates set `interval: 5s`
    for targets under test.
  - With the **community chart**, `prometheus/` provides the `servtuner-targets`
    job.

  Neither touches the global interval. A run's stages must last at least 10 scrapes of
  the slowest relevant job; `servtuner validate` warns when they do not.
- **Transport security**. Values:
  ```yaml
  tls:
    mode: mtls                 # mtls (default) | platform; per-hop overrides under tls.hops.*
    source: cert-manager       # cert-manager | secret
    certManager: {issuerRef: {kind: ClusterIssuer, name: <issuer>}}
    secrets: {api: <name>, bench: <name>, ingress: <name>, postgres: <name>, objectStore: <name>}  # when source: secret
    caSecretRef: null          # optional when the cert Secrets don't carry ca.crt
  ```
  - With `source: cert-manager`, the chart renders `Certificate` resources.
    With `source: secret`, it only mounts the named `kubernetes.io/tls`
    Secrets. Templates mount Secrets the same way in both cases.
  - In `mtls` mode, an init container (a shared template) concatenates the
    system CAs and `ca.crt` into an `emptyDir` and sets `SSL_CERT_FILE`.
  - In `mtls` mode, Postgres and the dev object store are configured to require
    client certificates. In `platform` mode they listen plain.
  - The chart **fails rendering** on contradictory values, for example
    `platform` mode with cert Secrets set.
- **Ingress** (optional, ingress-nginx): two Ingresses for one host. The UI goes to the frontend under
  `frontend.basePath`; the api is served under the same base path (`<basePath>/api`, prefix stripped), so the
  image's relative `config.json` works under any context path. In `mtls` the api Ingress uses backend mTLS with
  the chart's ingress certificate (`proxy-ssl-verify: "on"`); user annotations cannot override those.
- **Gateway API** (optional, Envoy Gateway): two
  HTTPRoutes with the same layout as the Ingresses, attached to a Gateway the cluster provides. In
  `mtls` an Envoy `Backend` carries the ingress client certificate and a `BackendTLSPolicy` checks the
  api's name. `ingress` and `gateway` are exclusive. kind runs Envoy Gateway beside ingress-nginx.
- **`kind` runs both kinds of environment:**
  - `just e2e-mtls` uses cert-manager with a self-signed ClusterIssuer.
  - `just e2e-platform` uses plain connections.

  CI runs both.
- `mocks/openai-mock` supports both modes: TLS requiring a client certificate,
  or plain HTTP.

## Constraints

- The chart must install cleanly on `kind` with the mock target. That is the
  end-to-end test environment.
- No cluster-specific values (hostnames, IPs, registry credentials) in
  committed files. Use placeholders.
