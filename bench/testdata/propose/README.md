# Fixtures of `servtuner propose`

Where the numbers come from: recorded RTX 3070 vLLM runs (an original and a
fine max_num_seqs ladder). The raw bundles are not in the repository, so
each fixture bundle holds only the files `propose` reads:
`manifest.json`, `spec.resolved.json` (with the SLOs of the recorded experiment:
TTFT p95 500 ms, ITL p95 50 ms, errors 1%), `analysis.json` and, when an advisor
fired, `recommendations.json`. There are no request records, no capacity
model and no Prometheus series.

The bundles live in `runs/` (the repository's .gitignore ignores a directory named `bundles`), one line of JSON per file (the repo-wide duplication check counts lines), and are written by `TestWriteFixtureBundles` in
`bench/internal/propose/fixtures_test.go`
(`go test ./internal/propose -run TestWriteFixtureBundles -update`); edit the
table there, not the files. The manifest, RunSpec and analysis are validated
against their schemas when written. Every RunSpec carries the labels a proposal asks for (`servtuner.dev/tuning-ladder`, `servtuner.dev/tuning-config`); `confirm-seqs128-aborted` is a confirmation run whose ladder stopped one stage early (not recorded).

| Bundle | Run id | Declared `max_num_seqs` | SLO-max (req/s) | Source |
|---|---|---|---|---|
| `seqs64-probe` | `01M807ST...` | 64 | 20.997 | first run of the 64 configuration in the original-ladder comparison |
| `seqs128` | `01M808ZJ...` | 128 | 37.922 | first run of the 128 configuration (the comparison's mean over 6 runs) |
| `seqs256` | `01M80A7F...` | 256 | 39.397 | first run of the 256 configuration (mean of 3) |
| `confirm-seqs256-r1..3` | `01M80BG5...` and two more | 256 | 44.01, 44.02, 44.03 | the fine-ladder comparison: mean 44.02, 95% interval 43.99 to 44.05 |
| `confirm-seqs128-r1..3` | `01M80CJ5...` and two more | 128 | 41.61, 41.71, 41.81 | the fine-ladder comparison: mean 41.71, interval 41.47 to 41.96 |
| `seqs128-frontend` | `01M80F00...` | 128 | 37.922 | **synthetic**: `seqs128` with V7 firing, to test the api_server_count gate |

What is not recorded, and is made up here:
- The comparison files keep only the mean and the interval per configuration,
  so the three confirmation values of a configuration are chosen to have that
  mean and standard deviation.
- The ladders (probe 8 rungs, screening 5, full 9) are plausible, not the
  recorded ones; the ladder kind comes from the label, not the length. The
  recorded SLO-max lies between two rungs in each.
- Every run declares `vllm.max_num_batched_tokens` 8192, `async_scheduling`
  false and `api_server_count` 1, the baseline of `space.yaml`, and one GPU
  (the recorded runs had no GPU count, so `slo_rps_per_gpu` was unavailable).
- The advisor files are whole, schema-valid `recommendations.json` files (empty
  derivation, one knob recommendation). V1 on `seqs64-probe` recommends 128;
  on `seqs128` it recommends 256 with a measured C_kv of 310. These stand for
  what the advisors say on such runs; they are not recorded.
- `boots.yaml` (boot-probe results) is illustrative, not measured by the boot
  probe.

Spaces: `space.yaml` (scheduler and fine-tuning knobs), `space-seqs.yaml`
(`max_num_seqs` only, so the search converges and confirms),
`space-fine.yaml` (fine-tuning only), `space-feasibility.yaml` (feasibility
knobs and a concurrency the KV cache must hold).
