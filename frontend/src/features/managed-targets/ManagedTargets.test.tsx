import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  LauncherCheck,
  ManagedTarget,
  ManagedTargetCreate,
  ManagedTargetLog,
  ManagedTargetReadiness,
  VllmKnobs,
} from "../../api/types";
import { failedRun, host, managedTarget, succeededRun } from "../../test/fixtures";
import { BOOTING_POLL_MS } from "../../api/hooks";
import { renderApp } from "../../test/render";
import { initialState, withLauncherUrl } from "./form";
import { server, TEST_API_BASE_URL } from "../../test/server";

const API = TEST_API_BASE_URL;
const PAGE = `/managed-targets/${managedTarget.id}`;
const ROUTE = `${API}${PAGE}`;

const deployed: ManagedTarget = {
  ...managedTarget,
  deployment: {
    knobs: { "vllm.max_num_seqs": 64 },
    args: ["--model=Qwen/Qwen3-32B", "--max-num-seqs=64"],
    deployed_at: "2026-11-24T09:00:00Z",
  },
};

const infeasible: ManagedTargetReadiness = {
  state: "infeasible",
  reason: "vLLM could not boot with these flags",
  failure: {
    kind: "infeasible",
    rule: "no-kv-blocks",
    message: "no memory left for KV cache blocks",
    excerpt: ["ValueError: No available memory for the cache blocks."],
    exit_code: 1,
  },
};

/** Serves the target; deploy and scale-down update it the way the api does. */
function serveTarget(initial: ManagedTarget, readiness: ManagedTargetReadiness = infeasible) {
  const state = {
    target: initial,
    deployedKnobs: undefined as VllmKnobs | undefined,
    scaledDown: false,
    logTails: [] as (string | null)[],
  };
  server.use(
    http.get(ROUTE, () => HttpResponse.json(state.target)),
    http.get(`${ROUTE}/readiness`, () => HttpResponse.json(readiness)),
    http.get(`${ROUTE}/logs`, ({ request }) => {
      state.logTails.push(new URL(request.url).searchParams.get("tail_lines"));
      return HttpResponse.json<ManagedTargetLog>({ source: "pod qwen-1", lines: ["INFO loading", "ValueError: boom"] });
    }),
    http.post(`${ROUTE}/deploy`, async ({ request }) => {
      state.deployedKnobs = ((await request.json()) as { knobs: VllmKnobs }).knobs;
      state.target = { ...deployed, deployment: { ...deployed.deployment!, knobs: state.deployedKnobs } };
      return HttpResponse.json(state.target, { status: 202 });
    }),
    http.post(`${ROUTE}/scale-down`, () => {
      state.scaledDown = true;
      return new HttpResponse(null, { status: 204 });
    }),
  );
  return state;
}

describe("managed target page", () => {
  afterEach(() => vi.restoreAllMocks());

  it("shows a target that was never deployed and deploys it with knobs", async () => {
    const user = userEvent.setup();
    const state = serveTarget(managedTarget);
    renderApp(PAGE);
    expect(await screen.findByRole("heading", { name: "qwen" })).toBeInTheDocument();
    expect(screen.getByText("Not deployed yet.")).toBeInTheDocument();
    expect(screen.getByText("http://qwen.servtuner-tune-qwen.svc:8000/v1")).toBeInTheDocument();
    // The header carries the actions; what servtuner deploys is folded until asked for.
    expect(screen.getByRole("link", { name: "Benchmark" })).toBeInTheDocument();
    expect(screen.queryByText("vllm/vllm-openai:v0.34.0")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Details" }));
    expect(await screen.findByText("vllm/vllm-openai:v0.34.0")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Scale to zero" })).toBeDisabled();

    // A knob from the catalog, found by its flag; it starts unset (vLLM decides), so Deploy waits.
    const picker = screen.getByRole("combobox", { name: "Add a knob" });
    await waitFor(() => expect(picker).toBeEnabled()); // once GET /knobs answered
    await user.type(picker, "--max-num-seqs");
    await user.click(await screen.findByRole("option", { name: /Max sequences/, hidden: true }));
    expect(screen.getByRole("button", { name: "Deploy" })).toBeDisabled();
    const value = await screen.findByRole("textbox", { name: "Value" });
    expect(screen.getByText(/Maximum number of sequences/)).toBeInTheDocument(); // vLLM's own words
    await user.type(value, "0");
    expect(screen.getByText("At least 1")).toBeInTheDocument();
    await user.clear(value);
    await user.type(value, "128");
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.getByRole("listitem")).toHaveTextContent("Max sequences 128");
    await user.click(screen.getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(state.deployedKnobs).toEqual({ "vllm.max_num_seqs": 128 }));
    expect(await screen.findByText("Cannot boot with these flags")).toBeInTheDocument();
    expect(screen.getByLabelText("vLLM log excerpt")).toHaveTextContent("No available memory");
    expect(screen.getByRole("button", { name: "Redeploy" })).toBeInTheDocument();
  });

  it("deploys any flag in vLLM's catalog, not only servtuner's labelled knobs", async () => {
    const user = userEvent.setup();
    const state = serveTarget(managedTarget);
    renderApp(PAGE);
    const picker = await screen.findByRole("combobox", { name: "Add a knob" });
    await waitFor(() => expect(picker).toBeEnabled());
    await user.type(picker, "scheduled tokens");
    await user.click(await screen.findByRole("option", { name: /Max number of scheduled tokens/, hidden: true }));
    await user.type(await screen.findByRole("textbox", { name: "Value" }), "4096");
    await user.click(screen.getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(state.deployedKnobs).toEqual({ "vllm.max_num_scheduled_tokens": 4096 }));
  });

  it("starts from the last deployment's knobs, as chips, never as flags", async () => {
    const user = userEvent.setup();
    serveTarget(deployed);
    renderApp(PAGE);
    expect(await screen.findByText("Max sequences")).toBeInTheDocument(); // once GET /knobs answered
    const chip = screen.getByRole("listitem");
    expect(chip).toHaveTextContent("Max sequences 64");
    expect(chip).not.toHaveTextContent("max_num_seqs");
    expect(screen.getByLabelText("Container args")).toHaveTextContent("--max-num-seqs=64");
    await user.click(screen.getByRole("button", { name: "Remove Max sequences" }));
    expect(screen.queryByRole("listitem")).toBeNull();
    expect(screen.getByRole("button", { name: "Redeploy" })).toBeEnabled(); // none: vLLM's defaults
  });

  it("says when a knob overrides a base flag", async () => {
    serveTarget({ ...deployed, spec: { ...deployed.spec, base_flags: ["--max-num-seqs=32", "--dtype=half"] } });
    renderApp(PAGE);
    expect(await screen.findByLabelText("Overridden base flags")).toHaveTextContent(
      "Max sequences overrides the base flag --max-num-seqs=32",
    );
  });

  it("shows vLLM's log, with a longer window on request", async () => {
    const user = userEvent.setup();
    const state = serveTarget(deployed);
    renderApp(PAGE);
    expect(await screen.findByLabelText("vLLM log")).toHaveTextContent("ValueError: boom");
    expect(screen.getByText("From pod qwen-1")).toBeInTheDocument();
    expect(state.logTails).toEqual(["200"]);
    await user.click(screen.getByText("Last 1000 lines"));
    await waitFor(() => expect(state.logTails).toContain("1000"));
  });

  it("opens New run pointed at the target, around its last SLO-max", async () => {
    const user = userEvent.setup();
    serveTarget(deployed);
    server.use(
      http.get(`${API}/run-defaults`, () =>
        HttpResponse.json({
          prometheus: { url: "http://kps-prometheus.monitoring.svc:9090", tls: { enabled: false } },
        }),
      ),
      http.get(`${API}/runs`, () =>
        HttpResponse.json([failedRun, { ...succeededRun, slo_max_rps: 20 }, { ...succeededRun, slo_max_rps: 5 }]),
      ),
    );
    renderApp(PAGE);
    await user.click(await screen.findByRole("link", { name: "Benchmark" }));
    expect(await screen.findByRole("heading", { name: "New run" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("qwen-bench");
    await user.click(screen.getByRole("tab", { name: "YAML" }));
    const yaml = (screen.getByRole("textbox", { name: "RunSpec YAML" }) as HTMLTextAreaElement).value;
    expect(yaml).toContain("endpoint: http://qwen.servtuner-tune-qwen.svc:8000/v1");
    expect(yaml).toContain("vllm.max_num_seqs: 64");
    expect(yaml).toContain("rate: 14"); // 0.7 x the newest SLO-max (20)
    expect(yaml).toContain("url: http://kps-prometheus.monitoring.svc:9090"); // the install's, from /run-defaults
    expect(yaml).toContain("rate: 26");
  });

  it("lists the target's runs by its run label, to compare them", async () => {
    const user = userEvent.setup();
    serveTarget(deployed);
    const labels: (string | null)[] = [];
    server.use(
      http.get(`${API}/runs`, ({ request }) => {
        labels.push(new URL(request.url).searchParams.get("label"));
        return HttpResponse.json([succeededRun, failedRun]);
      }),
    );
    const { router } = renderApp(PAGE);
    await user.click(await screen.findByRole("checkbox", { name: `Select ${succeededRun.name}` }));
    await user.click(screen.getByRole("checkbox", { name: `Select ${failedRun.name}` }));
    expect(labels).toContain("servtuner.managed-target=01jmanaged0000000000000000a");
    await user.click(screen.getByRole("button", { name: "Compare 2 runs" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/compare"));
    expect(router.state.location.search).toBe(`?runs=${succeededRun.id},${failedRun.id}`);
  });

  it("opens New session with the target picked", async () => {
    const user = userEvent.setup();
    serveTarget(deployed);
    server.use(http.get(`${API}/managed-targets`, () => HttpResponse.json([deployed])));
    renderApp(PAGE);
    await user.click(await screen.findByRole("link", { name: "Tune" }));
    expect(await screen.findByRole("heading", { name: "New tuning session" })).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: /^Managed target/ })).toHaveValue("qwen (Qwen/Qwen3-32B)"),
    );
  });

  it("checks a host target's launcher with its token", async () => {
    const user = userEvent.setup();
    const host = {
      ...managedTarget,
      spec: {
        placement: "host",
        server: "vllm",
        launcher_url: "http://gpu-host.localtest.me:9123",
        endpoint: "http://gpu-host.localtest.me:8000/v1",
        model: "Qwen/Qwen2.5-0.5B-Instruct",
        metrics: { job: "servtuner-external", pod: "rtx" },
      },
    } as ManagedTarget;
    serveTarget(host);
    server.use(
      http.post(`${ROUTE}/check-launcher`, () =>
        HttpResponse.json<LauncherCheck>({
          status: "refused",
          message: "the launcher refused the token: it is not the one it was started with",
        }),
      ),
    );
    renderApp(PAGE);
    await user.click(await screen.findByRole("button", { name: "Check launcher" }));
    expect(await screen.findByText("Token refused")).toBeInTheDocument();
    expect(screen.getByText(/not the one it was started with/)).toBeInTheDocument();
  });

  it("says which host a host target runs on, with its hardware and price", async () => {
    const onHost = {
      ...managedTarget,
      spec: {
        placement: "host",
        server: "vllm",
        host_id: host.id,
        launcher_url: "http://gpu-host.localtest.me:9123",
        endpoint: "http://gpu-host.localtest.me:8000/v1",
        model: "Qwen/Qwen2.5-0.5B-Instruct",
        metrics: { job: "servtuner-external", pod: "rtx" },
      },
    } as ManagedTarget;
    serveTarget(onHost);
    server.use(http.get(`${API}/hosts`, () => HttpResponse.json([host])));
    renderApp(PAGE);
    const line = await screen.findByLabelText("Runs on");
    expect(line).toHaveTextContent(
      "Runs on gpu-box: 1 × NVIDIA GeForce RTX 3070 (8 GiB) · 16 CPU cores · 32 GiB · $0.0762/h",
    );
    expect(within(line).getByRole("link", { name: "gpu-box" })).toHaveAttribute("href", `/hosts/${host.id}`);
  });

  it("offers no launcher check on a Kubernetes target", async () => {
    serveTarget(managedTarget);
    renderApp(PAGE);
    await screen.findByRole("heading", { name: "qwen" });
    expect(screen.queryByRole("button", { name: "Check launcher" })).toBeNull();
  });

  it("reads the log once more when the boot ends, so its last lines show", async () => {
    let state: ManagedTargetReadiness["state"] = "pending";
    let lines = ["INFO loading weights"];
    serveTarget(deployed);
    server.use(
      http.get(`${ROUTE}/readiness`, () => HttpResponse.json({ state, reason: state })),
      http.get(`${ROUTE}/logs`, () => HttpResponse.json<ManagedTargetLog>({ source: "pod qwen-1", lines })),
    );
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderApp(PAGE);
    expect(await screen.findByLabelText("vLLM log")).toHaveTextContent("loading weights");
    // vLLM finishes booting between two polls: the next readiness poll says ready.
    state = "ready";
    lines = ["INFO loading weights", "INFO Application startup complete."];
    await act(() => vi.advanceTimersByTimeAsync(BOOTING_POLL_MS + 100));
    await waitFor(() => expect(screen.getByLabelText("vLLM log")).toHaveTextContent("Application startup complete"));
    vi.useRealTimers();
  });

  it("shows the api's refusal while a session tunes the target", async () => {
    const user = userEvent.setup();
    serveTarget(deployed);
    server.use(
      http.post(`${ROUTE}/deploy`, () =>
        HttpResponse.json(
          { detail: "tuning session 01JSESSION is using this target: cancel it first" },
          { status: 409 },
        ),
      ),
    );
    renderApp(PAGE);
    await user.click(await screen.findByRole("button", { name: "Redeploy" }));
    expect(await screen.findByText(/cancel it first/)).toBeInTheDocument();
  });

  it("scales to zero, and deletes after confirming", async () => {
    const user = userEvent.setup();
    const state = serveTarget(deployed);
    let deleted = false;
    server.use(
      http.delete(ROUTE, () => {
        deleted = true;
        return new HttpResponse(null, { status: 204 });
      }),
      http.get(`${API}/managed-targets`, () => HttpResponse.json([])),
      http.get(`${API}/tuning-sessions`, () => HttpResponse.json([])),
    );
    const { router } = renderApp(PAGE);
    await user.click(await screen.findByRole("button", { name: "Scale to zero" }));
    await waitFor(() => expect(state.scaledDown).toBe(true));

    await user.click(screen.getByRole("button", { name: "Delete managed target" }));
    // An in-page dialog: Cancel leaves the target, Delete deletes it.
    const dialog = await screen.findByRole("dialog", { name: "Delete qwen?" });
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(deleted).toBe(false);
    await user.click(screen.getByRole("button", { name: "Delete managed target" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/managed-targets"));
    expect(deleted).toBe(true);
  });

  it("edits the target, keeping what the form does not show", async () => {
    const user = userEvent.setup();
    serveTarget(managedTarget);
    let saved: ManagedTargetCreate | undefined;
    server.use(
      http.put(ROUTE, async ({ request }) => {
        saved = (await request.json()) as ManagedTargetCreate;
        return HttpResponse.json({ ...managedTarget, spec: { ...managedTarget.spec, ...saved.spec } });
      }),
    );
    renderApp(PAGE);
    await user.click(await screen.findByRole("button", { name: "Details" }));
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const memory = screen.getByLabelText("Memory");
    await user.clear(memory);
    await user.type(memory, "96Gi");
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(saved?.spec).toMatchObject({ memory: "96Gi", gpus: 2, image: "vllm/vllm-openai:v0.34.0" }),
    );
    expect(await screen.findByRole("button", { name: "Edit" })).toBeInTheDocument();
  });
});

describe("managed server list", () => {
  it("points a vLLM with no GPU at the CPU build", async () => {
    const user = userEvent.setup();
    server.use(http.get(`${API}/managed-targets`, () => HttpResponse.json([])));
    renderApp("/managed-targets");
    await user.click(await screen.findByRole("button", { name: "Add a managed server" }));
    expect(screen.queryByText(/No GPU: vLLM needs its CPU build/)).toBeNull();
    const gpus = screen.getByLabelText(/^GPUs/);
    await user.clear(gpus);
    await user.type(gpus, "0");
    expect(await screen.findByText(/No GPU: vLLM needs its CPU build/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Use it" }));
    expect(screen.getByLabelText(/^Image/)).toHaveValue("vllm/vllm-openai-cpu:v0.34.0-x86_64");
    expect(screen.queryByText(/No GPU: vLLM needs its CPU build/)).toBeNull();
  });

  it("starts a new target's vLLM image under an air-gapped install's registry", async () => {
    const user = userEvent.setup();
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([])),
      http.get(`${API}/run-defaults`, () =>
        HttpResponse.json({ offline: true, image_registry: "registry.example.internal:5000" }),
      ),
    );
    renderApp("/managed-targets");
    await user.click(await screen.findByRole("button", { name: "Add a managed server" }));
    expect(await screen.findByLabelText(/^Image/)).toHaveValue(
      "registry.example.internal:5000/vllm/vllm-openai:v0.34.0",
    );
    const gpus = screen.getByLabelText(/^GPUs/);
    await user.clear(gpus);
    await user.type(gpus, "0");
    await user.click(await screen.findByRole("button", { name: "Use it" }));
    expect(screen.getByLabelText(/^Image/)).toHaveValue(
      "registry.example.internal:5000/vllm/vllm-openai-cpu:v0.34.0-x86_64",
    );
  });

  it("lists targets linking to their pages, and saves a Kubernetes target", async () => {
    let created: ManagedTargetCreate | undefined;
    server.use(
      http.get(`${API}/managed-targets`, () =>
        HttpResponse.json([
          managedTarget,
          { ...deployed, id: "01JMANAGED0000000000000000B", name: "llama", namespace: "servtuner-tune-llama" },
        ]),
      ),
      http.post(`${API}/managed-targets`, async ({ request }) => {
        created = (await request.json()) as ManagedTargetCreate;
        return HttpResponse.json(managedTarget, { status: 201 });
      }),
    );
    renderApp("/managed-targets");
    expect(await screen.findByRole("link", { name: "qwen" })).toHaveAttribute("href", PAGE);
    expect(within(screen.getByRole("link", { name: "qwen" }).closest("tr")!).getByText("Not deployed")).toBeVisible();
    expect(within(screen.getByRole("link", { name: "llama" }).closest("tr")!).getByText("Deployed")).toBeVisible();
    expect(screen.getAllByRole("link", { name: "Managed Servers" })[0]).toHaveAttribute("href", "/managed-targets");
    // The page says when a saved target is the right thing instead.
    expect(screen.getAllByRole("link", { name: "Targets" }).map((link) => link.getAttribute("href"))).toContain(
      "/targets",
    );

    await userEvent.click(screen.getByRole("button", { name: "Add a managed server" }));
    await userEvent.clear(screen.getByLabelText(/^Name(?!space)/)); // replace the generated name
    await userEvent.type(screen.getByLabelText(/^Name(?!space)/), "llama");
    await userEvent.type(screen.getByLabelText(/^Namespace/), "servtuner-tune-llama");
    expect(screen.getByLabelText(/^Image/)).toHaveValue("vllm/vllm-openai:v0.34.0"); // the pinned version, prefilled
    await userEvent.type(screen.getByLabelText(/^Model(?! cache)/), "meta-llama/Llama-3.1-8B-Instruct");
    await userEvent.type(screen.getByLabelText(/^Base flags/), "--max-modle-len=8192");
    // A typo is warned about once GET /knobs answers, never refused (a newer vLLM may take it).
    expect(await screen.findByText(/vLLM v0.34.0 has no --max-modle-len/)).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText(/^Base flags/));
    await userEvent.type(screen.getByLabelText(/^Base flags/), "--max-model-len=8192");
    expect(screen.queryByText(/has no --max/)).toBeNull();
    await userEvent.click(screen.getByText("TLS from a cert-manager ClusterIssuer"));
    await userEvent.type(screen.getByLabelText(/^ClusterIssuer/), "servtuner-ca");
    await userEvent.click(screen.getByRole("button", { name: "Save managed target" }));
    await waitFor(() => expect(created).toBeDefined());
    expect(created).toMatchObject({
      name: "llama",
      namespace: "servtuner-tune-llama",
      spec: {
        image: "vllm/vllm-openai:v0.34.0",
        model: "meta-llama/Llama-3.1-8B-Instruct",
        base_flags: ["--max-model-len=8192"],
        transport: {
          enabled: true,
          issuer: { kind: "ClusterIssuer", name: "servtuner-ca" },
        },
      },
    });
  });

  it("offers SGLang for a host target, and labels its fields by the chosen server", async () => {
    const user = userEvent.setup();
    server.use(http.get(`${API}/managed-targets`, () => HttpResponse.json([])));
    renderApp("/managed-targets");
    await user.click(await screen.findByRole("button", { name: "Add a managed server" }));
    await user.click(screen.getByText("A host with servtuner launcher"));
    expect(screen.getByLabelText(/^vLLM endpoint/)).toBeInTheDocument();
    await user.click(screen.getByText("SGLang"));
    expect(screen.getByLabelText(/^SGLang endpoint/)).toBeInTheDocument();
    expect(screen.getByLabelText(/^SGLang API key$/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/^vLLM (endpoint|API key)/)).toBeNull();
  });

  it("saves a host target with pasted keys, sent write-only after the target", async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    let created: ManagedTargetCreate | undefined;
    const pasted: Record<string, unknown> = {};
    server.use(
      http.get(`${API}/managed-targets`, () => HttpResponse.json([])),
      http.post(`${API}/managed-targets`, async ({ request }) => {
        calls.push("create");
        created = (await request.json()) as ManagedTargetCreate;
        return HttpResponse.json(managedTarget, { status: 201 });
      }),
      http.put(`${API}/managed-targets/:id/credentials/:slot`, async ({ params, request }) => {
        expect(params.id).toBe(managedTarget.id);
        calls.push(`credential ${params.slot as string}`);
        pasted[params.slot as string] = await request.json();
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp("/managed-targets");
    await user.click(await screen.findByRole("button", { name: "Add a managed server" }));
    await user.click(screen.getByText("A host with servtuner launcher"));
    const fill = async (label: RegExp, value: string) => user.type(screen.getByLabelText(label), value);
    await user.clear(screen.getByLabelText(/^Name(?!space)/)); // replace the generated name
    await fill(/^Name(?!space)/, "rtx");
    expect(screen.queryByLabelText(/^Namespace/)).toBeNull(); // a host's namespace is a metrics label, under Advanced
    await fill(/^Model/, "Qwen/Qwen2.5-0.5B-Instruct");
    await fill(/^Launcher URL/, "http://gpu-host.localtest.me:9123");
    // The endpoint follows the launcher's host, on vLLM's port, until the user edits it.
    expect(screen.getByLabelText(/^vLLM endpoint/)).toHaveValue("http://gpu-host.localtest.me:8000/v1");
    await fill(/^Launcher token$/, "launcher-secret-value");
    await fill(/^vLLM API key$/, "vllm-secret-value");
    expect(screen.queryByLabelText(/^Image/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Save managed target" }));
    await waitFor(() => expect(calls).toEqual(["create", "credential launcher_token", "credential api_key"]));
    expect(created).toEqual({
      name: "rtx",
      namespace: "servtuner-external", // the default label for a host
      spec: {
        placement: "host",
        server: "vllm",
        launcher_url: "http://gpu-host.localtest.me:9123",
        endpoint: "http://gpu-host.localtest.me:8000/v1",
        metrics: { job: "servtuner-external", pod: "rtx" },
        model: "Qwen/Qwen2.5-0.5B-Instruct",
        base_flags: [],
      },
    });
    expect(JSON.stringify(created)).not.toMatch(/secret-value/);
    expect(pasted).toEqual({
      launcher_token: { value: "launcher-secret-value" },
      api_key: { value: "vllm-secret-value" },
    });
  });

  it("keeps a typed endpoint when the launcher URL changes (a port proxy)", () => {
    const fields = { ...initialState().fields, launcherUrl: "http://a:9123", endpoint: "http://a:8080/v1" };
    expect(withLauncherUrl(fields, "http://b:9123").endpoint).toBe("http://a:8080/v1");
    const suggested = withLauncherUrl({ ...fields, endpoint: "" }, "http://a:9123");
    expect(withLauncherUrl(suggested, "http://b:9123").endpoint).toBe("http://b:8000/v1");
  });
});
