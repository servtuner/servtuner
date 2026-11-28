import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { ManagedTarget, TuningSession, TuningSessionCreate } from "../../api/types";
import { searchSpaces } from "../../test/bundles";
import { managedTarget as target, succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const API = TEST_API_BASE_URL;

const session: TuningSession = {
  id: "01JSESSION0000000000000000A",
  managed_target_id: target.id,
  status: "awaiting_approval",
  experiment: "tune-qwen-00000a",
  request: {
    managed_target_id: target.id,
    preset: "chat-short@1",
    streaming: true,
    slos: { ttft_p95: "500ms" },
    space: {},
    budget: { max_runs: 40, max_hours: 8 },
    mode: "manual",
    stage_duration: "90s",
    warmup: "30s",
    experiment: null,
    reuse_from: [],
  },
  step: {
    phase: "approval",
    proposed: { action: "boot_probe", config_id: "c-0000aaaa" },
  },
  boot_results: [],
  history: [],
  runs_started: 0,
  created_at: "2026-11-23T10:00:00Z",
  updated_at: "2026-11-23T10:00:00Z",
};

const done: TuningSession = {
  ...session,
  status: "done",
  step: null,
  runs_started: 6,
  history: [
    {
      action: "boot_probe",
      config_id: "c-0000aaaa",
      knobs: { "vllm.tensor_parallel_size": 1 },
      rationale: { kind: "feasibility", text: "boot with no load" },
      boot_result: {
        outcome: "infeasible",
        reason: "no-kv-blocks: no memory left for KV cache blocks",
      },
    },
    {
      action: "run",
      config_id: "c-0000bbbb",
      knobs: { "vllm.tensor_parallel_size": 2 },
      rationale: { kind: "baseline", text: "first run" },
      run_ids: ["r1", "r2", "r3"],
    },
  ],
  outcome: {
    outcome: "converged",
    recommendation: {
      command: "vllm serve Qwen/Qwen3-32B --tensor-parallel-size=2",
      knobs: { "vllm.tensor_parallel_size": 2 },
      confirmed: true,
      evidence: { confidence: "high", is_significant: true },
      infeasible: [{ knobs: { "vllm.tensor_parallel_size": 1 }, outcome: "infeasible" }],
      notes: ["vllm.gpu_memory_utilization=0.95 booted with the same KV cache as vllm.gpu_memory_utilization=0.75"],
      kv_sizing: { bound: "memory", kv_cache_gib: 3.89, requested_gib: 6 },
    },
  },
};

describe("tuning", () => {
  it("lists sessions with their managed target", async () => {
    server.use(
      http.get(`${API}/tuning-sessions`, () => HttpResponse.json([done])),
      http.get(`${API}/managed-targets`, () => HttpResponse.json([target])),
    );
    renderApp("/tuning");
    const row = (await screen.findByRole("link", { name: done.experiment })).closest("tr")!;
    expect(within(row).getByText("Done")).toBeInTheDocument();
    expect(within(row).getByText("qwen")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New session" })).not.toHaveAttribute("data-disabled");
  });

  it("sends a user with no managed server to add one first", async () => {
    server.use(
      http.get(`${API}/tuning-sessions`, () => HttpResponse.json([])),
      http.get(`${API}/managed-targets`, () => HttpResponse.json([])),
    );
    renderApp("/tuning");
    expect(await screen.findByRole("link", { name: "add one first" })).toHaveAttribute("href", "/managed-targets");
    expect(screen.getByRole("link", { name: "New session" })).toHaveAttribute("data-disabled", "true");
  });

  it("adds a vLLM host from Tuning and comes back to tune it", async () => {
    const user = userEvent.setup();
    const host = { ...target, id: "01JMANAGEDHOST000000000000A", name: "rtx" };
    let saved = false;
    server.use(
      http.get(`${API}/tuning-sessions`, () => HttpResponse.json([])),
      http.get(`${API}/managed-targets`, () => HttpResponse.json(saved ? [host] : [])),
      http.post(`${API}/managed-targets`, () => {
        saved = true;
        return HttpResponse.json(host, { status: 201 });
      }),
    );
    const { router } = renderApp("/tuning");
    await user.click(await screen.findByRole("link", { name: "Add a vLLM host" }));
    // The form opens in host mode: the launcher's fields, not the image.
    expect(await screen.findByLabelText(/^Launcher URL/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Image/)).toBeNull();
    await user.type(screen.getByLabelText(/^Name(?!space)/), "rtx");
    await user.type(screen.getByLabelText(/^Model/), "Qwen/Qwen2.5-0.5B-Instruct");
    await user.type(screen.getByLabelText(/^Launcher URL/), "http://gpu-host.localtest.me:9123");
    await user.click(screen.getByRole("button", { name: "Save and tune it" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/tuning/new"));
    expect(router.state.location.search).toBe(`?target=${host.id}`);
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: /^Managed target/ })).toHaveValue("rtx (Qwen/Qwen3-32B)"),
    );
  }, 15_000); // types a whole host form

  it("starts a session with the preset's SLOs and opens it", async () => {
    let created: TuningSessionCreate | undefined;
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([target])),
      http.post(`${API}/tuning-sessions`, async ({ request }) => {
        created = (await request.json()) as TuningSessionCreate;
        return HttpResponse.json(session, { status: 201 });
      }),
      http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(session)),
    );
    const { router } = renderApp("/tuning/new");
    await userEvent.click(await screen.findByRole("combobox", { name: /^Managed target/ }));
    await userEvent.click(await screen.findByRole("option", { name: /qwen/, hidden: true }));
    const presets = screen.getByRole("combobox", { name: /^Workload preset/ });
    await waitFor(() => expect(presets).toBeEnabled());
    await userEvent.click(presets);
    await userEvent.click(
      await screen.findByRole("option", {
        name: "rag-prefill@1",
        hidden: true,
      }),
    );
    expect(screen.getByLabelText("TTFT p95")).toHaveValue("2s");
    await userEvent.click(screen.getByText("Wait for my approval"));
    await userEvent.click(screen.getByRole("button", { name: "Start session" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/tuning/${session.id}`));
    expect(created).toMatchObject({
      managed_target_id: target.id,
      preset: "rag-prefill@1",
      slos: { ttft_p95: "2s", itl_p95: "60ms" },
      mode: "manual",
      budget: { max_runs: 40, max_hours: 8 },
    });
    // No fixture space fits a 2-GPU target, so the first of the catalog starts it.
    expect(created?.space).toEqual(searchSpaces[0]!.space);
  });

  it("starts from a space that fits the target, kept until the user picks another", async () => {
    const user = userEvent.setup();
    const oneGpu = { ...target, spec: { ...target.spec, gpus: 1 } } as ManagedTarget;
    server.use(http.get(`${API}/managed-targets`, () => HttpResponse.json([oneGpu])));
    renderApp(`/tuning/new?target=${oneGpu.id}`);
    const startFrom = await screen.findByRole("combobox", { name: /^Start from/ });
    await waitFor(() => expect(startFrom).toHaveValue("One GPU"));
    // Its knobs as chips; the YAML on request.
    const tuned = await screen.findByRole("list", { name: "Tuned knobs" });
    await waitFor(() => expect(tuned).toHaveTextContent("Max sequences 16–512"));
    await user.click(screen.getByRole("button", { name: "Edit as YAML" }));
    const space = screen.getByRole("textbox", { name: /^Search space/ });
    expect((space as HTMLTextAreaElement).value).toContain("Starting space single-gpu@1");
    await user.click(startFrom);
    expect(await screen.findByText("Fits qwen")).toBeInTheDocument();
    expect(screen.getByText("Other starting spaces")).toBeInTheDocument();
    // An edit stays; picking another start replaces it.
    await user.clear(space);
    await user.type(space, "objective: slo_max_rps");
    expect(space).toHaveValue("objective: slo_max_rps");
    await user.click(startFrom);
    await user.click(await screen.findByRole("option", { name: searchSpaces[1]!.title, hidden: true }));
    expect((space as HTMLTextAreaElement).value).toContain("Starting space host-scheduler@1");
  });

  it("edits the space's knobs as chips and sends them in the space", async () => {
    const user = userEvent.setup();
    let created: TuningSessionCreate | undefined;
    const oneGpu = { ...target, spec: { ...target.spec, gpus: 1 } } as ManagedTarget;
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([oneGpu])),
      http.post(`${API}/tuning-sessions`, async ({ request }) => {
        created = (await request.json()) as TuningSessionCreate;
        return HttpResponse.json(session, { status: 201 });
      }),
      http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(session)),
    );
    renderApp(`/tuning/new?target=${oneGpu.id}`);
    const presets = await screen.findByRole("combobox", { name: /^Workload preset/ });
    await waitFor(() => expect(presets).toBeEnabled());
    await user.click(presets);
    await user.click(await screen.findByRole("option", { name: "rag-prefill@1", hidden: true }));
    const tuned = await screen.findByRole("list", { name: "Tuned knobs" });
    await waitFor(() => expect(tuned).toHaveTextContent("Max sequences 16–512"));

    // Narrow a range.
    await user.click(within(tuned).getByRole("button", { name: /^Max sequences/ }));
    expect(screen.getByText("Range")).toBeInTheDocument(); // the catalog says it takes bounds
    const to = screen.getByRole("textbox", { name: "To" });
    await user.clear(to);
    await user.type(to, "256");
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(tuned).toHaveTextContent("Max sequences 16–256");

    // Tune a knob outside the starting space: a boolean starts with both states.
    const addTuned = within(tuned).getByRole("combobox", { name: "Add a knob" });
    await waitFor(() => expect(addTuned).toBeEnabled());
    await user.type(addTuned, "prefix caching");
    await user.click(await within(tuned).findByRole("option", { name: /Prefix caching/, hidden: true }));
    expect(screen.getByRole("combobox", { name: "Stage" })).toHaveValue("Fine tuning");
    expect(screen.queryByText("Range")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(tuned).toHaveTextContent("Prefix caching off or on");

    // Fix one for every configuration; a tuned knob is not offered there.
    const fixed = screen.getByRole("list", { name: "Fixed knobs" });
    await user.type(within(fixed).getByRole("combobox", { name: "Add a knob" }), "gpu memory");
    await user.click(await within(fixed).findByRole("option", { name: /GPU memory utilization/, hidden: true }));
    expect(screen.getByRole("button", { name: "Start session" })).toBeDisabled(); // no value yet
    await user.type(screen.getByRole("textbox", { name: "Value" }), "0.85");
    await user.click(screen.getByRole("button", { name: "Done" }));

    await user.click(screen.getByRole("button", { name: "Start session" }));
    await waitFor(() => expect(created).toBeDefined());
    expect(created?.space).toEqual({
      base: { "vllm.gpu_memory_utilization": 0.85 },
      knobs: [
        { name: "vllm.max_num_seqs", tier: "scheduler", min: 16, max: 256, default: 128 },
        { name: "vllm.enable_prefix_caching", tier: "fine_tuning", values: [false, true] },
      ],
    });
  }, 20_000); // many typed steps

  it("ranks by what the target can compute, cost included when it is priced", async () => {
    const user = userEvent.setup();
    let created: TuningSessionCreate | undefined;
    const priced = {
      ...target,
      spec: { ...target.spec, gpus: 1 },
      session_objectives: ["slo_max_rps", "slo_rps_per_gpu", "infra_cost_per_1k_requests"],
    } as ManagedTarget;
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([priced])),
      http.post(`${API}/tuning-sessions`, async ({ request }) => {
        created = (await request.json()) as TuningSessionCreate;
        return HttpResponse.json(session, { status: 201 });
      }),
      http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(session)),
    );
    renderApp(`/tuning/new?target=${priced.id}`);
    const rankBy = await screen.findByRole("combobox", { name: /^Rank by/ });
    expect(rankBy).toHaveValue("SLO-max RPS"); // the target's default
    await user.click(rankBy);
    await user.click(await screen.findByRole("option", { name: "Infrastructure cost per 1k requests", hidden: true }));
    const presets = screen.getByRole("combobox", { name: /^Workload preset/ });
    await waitFor(() => expect(presets).toBeEnabled());
    await user.click(presets);
    await user.click(await screen.findByRole("option", { name: "rag-prefill@1", hidden: true }));
    await user.click(screen.getByRole("button", { name: "Start session" }));
    await waitFor(() => expect(created).toBeDefined());
    expect(created?.space).toMatchObject({ objective: "infra_cost_per_1k_requests" });
  }, 15_000);

  it("starts the probes from the target's own SLO-max for the preset, else the preset's hint", async () => {
    const user = userEvent.setup();
    let created: TuningSessionCreate | undefined;
    let measured = true;
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([target])),
      http.get(`${API}/runs`, () =>
        HttpResponse.json(
          measured
            ? [
                { ...succeededRun, workload_preset: "rag-prefill@1", slo_max_rps: 20 },
                { ...succeededRun, id: "old", workload_preset: "rag-prefill@1", slo_max_rps: 5 },
              ]
            : [{ ...succeededRun, workload_preset: "chat-short@1", slo_max_rps: 20 }],
        ),
      ),
      http.post(`${API}/tuning-sessions`, async ({ request }) => {
        created = (await request.json()) as TuningSessionCreate;
        return HttpResponse.json(session, { status: 201 });
      }),
      http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(session)),
    );
    const { unmount } = renderApp(`/tuning/new?target=${target.id}`);
    const presets = await screen.findByRole("combobox", { name: /^Workload preset/ });
    await waitFor(() => expect(presets).toBeEnabled());
    await user.click(presets);
    await user.click(await screen.findByRole("option", { name: "rag-prefill@1", hidden: true }));
    expect(await screen.findByText(/SLO-max 20 RPS with rag-prefill@1/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Use these probe rates" }));
    await user.click(screen.getByRole("button", { name: "Start session" }));
    await waitFor(() => expect(created).toBeDefined());
    expect((created!.space as { ladder: unknown }).ladder).toMatchObject({ probe_rps: [10, 15, 20, 25] });
    unmount();

    // Another preset's runs do not count: the hand-written hint is offered, with its source.
    measured = false;
    renderApp(`/tuning/new?target=${target.id}`);
    const again = await screen.findByRole("combobox", { name: /^Workload preset/ });
    await waitFor(() => expect(again).toBeEnabled());
    await user.click(again);
    await user.click(await screen.findByRole("option", { name: "rag-prefill@1", hidden: true }));
    expect(await screen.findByText(/No run of qwen measured rag-prefill@1 yet/)).toBeInTheDocument();
  });

  it("shows the session's Pareto set, or why there is none", async () => {
    const withPareto: TuningSession = {
      ...done,
      outcome: {
        ...(done.outcome as object),
        comparison: { analysis_id: "a1", objective: "slo_rps_per_gpu" },
        pareto: [
          {
            knobs: { "vllm.tensor_parallel_size": 2, "vllm.max_num_seqs": 128 },
            run_ids: ["r1", "r2", "r3"],
            objective_value: 18.4,
            ttft_p95_seconds: 0.21,
          },
        ],
      },
    };
    server.use(http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(withPareto)));
    const { unmount } = renderApp(`/tuning/${done.id}`);
    expect(await screen.findByRole("heading", { name: "Pareto set" })).toBeInTheDocument();
    // Knobs by the catalog's labels once GET /knobs answers; one it lacks keeps its name.
    const row = await waitFor(() => {
      const found = screen
        .getAllByRole("row")
        .find((candidate) => candidate.textContent?.includes("vllm.tensor_parallel_size 2, Max sequences 128"));
      expect(found).toBeDefined();
      return found!;
    });
    expect(within(row).getByText("18.4")).toBeInTheDocument();
    expect(within(row).getByText("210 ms")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "the full comparison" })).toHaveAttribute(
      "href",
      `/experiments/${done.experiment}`,
    );
    unmount();

    const noComparison = { ...done, outcome: { ...(done.outcome as object), pareto_error: "no bench binary" } };
    server.use(http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(noComparison)));
    renderApp(`/tuning/${done.id}`);
    expect(await screen.findByText(/No comparison of this session's runs \(no bench binary\)/)).toBeInTheDocument();
  });

  it("reuses an earlier session of the same target and preset", async () => {
    const user = userEvent.setup();
    const host = {
      ...target,
      spec: {
        placement: "host",
        launcher_url: "http://gpu-host.localtest.me:9123",
        endpoint: "http://gpu-host.localtest.me:8000/v1",
        model: "Qwen/Qwen2.5-0.5B-Instruct",
        metrics: { job: "servtuner-external", pod: "rtx" },
      },
    } as ManagedTarget;
    const earlier = { ...done, request: { ...done.request, preset: "rag-prefill@1" } };
    let created: TuningSessionCreate | undefined;
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([host])),
      http.get(`${API}/tuning-sessions`, () => HttpResponse.json([earlier])),
      http.post(`${API}/tuning-sessions`, async ({ request }) => {
        created = (await request.json()) as TuningSessionCreate;
        return HttpResponse.json(session, { status: 201 });
      }),
      http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(session)),
    );
    renderApp(`/tuning/new?target=${host.id}`);
    const presets = await screen.findByRole("combobox", { name: /^Workload preset/ });
    await waitFor(() => expect(presets).toBeEnabled());
    await user.click(presets);
    await user.click(await screen.findByRole("option", { name: "rag-prefill@1", hidden: true }));
    // The dropdown's listbox carries the field's label too: the input comes first.
    await user.click((await screen.findAllByLabelText(/^Reuse earlier sessions/))[0]!);
    await user.click(await screen.findByRole("option", { name: /tune-qwen-00000a/, hidden: true }));
    // On a host the api checks the vLLM version: the page says so, and asks for nothing more.
    expect(screen.getByText(/if the host still runs the vLLM version they measured/)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Start session" }));
    await waitFor(() => expect(created).toBeDefined());
    expect(created).toMatchObject({ reuse_from: [done.id] });
    expect(created).not.toHaveProperty("confirm_same_vllm");
  }, 15_000);

  it("says when the repeats found no significant winner", async () => {
    // The RTX 3070 confirmation of 2026-11-24: 1024 and 2048 batched tokens overlapped.
    const outcome = done.outcome as {
      recommendation: { evidence: Record<string, unknown> };
    };
    const tied: TuningSession = {
      ...done,
      outcome: {
        ...outcome,
        recommendation: {
          ...outcome.recommendation,
          evidence: { confidence: "low", is_significant: false },
        },
      },
    };
    server.use(http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(tied)));
    renderApp(`/tuning/${done.id}`);
    expect(await screen.findByText(/Repeated, no significant winner/)).toBeInTheDocument();
    expect(screen.queryByText(/^Confirmed/)).not.toBeInTheDocument();
  });

  it("asks to approve a step, then shows the recommended command line", async () => {
    let approved = false;
    server.use(
      http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(approved ? done : session)),
      http.post(`${API}/tuning-sessions/:id/approve`, () => {
        approved = true;
        return HttpResponse.json(done);
      }),
    );
    renderApp(`/tuning/${session.id}`);
    expect(await screen.findByText(/Waiting for approval: boot probe of c-0000aaaa/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Run this step" }));
    expect(await screen.findByText("vllm serve Qwen/Qwen3-32B --tensor-parallel-size=2")).toBeInTheDocument();
    expect(screen.getByText("Confirmed, high confidence")).toBeInTheDocument();
    expect(screen.getByText(/sized by the memory free after warmup, below the 6 GiB/)).toBeInTheDocument();
    expect(screen.getByText(/0.95 booted with the same KV cache as/)).toBeInTheDocument();
    expect(screen.getByText(/Did not boot: no-kv-blocks/)).toBeInTheDocument();
    expect(screen.getByText("3 runs")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "qwen" })).toHaveAttribute("href", `/managed-targets/${target.id}`);
    expect(screen.queryByRole("button", { name: "Cancel session" })).toBeNull();
  });

  it("deploys the recommended configuration once the session has ended", async () => {
    let deployed: unknown;
    server.use(
      http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(done)),
      http.post(`${API}/managed-targets/:id/deploy`, async ({ request, params }) => {
        expect(params.id).toBe(target.id);
        deployed = await request.json();
        return HttpResponse.json({ ...target, deployment: { ...(deployed as object), args: [], deployed_at: "" } });
      }),
      http.get(`${API}/managed-targets/:id`, () => HttpResponse.json(target)),
      http.get(`${API}/managed-targets/:id/readiness`, () =>
        HttpResponse.json({ state: "pending", reason: "starting" }),
      ),
      http.get(`${API}/managed-targets/:id/logs`, () => HttpResponse.json({ source: "pod qwen-1", lines: [] })),
    );
    const { router } = renderApp(`/tuning/${done.id}`);
    await userEvent.click(await screen.findByRole("button", { name: "Deploy this configuration" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/managed-targets/${target.id}`));
    expect(deployed).toEqual({ knobs: { "vllm.tensor_parallel_size": 2 } });
  });

  it("offers no deploy while the session still holds the target", async () => {
    const running: TuningSession = { ...done, status: "running", step: { phase: "propose" } };
    server.use(http.get(`${API}/tuning-sessions/:id`, () => HttpResponse.json(running)));
    renderApp(`/tuning/${done.id}`);
    expect(await screen.findByText("Recommended command line")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Deploy this configuration" })).toBeNull();
  });
});
