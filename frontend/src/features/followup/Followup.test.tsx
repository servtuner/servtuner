import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { Followup, Recommendations, RunDetail } from "../../api/types";
import { bundleFiles, recommendations } from "../../test/bundles";
import { succeededRun, succeededRunDetail } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const RUN = `${TEST_API_BASE_URL}/runs/:runId`;

function withKnobRecommendation(): Recommendations {
  const document = structuredClone(recommendations);
  const resource = document.recommendations[0]!;
  document.recommendations.push({
    ...resource,
    id: "B1/target",
    rule: "B1",
    subject: { kind: "knob", name: "bifrost.concurrency" },
    unit: "requests",
    current: 32,
    recommended: 128,
    derivation_node: null,
    caveats: [],
  });
  return document;
}

const waitingRun = {
  ...succeededRun,
  id: "01JFOLLOWUP000000000000001",
  name: "vllm-baseline-followup",
  status: "awaiting_config" as const,
};
const followup: Followup = {
  runs: [waitingRun, { ...waitingRun, id: "01JFOLLOWUP000000000000002" }],
  knob: "bifrost.concurrency",
  value: 128,
  patch: "providers:\n  servtuner-mock:\n    concurrency_and_buffer_size:\n      concurrency: 128\n",
};

describe("follow-up flow", () => {
  it("offers a follow-up only for a knob change, then creates runs, shows the patch and starts them on confirm", async () => {
    const confirmed: string[] = [];
    let created: unknown = null;
    server.use(
      http.get(`${RUN}/recommendations`, () => HttpResponse.json(withKnobRecommendation())),
      http.get(`${RUN}/files`, () => HttpResponse.json(bundleFiles)),
      http.post(`${RUN}/followup`, async ({ request }) => {
        created = await request.json();
        return HttpResponse.json(followup, { status: 201 });
      }),
      http.post(`${RUN}/confirm-applied`, ({ params }) => {
        confirmed.push(String(params.runId));
        return HttpResponse.json({ ...waitingRun, id: String(params.runId), status: "launching" });
      }),
    );
    renderApp(`/runs/${succeededRun.id}?tab=recommendations`);
    const buttons = await screen.findAllByRole("button", { name: "Confirm with an experiment" });
    expect(buttons).toHaveLength(1); // the CPU request is a resource, not a knob
    await userEvent.click(buttons[0]!);
    await userEvent.click(await screen.findByRole("button", { name: "Create 3 runs" }));
    expect(created).toEqual({ recommendation_id: "B1/target", repeats: 3, experiment: "max-num-seqs" });
    expect(await screen.findByText(/concurrency: 128/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "I've applied it" }));
    expect(await screen.findByText("Runs launched")).toBeInTheDocument();
    expect(confirmed.sort()).toEqual(followup.runs.map((run) => run.id).sort());
  });

  it("names an experiment for the runs when this run has none", async () => {
    let created: unknown = null;
    const generated = "vllm-baseline-bifrost-concurrency";
    server.use(
      http.get(RUN, () => HttpResponse.json({ ...succeededRunDetail, experiment: null })),
      http.get(`${RUN}/recommendations`, () => HttpResponse.json(withKnobRecommendation())),
      http.post(`${RUN}/followup`, async ({ request }) => {
        created = await request.json();
        return HttpResponse.json(
          { ...followup, runs: followup.runs.map((run) => ({ ...run, experiment: generated })) },
          { status: 201 },
        );
      }),
    );
    renderApp(`/runs/${succeededRun.id}?tab=recommendations`);
    await userEvent.click(await screen.findByRole("button", { name: "Confirm with an experiment" }));
    const field = await screen.findByLabelText("Experiment");
    expect(field).toHaveValue("");
    expect(screen.getByText(/servtuner names one after the run and the knob/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create 3 runs" }));
    expect(created).toEqual({ recommendation_id: "B1/target", repeats: 3 });
    expect(await screen.findByRole("link", { name: generated })).toHaveAttribute(
      "href",
      expect.stringContaining(`/experiments/${generated}`),
    );
  });

  it("lets a waiting run be started from its own page", async () => {
    const detail: RunDetail = { ...succeededRunDetail, ...waitingRun, summary: null };
    let confirmed = false;
    server.use(
      http.get(RUN, () => HttpResponse.json(detail)),
      http.post(`${RUN}/confirm-applied`, () => {
        confirmed = true;
        return HttpResponse.json({ ...waitingRun, status: "launching" });
      }),
    );
    renderApp(`/runs/${waitingRun.id}`);
    expect(await screen.findByText("Waiting for the change this run tests")).toBeInTheDocument();
    expect(screen.getAllByText("Awaiting config change").length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole("button", { name: "I've applied it" }));
    await screen.findByRole("button", { name: "I've applied it" });
    expect(confirmed).toBe(true);
  });
});
