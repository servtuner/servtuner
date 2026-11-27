import { screen, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { ManagedTarget, TuningSession } from "../../api/types";
import { managedTarget, succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const API = TEST_API_BASE_URL;

const deployed: ManagedTarget = {
  ...managedTarget,
  deployment: { knobs: {}, args: [], deployed_at: "2026-11-25T09:00:00Z" },
};

const running = {
  id: "01JSESSION0000000000000000B",
  managed_target_id: managedTarget.id,
  status: "running",
  experiment: "tune-qwen-00000b",
  request: { preset: "chat-short@1" },
  step: { phase: "propose" },
  runs_started: 2,
} as unknown as TuningSession;

describe("overview", () => {
  it("is the home page: managed vLLMs with their state, sessions and the newest runs", async () => {
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([deployed])),
      http.get(`${API}/tuning-sessions`, () => HttpResponse.json([running])),
    );
    renderApp("/");
    expect(await screen.findByRole("heading", { name: "Overview" })).toBeInTheDocument();
    const vllm = (await screen.findByRole("link", { name: "qwen" })).closest("div")!.parentElement!;
    expect(within(vllm).getByText("Deployed")).toBeInTheDocument();
    const session = await screen.findByRole("link", { name: "tune-qwen-00000b" });
    expect(session).toHaveAttribute("href", `/tuning/${running.id}`);
    expect(screen.getByText(/Choosing the next configuration/)).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: succeededRun.name })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a vLLM host" })).toHaveAttribute(
      "href",
      "/managed-targets?add=host&next=tuning",
    );
  });

  it("says what to do first when nothing exists yet", async () => {
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([])),
      http.get(`${API}/tuning-sessions`, () => HttpResponse.json([])),
      http.get(`${API}/runs`, () => HttpResponse.json([])),
    );
    renderApp("/");
    expect(await screen.findByText(/add a vLLM for servtuner to deploy, benchmark and tune/)).toBeInTheDocument();
    expect(await screen.findByText(/No sessions yet/)).toBeInTheDocument();
    expect(await screen.findByText("No runs yet.")).toBeInTheDocument();
  });
});
