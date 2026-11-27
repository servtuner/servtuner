# kind / dev cluster manifests

`cluster-issuer.yaml` creates a self-signed CA chain (`servtuner-selfsigned` -> CA certificate -> `servtuner-ca` ClusterIssuer)
for `tls.enabled: true` with `tls.source: cert-manager`. Use it with `--set tls.certManager.issuerRef.name=servtuner-ca`.

It is **NOT applied automatically** by anything in this repository. On a shared cluster, review it and
apply it yourself (`kubectl apply -f deploy/kind/cluster-issuer.yaml`). cert-manager must already be installed.

## End to end

- `e2e-values.yaml`: the chart values shared by `just e2e-mutual-tls` and `just e2e-tls-off` (`e2e/cluster.py`).
  The script adds the mode, the issuer, the ingress host (`SERVTUNER_E2E_HOST`, default
  `servtuner-e2e-<mode>.localtest.me`), the base path and the image tags.
- `e2e/cluster.py` runs only on the kube context `SERVTUNER_KUBE_CONTEXT` (default `kind-kind`), installs into
  its own namespace `servtuner-e2e-<mode>`, and removes that namespace on exit (`--keep` skips this). With
  `mutual-tls` the `servtuner-ca` ClusterIssuer above must already be Ready; the script checks and never applies it.
- `kind-up.sh` (`just kind-up`, CI only) creates a fresh cluster from `kind-config.yaml` with cert-manager,
  CloudNativePG, ingress-nginx and the ClusterIssuer. It refuses to run if a cluster of that name exists.

## Prometheus

The kind cluster runs **kube-prometheus-stack** (the Prometheus Operator, chart 92.14.1) in `monitoring`,
in place of the community `prometheus` chart: servtuner's
monitors and the managed-target deployer's PodMonitors need the Operator. servtuner's
`prometheus.url` there is `http://kps-prometheus.monitoring.svc:9090`.

- `kube-prometheus-stack.values.yaml`: every PodMonitor and ServiceMonitor in any namespace is selected; no
  Grafana; kind's 127.0.0.1-bound control-plane monitors are off.
- Jobs that are not monitors come from the Secret `servtuner-additional-scrape-configs` (key
  `scrape-configs.yaml`) in `monitoring`, which the cluster's operator creates and which is never committed,
  because off-cluster targets carry real addresses. It holds `servtuner-targets` and `servtuner-cadvisor` (the
  snippets in `deploy/prometheus/`, with `ca_file: /etc/prometheus/configmaps/servtuner-kubelet-ca/ca.crt`), the
  off-cluster `servtuner-external` jobs, and `opencost`.
- Install, after `kubelet-ca.sh` and the Secret:
  `helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack --version 92.14.1 -n monitoring -f deploy/kind/kube-prometheus-stack.values.yaml`.
  The stack's node-exporter takes host port 9100, so a community chart on the same cluster goes first.
- opencost points at `kps-prometheus` on port 9090 (`opencost.prometheus.internal.serviceName`, `.port`).

## Prometheus resolution

- `kubelet-ca.sh` copies the kind kubelet's self-signed serving CA into the ConfigMap `servtuner-kubelet-ca` in
  `monitoring`. The `servtuner-cadvisor` job (`deploy/prometheus/servtuner-cadvisor.values.yaml`) verifies the kubelet
  against it; the Operator mounts it through `prometheusSpec.configMaps`. Rerun it if the node is recreated; the current CA expires a year after the node was created.

## A CPU vLLM on kind (no GPU)

kind has no GPU, but a managed vLLM can still be deployed, benchmarked and
tuned there with vLLM's CPU build. What worked on the development cluster
(16 cores, 60 GiB, shared with other workloads), 2026-11-25:

- **Install:** the release needs the Kubernetes runner and a tuning
  namespace, layered on its values: `kubectl create namespace servtuner-vllm`,
  then `helm upgrade ... --reuse-values -f deploy/kind/e2e-jobs-values.yaml
  -f deploy/kind/e2e-tuning-values.yaml --set-string
  "rbac.tuning.namespaces[0]=servtuner-vllm" --set-string
  bench.image.repository=servtuner-bench --set-string bench.image.tag=<tag>` (with `--set-json 'subprocess.secrets=[]'` when the
  subprocess runner had Secrets; the Kubernetes runner mounts a run's Secrets
  itself). Load the bench image into kind first. An install whose api
  Deployment was once changed with `kubectl patch` needs this upgrade with
  `--server-side=false`: server-side apply cannot drop fields another
  manager owns.
- **Target:** image `vllm/vllm-openai-cpu:v0.34.0-x86_64` (the CPU build of
  the pinned vLLM; `kind load docker-image` it, 5.6 GB), model
  `Qwen/Qwen2.5-0.5B-Instruct`, GPUs 0, CPU 4, memory 24Gi, base flag
  `--max-model-len=2048`, env `VLLM_CPU_KVCACHE_SPACE=4` (GiB of KV cache).
- **Knobs:** `vllm.enforce_eager: true`. Without it the worker compiled its
  graphs on one thread for over 15 minutes and never served; with it the
  target was Ready in about a minute. The `no-gpu` starting search space sets
  it in its base.
- **Requests must fit the node:** a bench Job asks for 2 CPUs, Guaranteed.
  With 8 CPUs for vLLM the node (90% requested) could not schedule it, and
  the run failed at once with the scheduler's reason; 4 CPUs left room.
- **Expect well under 1 RPS:** on 4 CPUs the 0.5B model completed about
  0.6 to 0.85 chat requests a second (256 tokens in, 128 out), behind even
  a 1 RPS stage, so a 1/2/4 RPS ladder had no valid stage and no SLO-max.
  Start a CPU ladder near 0.25 RPS with stages long enough for 200 requests
  (servtuner validate says how long), or shorter prompts and outputs.
