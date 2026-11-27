import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { searchSpaces } from "./bundles";
import type { AuthStatus, ListRunEventsResponse, ListRunsResponse, ManagedTarget, RunDetail } from "../api/types";
import {
  failedRun,
  knobCatalog,
  managedTarget,
  runningRun,
  runningRunDetail,
  succeededRun,
  succeededRunDetail,
  succeededRunEvents,
  workloadPresets,
} from "./fixtures";

export const TEST_API_BASE_URL = "http://api.test";

export const allRuns: ListRunsResponse = [failedRun, runningRun, succeededRun];

// Default handlers filter by the same query parameters the API supports.
// auth.mode none unless a test says so: pages render as before user auth.
export const authOff: AuthStatus = { mode: "none", methods: [], setup_required: false, user: null };

export const defaultHandlers = [
  http.get(`${TEST_API_BASE_URL}/auth/status`, () => HttpResponse.json<AuthStatus>(authOff)),
  // An unstamped api, as in development.
  http.get(`${TEST_API_BASE_URL}/version`, () => HttpResponse.json({ version: "0.0.0-dev", commit: "unknown" })),
  // New run lists saved targets for its picker; none unless a test says so.
  http.get(`${TEST_API_BASE_URL}/targets`, () => HttpResponse.json([])),
  // Experiment inputs suggest existing experiments; none unless a test says so.
  http.get(`${TEST_API_BASE_URL}/experiments`, () => HttpResponse.json([])),
  http.get(`${TEST_API_BASE_URL}/managed-targets/:id`, ({ params }) =>
    params.id === managedTarget.id
      ? HttpResponse.json<ManagedTarget>(managedTarget)
      : HttpResponse.json({ detail: "managed target not found" }, { status: 404 }),
  ),
  http.get(`${TEST_API_BASE_URL}/search-spaces`, () => HttpResponse.json(searchSpaces)),
  http.get(`${TEST_API_BASE_URL}/knobs`, () => HttpResponse.json(knobCatalog)),
  // No hosts unless a test says so.
  http.get(`${TEST_API_BASE_URL}/hosts`, () => HttpResponse.json([])),
  // No prices unless a test says so.
  http.get(`${TEST_API_BASE_URL}/price-book`, () =>
    HttpResponse.json({ source: "none", discount: 1, nodes: {}, gpus: {} }),
  ),
  // No Prometheus unless a test says so: the template stays as the api's template test checks it.
  http.get(`${TEST_API_BASE_URL}/run-defaults`, () => HttpResponse.json({})),
  http.get(`${TEST_API_BASE_URL}/workload-presets`, () => HttpResponse.json(workloadPresets)),
  http.get(`${TEST_API_BASE_URL}/runs`, ({ request }) => {
    const params = new URL(request.url).searchParams;
    const runs = allRuns.filter(
      (run) =>
        (!params.get("experiment") || run.experiment === params.get("experiment")) &&
        (!params.get("target_type") || run.target_type === params.get("target_type")) &&
        (!params.get("status") || run.status === params.get("status")),
    );
    return HttpResponse.json<ListRunsResponse>(runs);
  }),
  http.get(`${TEST_API_BASE_URL}/runs/:runId`, ({ params }) => {
    const detail = [succeededRunDetail, runningRunDetail].find((run) => run.id === params.runId);
    return detail
      ? HttpResponse.json<RunDetail>(detail)
      : HttpResponse.json({ detail: "run not found" }, { status: 404 });
  }),
  http.get(`${TEST_API_BASE_URL}/runs/:runId/events`, ({ params }) =>
    HttpResponse.json<ListRunEventsResponse>(params.runId === succeededRun.id ? succeededRunEvents : []),
  ),
];

export const server = setupServer(...defaultHandlers);
