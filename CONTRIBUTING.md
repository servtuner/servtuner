# Contributing to servtuner

Thanks for helping. Before a large change, open an issue to agree on the
approach; small fixes can go straight to a pull request.

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md). Report
violations privately to conduct@servtuner.dev.

## Set up

Toolchain versions are pinned in `.tool-versions`. With them installed:

```sh
just setup
```

Each component (`bench`, `api`, `frontend`, `schema`, `deploy`) has a
`CLAUDE.md` that describes its design and its own commands.

## Before you open a pull request

Run, from the repository root:

```sh
just gen-check   # generated code is current
just lint
just test
```

For changes to the UI or the api's run flow, also run `just e2e-smoke`. For
chart changes, `just e2e-platform` or `just e2e-mtls` on a kind cluster.

## Rules the review checks

- **Change the schema, not generated code.** Edit `schema/json/` (or the api's
  models for the OpenAPI document), then run `just gen` and commit the result.
- **Every transport mode is tested.** A connection is `mtls` (the default),
  `platform`, or, for benchmark hops only, `tls`. A new client or server is
  tested in every mode it supports, and there is never a skip-verify option.
- **No secrets in runs or values.** RunSpecs and chart values name Kubernetes
  Secrets; they never hold a credential. Captured env vars and headers are
  redacted.
- **No addresses or hostnames** in committed files; use placeholders and
  `*.localtest.me`.
- **Raw data first.** Analysis is derived from a run's raw records and series,
  never from engine summaries.
- **Pin versions exactly.** New dependencies need a reason in the pull request.
- **Literal constants carry a comment** saying what the number is.
- **Define things once.** The lint runs a duplication check (jscpd).

## Releases

Maintainers publish releases by pushing a tag; see [`RELEASING.md`](RELEASING.md).

## Licence

By contributing, you agree that your contributions are licensed under the
Apache License, Version 2.0.
