import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { Comparison, ExperimentAnalysis, ExperimentDetail, ExperimentResource } from "../../api/types";
import { notComparableComparison, winnerComparison } from "../../test/bundles";
import { succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const NAME = "bifrost-concurrency";

function analysisOf(comparison: Comparison): ExperimentAnalysis {
  return {
    id: "01JANALYSIS0000000000000000",
    experiment: NAME,
    created_at: "2026-11-21T10:00:00Z",
    objective: comparison.objective,
    run_ids: comparison.configurations.flatMap((configuration) => configuration.run_ids),
    comparison,
  };
}

function detailWith(analysis: ExperimentAnalysis | null): ExperimentDetail {
  return {
    name: NAME,
    created_at: "2026-11-21T09:00:00Z",
    run_count: 1,
    latest_run_at: "2026-11-21T09:30:00Z",
    varied_knobs: ["bifrost.concurrency"],
    comparison: {
      runs: [{ ...succeededRun, experiment: NAME, knobs: { "bifrost.concurrency": 400 } }],
      knobs: [],
      metrics: [],
      unindexed_run_ids: [],
      ladders_match: true,
    },
    latest_analysis: analysis,
  };
}

function serveExperiment(analysis: ExperimentAnalysis | null) {
  server.use(
    http.get(`${TEST_API_BASE_URL}/experiments/:name`, () => HttpResponse.json<ExperimentDetail>(detailWith(analysis))),
  );
}

describe("experiments", () => {
  it("lists experiments with links", async () => {
    const listing: ExperimentResource[] = [{ name: NAME, created_at: "2026-11-21T09:00:00Z", run_count: 6 }];
    server.use(http.get(`${TEST_API_BASE_URL}/experiments`, () => HttpResponse.json(listing)));
    renderApp("/experiments");
    const link = await screen.findByRole("link", { name: NAME });
    expect(link).toHaveAttribute("href", `/experiments/${NAME}`);
    expect(screen.getByRole("cell", { name: "6" })).toBeInTheDocument();
  });

  it("leads with the knob values to apply, then the configurations", async () => {
    serveExperiment(analysisOf(winnerComparison));
    renderApp(`/experiments/${NAME}`);
    expect(await screen.findByText("High confidence")).toBeInTheDocument();
    expect(screen.getByText(/Best on/).previousElementSibling).toHaveTextContent("bifrost.concurrency 400");
    expect(screen.getByText("High confidence")).toBeInTheDocument();
    const table = screen.getByRole("heading", { name: "Configurations" }).parentElement!;
    const rows = within(table).getAllByRole("row");
    expect(rows[2]).toHaveTextContent("bifrost.concurrency 400");
    expect(within(rows[2]!).getByText("318–342")).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: /SLO RPS per CPU core against p95 time to first token/ }),
    ).toBeInTheDocument();
    expect(screen.getByText(/Left out/)).toBeInTheDocument();
  });

  it("names the workload preset every run used", async () => {
    const comparison = {
      ...winnerComparison,
      comparability: { ...winnerComparison.comparability, workload_preset: { name: "rag-prefill", version: 1 } },
    };
    serveExperiment(analysisOf(comparison));
    renderApp(`/experiments/${NAME}`);
    expect(await screen.findByLabelText("Workload preset rag-prefill@1")).toBeInTheDocument();
  });

  it("says why nothing wins when the runs are not comparable", async () => {
    serveExperiment(analysisOf(notComparableComparison));
    renderApp(`/experiments/${NAME}`);
    expect(await screen.findByText("No configuration wins on slo-max rps")).toBeInTheDocument();
    expect(screen.getByText("These runs are not comparable")).toBeInTheDocument();
    expect(screen.getAllByText("single run").length).toBeGreaterThan(0);
  });

  it("compares on the chosen objective and shows bench's refusal", async () => {
    serveExperiment(null);
    let received: unknown = null;
    server.use(
      http.post(`${TEST_API_BASE_URL}/experiments/:name/analyze`, async ({ request }) => {
        received = await request.json();
        return HttpResponse.json(
          { detail: "experiment bifrost-concurrency has no succeeded run with an analysis.json to compare" },
          { status: 409 },
        );
      }),
    );
    renderApp(`/experiments/${NAME}`);
    expect(await screen.findByText(/Not compared yet/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: "Objective" }));
    await userEvent.click(await screen.findByRole("option", { name: "SLO-max RPS", hidden: true }));
    await userEvent.click(screen.getByRole("button", { name: "Compare configurations" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("has no succeeded run with an analysis.json");
    expect(received).toEqual({ objective: "slo_max_rps" });
  });
});
