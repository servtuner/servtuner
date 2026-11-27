# CLAUDE.md — frontend (SPA)

## Purpose

The frontend is a single-page app for people who tune and size LLM serving
stacks. It lets them:
- create runs from a RunSpec form or YAML,
- watch runs progress,
- inspect one run in depth,
- **compare runs across knobs**, to answer "which configuration is best?",
- tune managed vLLM targets and plan capacity,
- and read sizing and cost recommendations.

It is a pure client of `api`. It holds no business logic that the API doesn't
also expose. In particular, it never computes summaries from raw data. Charts
read the chart-ready `charts/*` files that `servtuner analyze` produces, and
every planner number comes from `POST /scenarios/evaluate`.

## Design

- **React + TypeScript 5.x + Vite**, with **Mantine** components and TanStack
  Table for large tables. Stay on TypeScript 5 until openapi-typescript
  supports 7. The API types are **generated from the API's OpenAPI document**
  (`openapi-typescript`), so the RunSpec, run and summary types are never
  written by hand.
- **API client:** `src/api/client.ts` is a small typed `fetch` wrapper whose
  types come from `schema.d.ts` through `src/api/types.ts`. `types.ts` also has
  request-side views (`PlanRequest`, `PlanCreate`), because the generated models
  mark fields with defaults as required.
- Data fetching uses TanStack Query (polling while a run is active).
- RunSpec validation is the API's `/specs/validate` (a dry run of `servtuner
  validate`); the browser only checks that the YAML parses. No ajv, so the
  rules live in one place. YAML is edited with the `yaml` package, which keeps
  comments.
- Routing uses React Router; a run's tab is in the URL (`?tab=`). The build
  output is static files served by nginx. Runtime config such as the API base
  URL comes from a `config.json` fetched at startup, so one image works in any
  environment.

## Layout

```
src/
  api/           # client.ts, types.ts, schema.d.ts (generated), runStatus.ts (Record<Union, …> maps)
  app/           # shell, routes, theme, config loader, basePath.ts
  charts/        # useChart (ECharts core, only the parts used), palette.ts, stageBands.ts
  components/    # shared components; format.ts, IntervalValue
  features/      # one folder per area:
    auth users            # sign-in, setup, forced password change; the Users page
    runs new-run run-detail overview followup schedules
    compare experiments   # comparison and experiment views
    targets managed-targets hosts knobs tuning planner
  test/          # MSW handlers and fixtures, typed against the generated schema
```

- **Run detail tabs:** Overview, Latency, Throughput, Queueing, Server,
  Resources, Config, Recommendations, Capacity, Files. Chart tabs are
  lazy-loaded, so ECharts stays out of the first page load.
- **Knobs:** `features/knobs/` is the one knob editor (chips, an inline editor
  of the knob's kind inside vLLM's limits, a picker over `GET /knobs`), used by
  Deploy and New session. `features/tuning/SpaceKnobEditor` reuses it for a
  search space's tuned knobs and writes them back into the space's YAML.
- **Prices:** `PriceBookSection` edits the install's price book (hosts, node
  types, the token-price datasheet); New run fills it into a new spec as
  `analysis.infra_pricing`.

## Charts and formatting

- **ECharts 6** through `useChart`, with one shared theme (light and dark).
  **The SVG renderer is used, not canvas:** every series is capped at about
  2,000 points, and SVG renders in jsdom, so charts are tested.
- Every time chart has stage bands. Bands alternate in depth, carry their
  labels above the plot, and hatch excluded stages red.
- Percentiles show CIs; requests and limits are reference lines on resource
  charts.
- `format.ts`: seconds as ms below 1 s, four significant figures for plain
  numbers, binary units for bytes. `IntervalValue` renders "n/a (n=…)" for too
  few samples, never a number.

## Context path

The one image is hosted under any context path with no rebuild. The rule:
**one `<base href>`, and everything derives from `document.baseURI`.**

- nginx writes `<base href>` into `index.html` per request: from the
  `X-Forwarded-Prefix` header when it is valid (the rule is in
  `nginx.conf.template`), otherwise from `SERVTUNER_BASE_PATH` (default `/`,
  validated at container start by `base-path.envsh`). An invalid or duplicated
  header falls back to the env path. It serves the app at `/` and under
  `SERVTUNER_BASE_PATH`.
- Vite uses `base: "./"`. `src/app/basePath.ts` is the only place that derives
  the router basename and resolves URLs. `config.json` and a relative
  `apiBaseUrl` (default `"api"`) resolve against the base URI.
- `apiBaseUrl: "api"` follows the prefix. `"/api"` is origin-absolute and
  ignores it, which is right only when the API really is at the origin root.
- Never write an absolute app path (`/assets/...`, `fetch("/config.json")`)
  or build a URL by hand. Use router links, or `resolveAgainstBase`.

## Commands

- `just gen`: writes `src/api/schema.d.ts` from `../api/openapi.json` (never
  hand-edited).
- `just lint`: ESLint, `tsc --noEmit` and `prettier --check` (width 120;
  Markdown and `schema.d.ts` are ignored). `just format` rewrites in that style.
- `just test`: Vitest and Testing Library. `just build`: `dist/`.
- `just test-container` (needs docker): checks the context-path rules against a
  real container.
- `just e2e-smoke` (root; needs docker, go, node and Google Chrome): Postgres,
  the real api with bench's null engine, and the dev server with
  `SERVTUNER_DEV_API` proxying `/api`. It creates and watches a run, opens its
  tabs, the runs list and the planner, deletes the run, and exercises a managed
  vLLM's pages. Playwright uses the installed Chrome (`channel: "chrome"`);
  `PLAYWRIGHT_CHANNEL=""` uses its own Chromium.
- `just e2e-managed-ui` (root; kind, about 30 minutes): add, deploy, log,
  redeploy, benchmark, free, tune and delete a managed vLLM in the browser.

## Tests

- MSW handlers and fixtures in `src/test/` are typed against the generated
  schema, so a contract change breaks the build.
- `src/test/bundles.ts` reads the schema examples and a golden `analysis.json`
  from `bench/testdata/analysis/` (`?raw` imports; Vite's `server.fs.allow`
  admits those two folders only).

## Image

`Dockerfile` builds with Node 24 and serves with `nginxinc/nginx-unprivileged`
on 8080, from `nginx.conf.template` (rendered by envsubst) and
`base-path.envsh`. `config.json` is sent `no-store`.

## Constraints

- It is served over TLS from the ingress or gateway, which talks to the API
  with mTLS. The SPA never holds a client certificate itself.
- Run detail shows the stage validity of each stage (achieved vs. offered RPS,
  send lag, `client_bound`) next to its numbers, so untrustworthy RPS is never
  shown unmarked.
- No secrets in the UI. Spec fields that reference Secrets only show the
  reference.
- Always label units and percentiles. Show times in the viewer's local time
  with the UTC value on hover.
