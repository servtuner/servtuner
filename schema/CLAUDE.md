# CLAUDE.md — schema (shared contract)

## Purpose

This folder is the single, **language-neutral** definition of every data shape
that crosses a component boundary:
- the **RunSpec**,
- the **run bundle files** (`manifest`, `phases`, `environment`, `summary`, and
  the `requests.parquet` record),
- and the **status events** bench sends to the API.

`bench` is Go, `api` is Python and `frontend` is TypeScript. **JSON Schema** is
the source of truth, and every language generates its types from it. Defining
the shapes once here is what keeps the three components from drifting apart.

## Design

```
schema/
  json/                    # source of truth, JSON Schema, written in draft-07 style (see Constraints)
    common.schema.json     # shared definitions: duration, transportMode
    runspec.schema.json
    bundle/{manifest,phases,environment,summary,request-record}.schema.json
    bundle/{prometheus-queries,prometheus-targets,target-snapshot,client-sample,analysis,…}.schema.json
    events.schema.json
  examples/<schema>/{valid,invalid}/*.json   # the one fixture set, shared by Go and Python
  presets/<name>.v<n>.yaml        # workload presets (workload-preset.schema.json), embedded in bench
  search-spaces/<name>.v<n>.yaml  # starting search spaces (search-space-template.schema.json), embedded in bench
  Justfile                 # lint only; generation lives with each consumer (below)
```

Each consumer owns its generator invocation, next to the version it pins:
`just --justfile bench/Justfile --working-directory bench gen` (Go) and the
same for `api` (Python). The root `just gen` runs them in order.
Schema names are the file name without `.schema.json`, and are also the
directory names in `examples/` (`runspec`, `manifest`, `phases`,
`environment`, `summary`, `request-record`, `events`, `analysis`, …).

Generated outputs are checked in, and CI fails if they're stale:

| Consumer | Generator | Output |
|---|---|---|
| bench (Go) | `github.com/atombender/go-jsonschema` v0.24.1 (pinned, via a `tool` directive in `bench/go.mod`) | `bench/internal/spec/zz_generated.go`, plus a copy of `json/` in `bench/internal/spec/schemas/` for `go:embed` (embed cannot reach outside the module) |
| api (Python) | `datamodel-code-generator` 0.87.0 (dev dependency) → pydantic v2 | `api/src/servtuner_api/models/generated/` (a package, see findings) |
| frontend (TS) | `openapi-typescript` from the API's OpenAPI document, which embeds these models | `frontend/src/api/schema.d.ts` |

## Constraints

- Edit the JSON Schema only. Never edit generated code.
- `apiVersion: servtuner.dev/v1` and `bundle_version` change only on a breaking
  change. Additive fields are optional and have defaults that are declared in
  the schema, so every language applies the same defaults.
- **Cross-language conformance test.** Every fixture in `examples/` is
  validated and round-tripped by Go and Python tests. A fixture that one
  language accepts and the other rejects fails CI.
- Old bundles must stay readable. `servtuner analyze` has to work on any bundle
  version still supported.
- **Draft-07 style only.** go-jsonschema (v0.24.1) has no documented 2020-12
  support, although datamodel-code-generator does. Use `definitions`, `enum`,
  `const`, `required` and `additionalProperties`, and avoid 2020-12-only
  keywords (`$dynamicRef`, `prefixItems`, `unevaluated*`).
- Keep schemas simple enough for every generator. Avoid `oneOf` cleverness and
  use `type` discriminators. The TLS rules (TLS on or off, no certificate settings
  without TLS) are expressed as a discriminated object, so every language enforces
  them identically.

## Conventions

- **Durations are Go-style strings** (`"500ms"`, `"120s"`, `"1m30s"`), one
  `duration` definition in `common.schema.json` with a `pattern`. Never a bare
  number. Generated as `string` in Go and a constrained string model in
  Python. Parsing to `time.Duration` is the consumer's job.
- **Field names** are stable once published (`apiVersion`, `secretRef`,
  `input_tokens`, `ttft_p95`, `optional_catalogs`). Units go in names that
  carry a number (`send_lag_seconds`, `size_bytes`).
- **TLS is a `oneOf` of two closed objects**, discriminated by `enabled`:
  `{enabled: false}` (nothing else allowed) and `{enabled: true, clientCert?:
  {secretRef}, caSecretRef?}`. `tls` is required wherever it appears (`target`,
  `upstreams[]`, `prometheus`): it is never inferred, so a missing `tls` is an
  error, not a default. Which URL scheme goes with TLS (`https://`) is a
  cross-field rule that `servtuner validate` enforces; the schema only checks
  the endpoint starts with `http://` or `https://`. servtuner 0.1's
  `servtuner.dev/v1` with `tls.mode` is refused on input and upgraded when read
  from stored data (bench `spec/legacy.go`, api `legacy.py`).
- **Optional means absent, never `null`.** Defaults are declared with
  `default` and applied by each generated type (Go on unmarshal, pydantic on
  validate). An optional object that is absent stays absent (no defaults are
  materialised inside it).
- `prometheus` is optional in a RunSpec (local runs may have none).
- **Events carry both `step` and `state`.** `step` is one of the seven
  pipeline steps (bench/CLAUDE.md); `state` is the part of the API run
  lifecycle that bench can report (`running`, `collecting`, `succeeded`,
  `failed`). `pending`, `launching` and `cancelled` belong to the API's
  runner. `phase` names the load phase while `state` is `running`.
- **Timestamps are RFC 3339 UTC strings**, one `utcTimestamp` definition in
  `common.schema.json`: a `Z` suffix, optional fractional seconds, enforced by
  a `pattern`. There is deliberately **no `format: date-time`**: go-jsonschema
  generates `type UtcTimestamp time.Time` for a `$ref`'d date-time definition,
  a defined type that has lost `time.Time`'s methods and cannot unmarshal a
  JSON string; and `format` checkers differ between languages (Python's
  `fromisoformat` accepts `20260101T000000Z`, offsets and week dates). The
  pattern behaves identically everywhere. It does not reject impossible dates
  such as 30 February; parse with `time.Parse` / `datetime.fromisoformat` when
  a value is used. `requests.parquet` stores real timestamps;
  `request-record` describes the logical row.
- **Always validate before you decode.** The generated types accept documents
  the schema forbids (Go: platform TLS with a `secretRef`, unknown fields,
  object knob values; pydantic: explicit `null`). Go callers use
  `spec.ParseRunSpec` / `spec.Parse[T]`, never a bare `json.Unmarshal`. Python
  callers use `servtuner_api.models.parse_run_spec` / `validate`, never a bare
  `RunSpec.model_validate`. Python finds the schemas in `$SERVTUNER_SCHEMA_DIR`,
  else `schema/json` of the checkout; the container image must set the
  variable to a copy.
- **Defaults have one shared expectation fixture,**
  `examples/runspec/defaults/{input,expected}.json`. Both languages apply the
  defaults to `input` and compare with `expected`. Go cannot show empty
  arrays/objects, `false` and `0` in marshalled JSON (`omitempty`), so its
  test ignores those on both sides and asserts the map/array defaults are
  non-nil separately.

## Bundle files

- **Bundle files use numbers with a unit suffix** (`scrape_interval_seconds`,
  `gpu_memory_bytes`), not duration strings; the Go-style `duration` is for
  the RunSpec only.
- **Shared definitions in common:** `bootstrapInterval` (`{value, ci_low,
  ci_high, samples}`, or `insufficient_samples_reason` instead of the three
  numbers, never both), `scrapeIntervalSource` (`discovered` | `assumed`),
  `metricName`, `catalogName`, `sloName`.
- **`load.pacing`** (`constant` | `poisson` | `linear` | `sine`, default
  `constant`) is separate from `load.profile`, which stays the ladder kind.
  `linear` ramps from the previous stage's rate to this stage's rate; stage 0
  holds its rate. `load.sine` is required when pacing is `sine`.
- **Snapshots are two files**, `target/snapshot-pre.json` and
  `target/snapshot-post.json`, one schema (`target-snapshot`) with `phase`
  `pre` | `post`. Each holds `workloads[]`: the target and each upstream
  (`role`, `upstream_index`). A redacted env var carries no value, by a
  `oneOf` of two closed objects.
- **`analysis.json` is authoritative over `summary.json`** for the stage
  statistics and capacity it covers; `summary.json` is the quick
  summary bench writes before analysis.
- **Writers emit integers for integer fields.** `5.0` is schema-valid (JSON
  Schema treats it as an integer) but Go's typed decode rejects it.
- **Don't name a property `class`** (a Python keyword makes the generated
  field `class_`, which fails round trip); use `error_class`.
- **Root schema titles must not collide with a definition title** in another
  schema: the analysis document is `AnalysisResult` because the RunSpec
  already defines `Analysis`.
- **Rules by `if`/`then`, `allOf` and `oneOf`.** Both generators ignore `if`/`then`
  (types do not change); only validators enforce them, so Go must use
  `Parse` and Python `validate`. go-jsonschema fails on `dependencies`, so use
  `if`/`then` instead. The generated Python model alone does not reject such
  documents (see `VALIDATOR_ONLY_INVALID_RUNSPECS` in the Python test).
- **Self-checks the schema cannot express**, left to `servtuner analyze` and
  `servtuner validate`: `ci_low <= value <= ci_high`; `failed_stage_index >
  valid_stage_index`; HPA `min_replicas <= max_replicas`.

## Upstreams, request records and knobs

- **`upstream.tls` is required** (same `tls` definition as the target; never
  inferred, never copied from the target) and `upstream.type` is an optional
  `targetType` (absent means openai-compatible). `target.admin` is
  `{secretRef: adminSecretRef}`, modelled like `target.auth`: names of Secret
  keys only; exactly one of `tokenKey` or `usernameKey` + `passwordKey`,
  enforced by `if`/`then` (validator-only, see `VALIDATOR_ONLY_INVALID_RUNSPECS`).
- **Request record:** `phase` (`warmup` | `stage`) is required; `stage_index`
  is required only when phase is `stage`. `scheduled_at` and `send_lag_seconds`
  are optional (null in Parquet, absent in the JSON row). `first_content_seconds`
  is the first visible content token; `ttft_seconds` is the first token of any
  kind. Warmup rows are kept in the raw data and excluded from stage statistics.
- **`phases.json` `scrape_warnings[]`** are structured `missing_scrape_role`
  records (`rule`, `role`, `message`), separate from the stage-length `warnings`.
- **`environment.json` carries `knobs`** (name to value, as captured by the
  target adapter) plus `pod` and `namespace`. Knobs live in environment.json
  rather than a new `target/knobs.json` because they are small, are written once
  after the pre snapshot, and analysis already reads environment.json; a new
  file would add a schema, a bundle path and a missing-file case for no gain.

## Generator findings

Both generators handle the RunSpec. These are the limitations found, and the
rule that follows from each. Conformance tests: `bench/internal/spec/conformance_test.go`
and `api/tests/test_schema_conformance.py`.

go-jsonschema v0.24.1:
- **`oneOf` becomes `interface{}`** (the `Tls` type). The variants are still
  generated (`TlsDisabled`, `TlsEnabled`), and `spec.DecodeTLS` picks one by
  `enabled`, and refuses a lossy decode (extra keys such as `skipVerify`, or a
  `clientCert` with `enabled: false`). Unmarshalling the document does not enforce the
  TLS rule; schema validation does, so use `spec.ParseRunSpec`. Do not replace
  this with a hybrid "object with both properties plus oneOf": Go gets a
  struct, but datamodel-code-generator then merges the properties into each
  variant and lets `enabled: false` carry a `clientCert`.
- **Multi-type properties** (`knobValue`: string, number or boolean) become
  `interface{}` with no validation. The knob value type is checked by the
  schema validator only.
- **Non-zero scalar defaults cannot round-trip an explicit zero value.**
  Optional scalars get `omitempty`, so an explicit `false` (or `0`) is dropped
  on marshal and the default comes back on the next unmarshal.
  `--disable-omitempty` is not a fix: nil pointers then marshal as `null`,
  which the schemas reject. Rule: **never give a boolean a default of `true`,
  and never give a number a non-zero default where zero is meaningful.**
  Make the field required instead (this is why `workload.streaming` is
  required). The conformance test does a second round trip to catch this.
- **Empty arrays and objects, `false` and `0` are omitted on marshal.** That
  is equivalent to the declared default; the round-trip test accepts it.
- **Definition names must not collide with a generated path name.** A
  definition `manifestStatus` used by `Manifest.status` produced duplicate
  types (`ManifestStatus_1`). Name definitions by meaning (`runStatus`,
  `failure`), not `<parent><Field>`.
- It accepts cross-file `$ref`s (`common.schema.json#/definitions/duration`)
  and writes one file from several inputs, with a fixed input order for
  deterministic output. `$ref` with a sibling `default` works. A shared-
  definitions file must not declare `"type": "object"`, or it generates an
  empty root type (`Common`).

datamodel-code-generator 0.87.0:
- **One output file works only for a self-contained schema.** Schemas that
  reference each other (here, `common.schema.json`) require a directory
  output, so the models are a package, `servtuner_api.models.generated`, with a
  module per schema (`runspec_schema`, `bundle/manifest_schema`, ...).
  `servtuner_api.models` re-exports the document roots.
- Output is deterministic only with `--disable-timestamp`. The Justfile pins
  the formatter (`ruff-format`, version from `uv.lock`).
- **Enum defaults need `--set-default-enum-member`;** otherwise the default
  is a plain string on an enum-typed field, which breaks equality and
  triggers serializer warnings.
- **Defaulted fields are non-nullable only with `--strict-nullable`.**
  Optional fields without a default are still `X | None = None`, so the
  models accept an explicit `null` that the schema rejects. Serialise with
  `model_dump(mode="json", exclude_none=True)`.
- `oneOf` of closed objects is handled exactly (`Tls = TlsMtls | TlsPlatform`,
  `extra="forbid"`), and the generated RunSpec rejects every invalid example.
- Integer knob values come back as floats (`256` becomes `256.0`), because
  the union is `str | float | bool`. Numerically equal, schema-valid.
- `common.schema.json` generates an empty, unused `Common` model, because a
  schema file with a title is a root. Go does not emit one.

Validators (santhosh-tekuri/jsonschema v6, Python `jsonschema` 4.26):
- Both agree on every example. No `format` keyword is used (see Timestamps);
  do not add one without making both sides check it identically.
- Cross-file `$ref`s resolve through each schema's `$id`
  (`https://servtuner.dev/schema/...`, never fetched); both validators load
  all schema files into a local registry up front.

## Off-cluster targets, comparisons and charts

- **`target.metrics`** (`job`, default `servtuner-external`; `namespace`; `pod`)
  names where an off-cluster target's series live: the scrape job attaches
  those labels, so bench selects them like a pod. `servtuner validate` rejects it
  together with `target.workload` (a cross-field rule, so validator-only).
- **`comparison.schema.json`** (servtuner compare) and the chart files
  `bundle/chart-client-timeseries` and `bundle/chart-histograms`. Their
  definitions are named so no existing generated type is renamed: check
  `git diff` of `zz_generated.go` shows additions only after `just gen`.
- **Write schema JSON with `ensure_ascii=False`** when generating it from a
  script, or non-ASCII characters become `\uXXXX` escapes throughout the file.

## Workload presets

- **`workload.preset`** (`name@version`) replaces `dataset`, `conversation` and the api: `workload` has `if`/`then`/`else` directly on the definition (an `allOf` there made go-jsonschema merge the RunSpec `Workload` with the snapshot `Workload` and rename the latter). `dataset` is therefore optional in the schema and a pointer in Go; code after `spec.LoadRunSpec` can rely on it being set, because the preset is expanded there.
- **`api` cannot be forbidden beside a preset**: the generated types write its `chat` default back on marshal. `spec.ExpandPreset` rejects one that differs from the preset instead.
- **`workload_preset`** is a top-level RunSpec property that only bench writes (into `spec.resolved.json`).
- **`workload-preset.schema.json`** validates `schema/presets/<name>.v<N>.yaml` (YAML, so the files are checked by the Go and Python conformance tests directly, not through `examples/`). Its examples are JSON in `examples/workload-preset/`. `just gen` copies the presets next to the embedded schemas for `go:embed`.

## Tuning loop

- **`search-space`, `boot-results` and `proposal`** are the inputs and output of `servtuner propose`. Cross-field rules (values or bounds, not both; which knobs take bounds; vLLM knob names; a non-empty boot knob set) are checked by `servtuner propose`, not the schema: a `$ref` with siblings ignores the siblings in draft-07, so `minProperties` beside a `$ref` cannot work.
- **Definitions are prefixed** (`tuning*`, `proposal*`) so no existing generated type is renamed; the generators renumber unrelated enum names (`Source5` to `Source6` in the Python runspec model) when a new `source` enum appears.
- **`config_id`** is `c-` plus the first 8 hex digits of the sha256 of the knobs as JSON with sorted keys and no spaces. The tuning session stores it.
- **RunSpec labels** `servtuner.dev/tuning-ladder` (`probe`, `screen`, `full`) and `servtuner.dev/tuning-config` are how `servtuner propose` tells the session's runs apart; the proposal lists them.
