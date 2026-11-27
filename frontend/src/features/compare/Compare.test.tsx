import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { RunComparison } from "../../api/types";
import { failedRun, succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";
import { separatedLowest } from "./separation";

const A = {
  ...succeededRun,
  id: "01JCOMPAREA00000000000000A",
  name: "concurrency-100",
  knobs: { "bifrost.concurrency": 100, region: "lab" },
};
const B = {
  ...succeededRun,
  id: "01JCOMPAREB00000000000000B",
  name: "concurrency-400",
  knobs: { "bifrost.concurrency": 400, region: "lab" },
};

function comparison(overrides: Partial<RunComparison> = {}): RunComparison {
  return {
    runs: [A, B],
    knobs: [
      { name: "bifrost.concurrency", values: [100, 400], differs: true },
      { name: "region", values: ["lab", "lab"], differs: false },
    ],
    metrics: [
      {
        stage: "run",
        metric: "slo_max_rps",
        stat: "value",
        values: [210, 330],
      },
      { stage: "0", metric: "offered_rps", stat: "value", values: [300, 300] },
      {
        stage: "0",
        metric: "achieved_rps",
        stat: "value",
        values: [299.2, 299.8],
      },
      { stage: "0", metric: "is_valid", stat: "value", values: [1, 0] },
      { stage: "0", metric: "ttft_seconds", stat: "p95", values: [0.4, 0.2] },
      {
        stage: "0",
        metric: "ttft_seconds",
        stat: "p95_ci_low",
        values: [0.38, 0.19],
      },
      {
        stage: "0",
        metric: "ttft_seconds",
        stat: "p95_ci_high",
        values: [0.42, 0.21],
      },
    ],
    unindexed_run_ids: [],
    ladders_match: true,
    ...overrides,
  };
}

function serve(document: RunComparison) {
  server.use(http.get(`${TEST_API_BASE_URL}/compare`, () => HttpResponse.json(document)));
}

describe("compare", () => {
  it("highlights a best value only when its interval clears every other", () => {
    expect(separatedLowest([0.38, 0.19], [0.42, 0.21])).toBe(1);
    expect(separatedLowest([0.38, 0.2], [0.42, 0.39])).toBe(-1); // overlapping
    expect(separatedLowest([0.38, null], [0.42, null])).toBe(-1); // nothing to clear against
  });

  it("puts the knob diff first and marks the separated best p95", async () => {
    serve(comparison());
    renderApp(`/compare?runs=${A.id},${B.id}`);
    expect(await screen.findByRole("heading", { name: "Comparing 2 runs" })).toBeInTheDocument();
    const diff = screen.getByRole("heading", {
      name: "What differs",
    }).parentElement!;
    expect(within(diff).getByText("bifrost.concurrency")).toBeInTheDocument();
    expect(within(diff).queryByRole("cell", { name: "region" })).toBeNull();
    expect(screen.getByText(/Same in every run/)).toHaveTextContent("Same in every run: region lab");
    const stage = screen.getByRole("heading", {
      name: "Stage 0, 300 RPS offered",
    }).parentElement!;
    const ttft = within(stage).getByRole("row", { name: /TTFT p95/ });
    const cells = within(ttft).getAllByRole("cell");
    expect(cells[2]).toHaveStyle({ fontWeight: "700" });
    expect(cells[1]).not.toHaveStyle({ fontWeight: "700" });
    expect(within(stage).getByRole("row", { name: /Valid/ })).toHaveTextContent("YesNo");
  });

  it("shows each run's priced cost, and a dash for an unpriced one (never $0)", async () => {
    serve(
      comparison({
        metrics: [
          { stage: "run", metric: "slo_max_rps", stat: "value", values: [210, 330] },
          { stage: "run", metric: "infra_usd_per_replica_hour", stat: "value", values: [0.0762, null] },
          { stage: "run", metric: "infra_usd_per_1k_requests", stat: "value", values: [0.00101, null] },
        ],
      }),
    );
    renderApp(`/compare?runs=${A.id},${B.id}`);
    const row = (await screen.findByText("Cost per replica-hour")).closest("tr")!;
    expect(row).toHaveTextContent("Cost per replica-hour$0.0762–");
    const perRequest = screen.getByText("Infrastructure cost per 1k requests").closest("tr")!;
    expect(perRequest).toHaveTextContent("Infrastructure cost per 1k requests$0.00101–");
  });

  it("warns when the ladders differ or a run has no analysis", async () => {
    serve(comparison({ ladders_match: false, unindexed_run_ids: [B.id] }));
    renderApp(`/compare?runs=${A.id},${B.id}`);
    expect(await screen.findByText(/offered different load at some stage/)).toBeInTheDocument();
    expect(screen.getByText(/No analysis yet for concurrency-400/)).toBeInTheDocument();
  });

  it("is reached by selecting runs on the runs list", async () => {
    serve(comparison());
    const { router } = renderApp("/runs");
    const compare = await screen.findByRole("button", { name: "Compare runs" });
    expect(compare).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox", { name: `Select ${succeededRun.name}` }));
    await userEvent.click(screen.getByRole("checkbox", { name: `Select ${failedRun.name}` }));
    await userEvent.click(screen.getByRole("button", { name: "Compare 2 runs" }));
    expect(router.state.location.pathname).toBe("/compare");
    expect(router.state.location.search).toBe(`?runs=${succeededRun.id},${failedRun.id}`);
  });
});
