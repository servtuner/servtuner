import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { Schedule, ScheduleCreate, ScheduleDetail, ScheduleUpdate } from "../../api/types";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const API = TEST_API_BASE_URL;
const ID = "01JSCHED00000000000000000A";
const BASE = "01JRUN0000000000000000000A";
const LATEST = "01JRUN0000000000000000000C";

const schedule: Schedule = {
  id: ID,
  name: "nightly vllm",
  spec: { name: "nightly" },
  every_hours: 24,
  start_at: "2026-11-01T02:00:00Z",
  next_run_at: "2026-11-23T02:00:00Z",
  is_enabled: true,
  regression_tolerance: 0.1,
  last_run_id: LATEST,
  created_at: "2026-11-01T01:00:00Z",
};

const detail: ScheduleDetail = {
  ...schedule,
  effective_baseline_run_id: BASE,
  baseline_slo_max_rps: 100,
  runs: [
    {
      run_id: BASE,
      status: "succeeded",
      created_at: "2026-11-20T02:00:00Z",
      slo_max_rps: 100,
      verdict: "baseline",
      change: 0,
    },
    {
      run_id: "01JRUN0000000000000000000B",
      status: "succeeded",
      created_at: "2026-11-21T02:00:00Z",
      slo_max_rps: 96,
      verdict: "steady",
      change: -0.04,
    },
    {
      run_id: LATEST,
      status: "succeeded",
      created_at: "2026-11-22T02:00:00Z",
      slo_max_rps: 80,
      verdict: "regressed",
      change: -0.2,
    },
  ],
};

describe("schedules", () => {
  it("lists schedules and creates one from YAML", async () => {
    const created: ScheduleCreate[] = [];
    server.use(
      http.get(`${API}/schedules`, () => HttpResponse.json([schedule])),
      http.post(`${API}/schedules`, async ({ request }) => {
        created.push((await request.json()) as ScheduleCreate);
        return HttpResponse.json(schedule, { status: 201 });
      }),
      http.get(`${API}/schedules/${ID}`, () => HttpResponse.json(detail)),
    );
    renderApp("/schedules");
    expect(await screen.findByRole("link", { name: "nightly vllm" })).toBeInTheDocument();
    expect(screen.getByText("every day")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: /Name/ }), "weekly");
    await userEvent.type(screen.getByRole("textbox", { name: /RunSpec/ }), "name: weekly{enter}labels: {{}");
    await userEvent.click(screen.getByRole("button", { name: "Create schedule" }));
    expect(await screen.findByRole("heading", { name: "nightly vllm" })).toBeInTheDocument();
    expect(created).toEqual([{ name: "weekly", spec: { name: "weekly", labels: {} }, every_hours: 24 }]);
  });

  it("flags a regression and moves the baseline", async () => {
    const updates: ScheduleUpdate[] = [];
    server.use(
      http.get(`${API}/schedules/${ID}`, () => HttpResponse.json(detail)),
      http.get(`${API}/schedules`, () => HttpResponse.json([schedule])),
      http.patch(`${API}/schedules/${ID}`, async ({ request }) => {
        updates.push((await request.json()) as ScheduleUpdate);
        return HttpResponse.json(schedule);
      }),
    );
    renderApp(`/schedules/${ID}`);
    expect(await screen.findByText("The latest run regressed")).toBeInTheDocument();
    expect(screen.getByText(/20% below the baseline's 100 RPS/)).toBeInTheDocument();
    const row = screen.getByText(LATEST).closest("tr")!;
    expect(within(row).getByText("Regressed")).toBeInTheDocument();
    expect(within(row).getByText("-20%")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Compare the latest run/ })).toHaveAttribute(
      "href",
      `/compare?runs=${encodeURIComponent(`${BASE},${LATEST}`)}`,
    );
    await userEvent.click(within(row).getByRole("button", { name: "Use as baseline" }));
    expect(updates).toEqual([{ baseline_run_id: LATEST }]);
  });
});
