# Prometheus (community chart)

For clusters that run the community `prometheus` chart. The kind dev cluster runs kube-prometheus-stack
instead (deploy/kind/README.md, "Prometheus"); there the same job definitions go into the
`servtuner-additional-scrape-configs` Secret, and managed targets get PodMonitors (`tuning.monitor: podmonitor`).

`servtuner-targets.values.yaml` adds the `servtuner-targets` job to the community
`prometheus` chart through `extraScrapeConfigs`:

- 5s interval, 4s timeout, for targets under test only. The global interval is untouched.
- A pod opts in with the label `servtuner.dev/scrape=true`; remove the label to drop it back to the normal interval.
- Optional annotations `servtuner.dev/metrics-port`, `servtuner.dev/metrics-path`, `servtuner.dev/metrics-scheme`.
- `metric_relabel_configs` keep only `vllm:*`, `bifrost_*`, `http_request*`, `http_response*`, `litellm_*` and the mock's `openai_mock_*`.
- servtuner's catalogs select `job="servtuner-targets"`. A pod that is also annotated `prometheus.io/scrape` is scraped twice by distinct jobs, which is harmless.

Authenticated targets (LiteLLM key, Bifrost basic auth) need one job per target type; see the comments in the file.

`servtuner-cadvisor.values.yaml` adds a 15s cAdvisor job for the `servtuner*` namespaces only,
with the kubelet's TLS verified (no `insecure_skip_verify`). It mounts the kubelet's
serving CA from the ConfigMap `servtuner-kubelet-ca`; on `kind`, create it with
`deploy/kind/kubelet-ca.sh` before upgrading.

`extraScrapeConfigs` is a single string. Applying either file alone replaces the
jobs already in it, so merge them with your existing jobs.

This changes Prometheus configuration. Review it and apply it yourself; nothing here is applied automatically.
With the Prometheus Operator, use ServiceMonitors with `interval: 5s` instead.
