import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { PlanCreate, Scenario } from "../../api/types";
import { plan } from "../../test/bundles";
import { succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const API = TEST_API_BASE_URL;

/** Serves plans: with overrides, bench's answer moves replicas_peak to 9. */
function servePlans(bodies: PlanCreate[] = []) {
  server.use(
    http.get(`${API}/scenarios`, () => HttpResponse.json([])),
    http.post(`${API}/scenarios/evaluate`, async ({ request }) => {
      const body = (await request.json()) as PlanCreate;
      bodies.push(body);
      if (!body.request.overrides?.length) return HttpResponse.json(plan);
      const tier = { ...plan.tiers[0]!, replicas_peak: 9 };
      return HttpResponse.json({ ...plan, tiers: [tier] });
    }),
  );
  return bodies;
}

describe("capacity planner", () => {
  it("plans the runs from the URL through bench, with its checks", async () => {
    const bodies = servePlans();
    renderApp(`/planner?runs=${succeededRun.id}`);
    expect(await screen.findByText(/runs at 44% of its measured SLO-max/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /^bifrost/ })).toBeInTheDocument();
    expect(bodies.at(-1)).toEqual({
      request: {
        peak_rps: 100,
        slos: { ttft_p95: "500ms" },
        availability: { min_replicas: 2, spare: false, zones: 1 },
      },
      run_ids: [succeededRun.id],
    });
  });

  it("pins a derived value and shows the plan without it crossed out", async () => {
    const bodies = servePlans();
    renderApp(`/planner?runs=${succeededRun.id}`);
    const tree = within(
      (await screen.findByRole("heading", { name: "How the plan is calculated" })).closest("div")!.parentElement!,
    );
    await userEvent.click(tree.getByRole("button", { name: "Pin" }));
    expect(await screen.findByText("9")).toBeInTheDocument();
    expect(bodies.some((body) => body.request.overrides?.[0]?.kind === "pin")).toBe(true);
    const peak = screen.getAllByText("Replicas at peak")[0]!.closest("tr")!; // the tier table, before the tree
    expect(within(peak).getByText(String(plan.tiers[0]!.replicas_peak))).toHaveStyle({
      textDecoration: "line-through",
    });
    await userEvent.click(screen.getByRole("button", { name: /Reset all/ }));
    expect(await within(peak).findByText(String(plan.tiers[0]!.replicas_peak))).not.toHaveStyle({
      textDecoration: "line-through",
    });
  });

  it("shows byte values in binary units and leaves other units alone", async () => {
    const base = plan.derivation.nodes[0]!;
    const workingSet = {
      ...base,
      id: "bifrost.ws",
      kind: "measured" as const,
      label: "Working set",
      unit: "bytes",
      value: 1.5 * 1024 ** 3,
    };
    const rate = { ...workingSet, id: "bifrost.rate", label: "Offered rate", unit: "requests/s", value: 1500 };
    server.use(
      http.get(`${API}/scenarios`, () => HttpResponse.json([])),
      http.post(`${API}/scenarios/evaluate`, () =>
        HttpResponse.json({ ...plan, derivation: { ...plan.derivation, nodes: [base, workingSet, rate] } }),
      ),
    );
    renderApp(`/planner?runs=${succeededRun.id}`);
    const workingSetRow = (await screen.findByText("Working set")).closest("tr")!;
    expect(within(workingSetRow).getByText("1.5")).toBeInTheDocument();
    expect(within(workingSetRow).getByText("GiB")).toBeInTheDocument();
    const rateRow = screen.getByText("Offered rate").closest("tr")!;
    expect(within(rateRow).getByText("1500")).toBeInTheDocument();
    expect(within(rateRow).getByText("requests/s")).toBeInTheDocument();
  });

  it("asks for runs before planning, and shows bench's reason when it cannot plan", async () => {
    servePlans();
    server.use(
      http.post(`${API}/scenarios/evaluate`, () =>
        HttpResponse.json({ detail: "the SLO is stricter than the lightest measured stage" }, { status: 422 }),
      ),
    );
    renderApp("/planner");
    expect(await screen.findByText(/Choose at least one analysed run/)).toBeInTheDocument();
    renderApp(`/planner?runs=${succeededRun.id}`);
    expect(await screen.findByText(/stricter than the lightest measured stage/)).toBeInTheDocument();
  });

  it("saves a scenario and reopens it from its link", async () => {
    servePlans();
    let saved: Scenario | null = null;
    server.use(
      http.post(`${API}/scenarios`, async ({ request }) => {
        const body = (await request.json()) as { name: string; scenario: PlanCreate };
        saved = {
          id: "01JSCENARIO000000000000000",
          name: body.name,
          created_at: "2026-11-22T10:00:00Z",
          scenario: body.scenario,
        } as Scenario;
        return HttpResponse.json(saved, { status: 201 });
      }),
      http.get(`${API}/scenarios/:id`, () => HttpResponse.json(saved)),
    );
    const { router } = renderApp(`/planner?runs=${succeededRun.id}`);
    await screen.findByText(/runs at 44%/);
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "black friday");
    await userEvent.click(screen.getByRole("button", { name: "Save scenario" }));
    expect(await screen.findByRole("heading", { name: "black friday" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/planner/01JSCENARIO000000000000000");
  });
});
