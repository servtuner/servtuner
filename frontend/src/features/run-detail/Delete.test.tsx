import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { ExperimentDeleteImpact, ExperimentDetail, RunDeleteImpact } from "../../api/types";
import { succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL as API } from "../../test/server";

const RUN = succeededRun.id;
const NAME = "tune-qwen";

const runImpact: RunDeleteImpact = {
  run_id: RUN,
  blocks: [],
  analyses: ["01JANALYSIS0000000000000000"],
  bundle_files: 12,
  notes: ["tuning session tune-qwen keeps its recommendation; its history will show this run as deleted"],
};

const experimentDetail: ExperimentDetail = {
  name: NAME,
  created_at: "2026-11-21T09:00:00Z",
  run_count: 2,
  latest_run_at: "2026-11-21T09:30:00Z",
  varied_knobs: [],
  comparison: { runs: [], knobs: [], metrics: [], unindexed_run_ids: [], ladders_match: true },
  latest_analysis: null,
};

const experimentImpact: ExperimentDeleteImpact = {
  name: NAME,
  blocks: [],
  runs: ["01JRUN00000000000000000001", "01JRUN00000000000000000002"],
  analyses: 1,
  bundle_files: 24,
  sessions: ["01JSESSION0000000000000000"],
  scenarios: [{ id: "01JSCENARIO000000000000000", name: "peak 400" }],
  notes: [],
};

async function openDialog(label: string) {
  await userEvent.click(await screen.findByRole("button", { name: label }));
  return screen.findByRole("dialog");
}

describe("deleting a run", () => {
  it("shows what goes before deleting, then leaves for the runs list", async () => {
    let deleted = false;
    server.use(
      http.get(`${API}/runs/:runId/delete-impact`, () => HttpResponse.json(runImpact)),
      http.delete(`${API}/runs/:runId`, () => {
        deleted = true;
        return HttpResponse.json(runImpact);
      }),
    );
    renderApp(`/runs/${RUN}`);
    const dialog = await openDialog("Delete run");
    expect(await within(dialog).findByText("the run and its 12 bundle files")).toBeInTheDocument();
    expect(within(dialog).getByText(/1 experiment comparison that included it/)).toBeInTheDocument();
    expect(within(dialog).getByText(/keeps its recommendation/)).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "Files" })).toHaveAttribute("href", `/runs/${RUN}?tab=files`);
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(deleted).toBe(true));
    expect(await screen.findByRole("heading", { name: "Runs" })).toBeInTheDocument();
  });

  it("names what blocks the delete and keeps confirm disabled", async () => {
    server.use(
      http.get(`${API}/runs/:runId/delete-impact`, () =>
        HttpResponse.json({ ...runImpact, blocks: ["schedule nightly uses it as its baseline: choose another first"] }),
      ),
    );
    renderApp(`/runs/${RUN}`);
    const dialog = await openDialog("Delete run");
    expect(await within(dialog).findByText(/schedule nightly uses it as its baseline/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("is never offered on the runs list", async () => {
    renderApp("/runs");
    expect(await screen.findByRole("heading", { name: "Runs" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /delete/i })).not.toBeInTheDocument();
  });
});

describe("deleting an experiment", () => {
  it("needs the scenarios ticked before it can go, and sends that choice", async () => {
    let query = "";
    server.use(
      http.get(`${API}/experiments/:name`, () => HttpResponse.json(experimentDetail)),
      http.get(`${API}/experiments/:name/delete-impact`, () => HttpResponse.json(experimentImpact)),
      http.delete(`${API}/experiments/:name`, ({ request }) => {
        query = new URL(request.url).search;
        return HttpResponse.json(experimentImpact);
      }),
    );
    renderApp(`/experiments/${NAME}`);
    const dialog = await openDialog("Delete experiment");
    expect(await within(dialog).findByText("2 runs and their 24 bundle files")).toBeInTheDocument();
    expect(within(dialog).getByText("1 finished tuning session")).toBeInTheDocument();
    const confirm = within(dialog).getByRole("button", { name: "Delete" });
    expect(confirm).toBeDisabled();
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /peak 400/ }));
    expect(within(dialog).getByText("1 saved scenario")).toBeInTheDocument();
    await userEvent.click(confirm);
    await waitFor(() => expect(query).toBe("?delete_scenarios=true"));
  });
});
