# Experiments on kind

`bifrost-to-mock-saturation.json` is the router saturation experiment: Bifrost in front of the mock,
Poisson arrivals at 50, 150, 300 and 450 RPS for 3 minutes each, run as a Job:

```
PYTHONPATH=api uv run --project api python e2e/cluster.py platform --load --runner kubernetes \
  --runspec deploy/kind/experiments/bifrost-to-mock-saturation.json --image-tag e2e --keep
```

**What it found on kind (2026-10-30):** the limit is Bifrost itself. Its 1-core CPU limit is at 87% at
300 RPS, and TTFT p95 doubles to 0.86s at 450 RPS. The analysis puts the knee at 300 RPS with CPU as the
bottleneck, interpolates an SLO-max of 334.5 RPS per replica (TTFT p95 limiting), sizes both containers,
recommends `bifrost.concurrency` 395 and `buffer_size` 0, and raises B6 (CPU-bound) and B8 (logging on).

`mock-latency-per-in-flight.yaml` makes the mock itself slow down with load, for an upstream-bound run
(rule B3). It needs a mock image with the fault knobs, so pass its tag explicitly:

```
helm upgrade openai-mock deploy/mocks/chart -n servtuner-mock --reuse-values \
  --set image.tag=<tag with fault knobs> -f deploy/kind/experiments/mock-latency-per-in-flight.yaml
```

Pitfall: when an upgrade fails, `--reuse-values` on the next one starts from the last *deployed* revision,
not the failed one, so settings from the failed upgrade (such as the image tag) are silently lost. Always
pass them again, and check the running image afterwards. To restore the mock, write its values without
`mock.faults` and `resources` and upgrade with `-f` (not `--reuse-values`, which keeps old keys).

## Payload size: memory per in-flight request, m(s)

`payload-size-{0,64k,512k,2m}.json` are the payload-size template (memory per in-flight request): the same
Bifrost → mock ladder (10, 20 and 40 RPS, Poisson, 3 minutes each: a stage needs 10 scrapes of the 15s cAdvisor job) with `workload.dataset.padding_bytes`
at 0, 64 KiB, 512 KiB and 2 MiB. Each run fits its own memory line (working set against requests in
flight); together they fit how the bytes per in-flight request grow with request size. The mock accepts
bodies up to 8 MiB.

```
PYTHONPATH=api uv run --project api python e2e/cluster.py platform --load --runner kubernetes --image-tag e2e --keep \
  --runspec deploy/kind/experiments/payload-size-0.json --runspec deploy/kind/experiments/payload-size-64k.json \
  --runspec deploy/kind/experiments/payload-size-512k.json --runspec deploy/kind/experiments/payload-size-2m.json
```

Then plan with one run as the tier and the others as the series, here asking about 4 MiB messages:

```
servtuner plan --input plan-request.json --models 0/capacity_model.json \
  --payload-models 64k/capacity_model.json,512k/capacity_model.json,2m/capacity_model.json
# plan-request.json overrides {"node": "bifrost.payload_bytes", "kind": "hypothetical", "value": 4194304}
```

The runs differ in workload hash by design; the payload series is matched to the tier by target type,
knobs and hardware profile instead.

**What it found on kind (2026-10-30, with 2-minute stages, so each run is graded degraded for low
resolution; the template now uses 3 minutes):** Bifrost's bytes per in-flight request grew about 50×
faster than the request size (0.39 MB at 0.8 KB, 3.3 MB at 64 KB, 29.9 MB at 525 KB). Bifrost was
OOM-killed at its 1 GiB limit at 512 KiB × 40 RPS and throughout the 2 MiB run, which is graded
unusable (so `e2e/cluster.py` exits non-zero on it). Planned from the other three runs, 2 MiB requests
need 2.9 GiB at C + B: the planner flags the limit the real run hit.

## Bifrost concurrency: does the knob bind?

`bifrost-concurrency-{32,128}.json` with `bifrost-concurrency-{32,128}.values.yaml` (applied on top of
`deploy/kind/servtuner-bifrost.values.yaml`, never with `--reuse-values`). Three repeats per value as Jobs:

```
helm upgrade servtuner-bifrost <bifrost chart> -n servtuner-bifrost -f deploy/kind/servtuner-bifrost.values.yaml \
  -f deploy/kind/experiments/bifrost-concurrency-128.values.yaml
R=deploy/kind/experiments/bifrost-concurrency-128.json
PYTHONPATH=api uv run --project api python e2e/cluster.py platform --load --runner kubernetes --image-tag e2e --keep \
  --runspec $R --runspec $R --runspec $R
```

Copy the bundles out of the api pod (`kubectl exec deploy/<release>-api -- tar -C /var/lib/servtuner/runs -cf - <id>`)
before removing the namespace, then `servtuner compare --bundles ...`.

**What it found (2026-11-22):** 128 reaches Bifrost's CPU-bound ~330 RPS, 32 stops at ~100 RPS; 128 wins
with high confidence. For streaming, a concurrency slot is held about as long as the upstream TTFT, not
the whole response. `buffer_size` 5000 behind a binding concurrency OOM-killed Bifrost at 1 GiB.

## LiteLLM workers: verifying the LiteLLM advisor

`litellm-workers-{1,2}.json` run the same Poisson ladder (8, 16, 24 and 32 RPS, 150s each, streaming)
against the LiteLLM proxy of `deploy/kind/servtuner-litellm.yaml` with one worker, then with two workers and
two cores (`litellm-workers-2.patch.yaml`). bench runs as a **local process** here, because the proxy's
master key is a Secret in `servtuner-litellm` that a Job in another namespace cannot mount:

```
kubectl -n monitoring port-forward svc/kps-prometheus 19090:9090 &
# replace kind-node.invalid with the kind node's InternalIP (kubectl get nodes -o wide)
servtuner run --spec litellm-workers-1.json --out <dir> --kubeconfig ~/.kube/config \
  --secrets-dir <dir holding servtuner-litellm-master-key/token, mode 600>
```

Do not drive load through `kubectl port-forward`: it dropped connections at 32 RPS and then died, which
showed up as thousands of `connection_refused` errors. The NodePort (30400) has no proxy in between.
`--kubeconfig` is required for the snapshot; without it bench knows no target pods, so it skips the
Prometheus collection and the advisor has nothing to read.

## Scale-out linearity (template)

`litellm-scale-out-2.json` is `litellm-workers-1.json` at two replicas, with each stage's rate doubled so each
replica sees the same load (16, 32, 48, 64 RPS). Scale the proxy first
(`kubectl -n servtuner-litellm scale deploy/servtuner-litellm --replicas=2`), run both, and plan with both
capacity models: `servtuner plan` folds them into one tier and checks that per-replica SLO-max agrees within 10%.
On kind (2026-11-22): 23.85 RPS per replica at 1 replica and 24.08 at 2, so linear scale-out is verified;
the NodePort spread the load evenly over the two pods.

## Burst probe (template)

`bifrost-burst.json`: Bifrost over the mock at 50 and 100 RPS, then a 20-second step to 300 RPS marked
`is_burst`. The burst stage is kept out of the capacity ladder; its peak in flight per replica is checked
against the recommended C + B. Bifrost needs `servtuner-bifrost-nodeport.yaml` (port 30808) to be driven by bench
as a local process; replace `kind-node.invalid` with the node's IP and point `prometheus.url` at a
port-forward, as for LiteLLM.

On kind (2026-11-22) the queue model sized C = 31 and B = 0 from the 100 RPS stage, and the 300 RPS burst
peaked at 126 in flight per replica: the queue recommendation turned urgent, since those settings would drop
requests in a 3× burst. Poisson sizing alone would not have shown it.
