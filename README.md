# servtuner

servtuner benchmarks LLM serving on Kubernetes (vLLM, LiteLLM, Bifrost and other
OpenAI-compatible routers) and turns the results into evidence: the best
configuration for each application and the right resource sizing for it.

- Every benchmark is a reproducible run that keeps all of its raw data:
  per-request records from the client and Prometheus series from the server.
- Runs are checked before they count: a stage is trusted only if the client
  achieved the offered load and was not itself the bottleneck.
- Experiments compare configurations with confidence intervals, a tuning loop
  searches vLLM's knobs, and a capacity planner sizes replicas, HPA and cost.
- It installs with Helm and helmfile, including in air-gapped clusters
  (a Hauler archive).

**Documentation:** https://docs.servtuner.dev · **Install:** [`INSTALL.md`](INSTALL.md)

## Repository

| Folder | What it is |
|---|---|
| `bench/` | The `servtuner` CLI (Go): runs one benchmark end to end and analyses it |
| `api/` | The control plane (Python, FastAPI) |
| `frontend/` | The web UI (React, TypeScript) |
| `schema/` | The shared JSON Schema contract; Go, Python and TypeScript types are generated from it |
| `deploy/` | The Helm chart, helmfile, release tooling, kind setup and mock targets |
| `e2e/` | End-to-end tests |

## Develop

Toolchain pins are in `.tool-versions`.

```sh
just setup      # dependencies for every component
just gen        # regenerate generated code (just gen-check verifies it)
just lint
just test
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) before opening a pull request.

## License

Apache License 2.0. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
