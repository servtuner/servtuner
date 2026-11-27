import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ExperimentResource, ListRunsResponse } from "../../api/types";
import { failedRun, runningRun, succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

function countRunsRequests(runs: ListRunsResponse) {
  const counter = { requests: 0 };
  server.use(
    http.get(`${TEST_API_BASE_URL}/runs`, () => {
      counter.requests += 1;
      return HttpResponse.json<ListRunsResponse>(runs);
    }),
  );
  return counter;
}

// The suggestion list shares the input's label, so find the input by role.
const experimentInput = () => screen.getByRole("combobox", { name: "Experiment" });

describe("runs list", () => {
  afterEach(() => vi.useRealTimers());

  it("renders every run", async () => {
    renderApp("/runs");
    expect(await screen.findByRole("link", { name: succeededRun.name })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: runningRun.name })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: failedRun.name })).toBeInTheDocument();
    const table = screen.getByRole("table");
    expect(within(table).getByText("Succeeded")).toBeInTheDocument();
    expect(within(table).getByText("Bifrost")).toBeInTheDocument();
    const row = screen.getByRole("link", { name: succeededRun.name }).closest("tr")!;
    expect(within(row).getByLabelText("Model example/model-8b")).toBeInTheDocument();
  });

  it("shows each run's SLO-max", async () => {
    countRunsRequests([{ ...succeededRun, slo_max_rps: 42.5 }, failedRun]);
    renderApp("/runs");
    const row = (await screen.findByRole("link", { name: succeededRun.name })).closest("tr")!;
    expect(within(row).getByText("42.5")).toBeInTheDocument();
    const failed = screen.getByRole("link", { name: failedRun.name }).closest("tr")!;
    expect(within(failed).getByText("–")).toBeInTheDocument();
  });

  it("marks a run that used a workload preset", async () => {
    countRunsRequests([{ ...succeededRun, workload_preset: "rag-prefill@1" }, failedRun]);
    renderApp("/runs");
    const row = (await screen.findByRole("link", { name: succeededRun.name })).closest("tr")!;
    expect(within(row).getByLabelText("Workload preset rag-prefill@1")).toBeInTheDocument();
    const custom = screen.getByRole("link", { name: failedRun.name }).closest("tr")!;
    expect(within(custom).queryByLabelText(/Workload preset/)).toBeNull();
  });

  it("shows local time with the UTC value on hover", async () => {
    renderApp("/runs");
    await screen.findByRole("link", { name: succeededRun.name });
    const row = screen.getByRole("row", { name: new RegExp(succeededRun.name) });
    expect(within(row).getByTitle("2026-10-28T12:00:00.000Z (UTC)")).toBeInTheDocument();
  });

  it("filters by status and target type through the API query parameters", async () => {
    const user = userEvent.setup();
    renderApp("/runs");
    await screen.findByRole("link", { name: succeededRun.name });

    await user.selectOptions(screen.getByLabelText("Status"), "failed");
    await waitFor(() => expect(screen.queryByRole("link", { name: succeededRun.name })).toBeNull());
    expect(screen.getByRole("link", { name: failedRun.name })).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Status"), "");
    await user.selectOptions(screen.getByLabelText("Target type"), "bifrost");
    await waitFor(() => expect(screen.queryByRole("link", { name: failedRun.name })).toBeNull());
    expect(screen.getByRole("link", { name: runningRun.name })).toBeInTheDocument();
  });

  it("filters by experiment from the URL", async () => {
    renderApp("/runs?experiment=router-overhead");
    expect(await screen.findByRole("link", { name: runningRun.name })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: succeededRun.name })).toBeNull();
    expect(experimentInput()).toHaveValue("router-overhead");
  });

  it("suggests existing experiments and filters by the one picked", async () => {
    const user = userEvent.setup();
    server.use(
      http.get(`${TEST_API_BASE_URL}/experiments`, () =>
        HttpResponse.json<ExperimentResource[]>([
          { name: "router-overhead", run_count: 1, created_at: "2026-10-28T12:00:00Z" },
          { name: "kv-sizing", run_count: 2, created_at: "2026-10-28T12:00:00Z" },
        ]),
      ),
    );
    renderApp("/runs");
    await screen.findByRole("link", { name: succeededRun.name });

    await user.type(experimentInput(), "rout");
    const option = await screen.findByRole("option", { name: "router-overhead", hidden: true });
    expect(screen.queryByRole("option", { name: "kv-sizing", hidden: true })).toBeNull();
    await user.click(option);
    expect(experimentInput()).toHaveValue("router-overhead");
    await waitFor(() => {
      expect(screen.queryByRole("link", { name: succeededRun.name })).toBeNull();
      expect(screen.getByRole("link", { name: runningRun.name })).toBeInTheDocument();
    });
  });

  it("polls every 2s while a run is non-terminal", async () => {
    const counter = countRunsRequests([runningRun]);
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderApp("/runs");
    await screen.findByRole("link", { name: runningRun.name });
    const requestsBeforePolling = counter.requests;
    await act(() => vi.advanceTimersByTimeAsync(2100));
    await waitFor(() => expect(counter.requests).toBeGreaterThan(requestsBeforePolling));
  });

  it("stops polling when every run is terminal", async () => {
    const counter = countRunsRequests([succeededRun, failedRun]);
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderApp("/runs");
    await screen.findByRole("link", { name: succeededRun.name });
    const requestsAfterLoad = counter.requests;
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(counter.requests).toBe(requestsAfterLoad);
  });

  it("shows an error when the API fails", async () => {
    server.use(http.get(`${TEST_API_BASE_URL}/runs`, () => new HttpResponse(null, { status: 500 })));
    renderApp("/runs");
    expect(await screen.findByRole("alert")).toHaveTextContent("failed with HTTP 500");
  });

  it("shows an empty state", async () => {
    server.use(http.get(`${TEST_API_BASE_URL}/runs`, () => HttpResponse.json<ListRunsResponse>([])));
    renderApp("/runs");
    expect(await screen.findByText("No runs yet.")).toBeInTheDocument();
  });
});
