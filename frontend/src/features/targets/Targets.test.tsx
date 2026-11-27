import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import type { CredentialInput, SavedTarget, TargetCheckView, TargetCreate } from "../../api/types";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const API = TEST_API_BASE_URL;
const ID = "01JTARGET00000000000000000A";
const KEY = "sk-live-0123456789abcdefghijKLMN";

const saved: SavedTarget = {
  id: ID,
  name: "vllm-lan",
  description: "RTX 3070",
  type: "vllm",
  target: {
    type: "vllm",
    endpoint: "http://vllm.example:8080/v1",
    model: "tiny",
    tls: { mode: "platform" },
    metrics: { namespace: "servtuner-external", pod: "vllm-lan", node: "vllm-lan-gpu", job: "servtuner-external" },
    upstreams: [],
  } as SavedTarget["target"],
  run_target: {
    type: "vllm",
    endpoint: "http://vllm.example:8080/v1",
    model: "tiny",
    tls: { mode: "platform" },
    metrics: { namespace: "servtuner-external", pod: "vllm-lan", node: "vllm-lan-gpu", job: "servtuner-external" },
    upstreams: [],
  } as SavedTarget["target"],
  slots: ["api_key"],
  credentials: { api_key: { is_set: false } },
  created_at: "2026-11-22T20:00:00Z",
  updated_at: "2026-11-22T20:00:00Z",
};

const withKey: SavedTarget = {
  ...saved,
  credentials: {
    api_key: {
      is_set: true,
      source: "managed",
      form: "token",
      preview: "sk-l************************KLMN",
      set_at: "2026-11-22T20:30:00Z",
    },
  },
};

const checkView = (status: "ok" | "failed", authMessage: string): TargetCheckView => ({
  result: {
    status,
    checked_at: "2026-11-22T21:00:00Z",
    target_type: "vllm",
    endpoint: "http://vllm.example:8080/v1",
    checks: [
      { name: "connect", status: "ok", message: "Connected to vllm.example:8080 in 3 ms.", duration_seconds: 0.003 },
      { name: "auth", status: status === "ok" ? "ok" : "failed", message: authMessage, duration_seconds: 0.01 },
      {
        name: "model",
        status: status === "ok" ? "ok" : "skipped",
        message: "Model tiny is served.",
        duration_seconds: 0,
      },
    ],
  },
  checked_from: "the api pod servtuner-api-0",
});

describe("targets", () => {
  it("lists saved targets with their key status and creates one without a key", async () => {
    let created: TargetCreate | undefined;
    server.use(
      http.get(`${API}/targets`, () => HttpResponse.json([withKey])),
      http.post(`${API}/targets`, async ({ request }) => {
        created = (await request.json()) as TargetCreate;
        return HttpResponse.json({ ...saved, id: "01JTARGET00000000000000000B", name: created.name }, { status: 201 });
      }),
      http.get(`${API}/targets/01JTARGET00000000000000000B`, () => HttpResponse.json(saved)),
    );
    renderApp("/targets");
    const row = (await screen.findByRole("link", { name: "vllm-lan" })).closest("tr")!;
    expect(within(row).getByText("API key set")).toBeInTheDocument();
    expect(within(row).getByText("not checked")).toBeInTheDocument();
    // A vLLM servtuner should deploy belongs under Managed vLLM; the page says so.
    expect(screen.getAllByRole("link", { name: "Managed vLLM" })).toHaveLength(2); // nav and the hint

    await userEvent.click(screen.getByRole("button", { name: "New target" }));
    await userEvent.type(screen.getByLabelText(/^Name/), "litellm-prod");
    await userEvent.type(screen.getByLabelText(/^Endpoint/), "http://litellm.example:4000/v1");
    await userEvent.type(screen.getByLabelText(/^Model/), "gpt-4o-mini");
    await userEvent.click(screen.getByRole("button", { name: "Save target" }));

    expect(await screen.findByRole("heading", { name: "vllm-lan" })).toBeInTheDocument();
    expect(created).toEqual({
      name: "litellm-prod",
      target: {
        type: "vllm",
        endpoint: "http://litellm.example:4000/v1",
        model: "gpt-4o-mini",
        tls: { mode: "platform" },
      },
      pricing: null,
    });
    expect(JSON.stringify(created)).not.toContain("auth");
  });

  it("saves a tls target with its CA Secret and no client certificate", async () => {
    let created: TargetCreate | undefined;
    server.use(
      http.get(`${API}/targets`, () => HttpResponse.json([])),
      http.post(`${API}/targets`, async ({ request }) => {
        created = (await request.json()) as TargetCreate;
        return HttpResponse.json({ ...saved, id: "01JTARGET00000000000000000C", name: created.name }, { status: 201 });
      }),
      http.get(`${API}/targets/01JTARGET00000000000000000C`, () => HttpResponse.json(saved)),
    );
    renderApp("/targets");
    await userEvent.click(await screen.findByRole("button", { name: "New target" }));
    await userEvent.type(screen.getByLabelText(/^Name/), "vllm-tls");
    await userEvent.type(screen.getByLabelText(/^Endpoint/), "https://vllm.example:8000/v1");
    await userEvent.type(screen.getByLabelText(/^Model/), "tiny");
    await userEvent.click(screen.getByText("TLS", { exact: true }));
    expect(screen.queryByLabelText(/Client certificate Secret/)).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/^CA Secret/), "vllm-ca");
    await userEvent.click(screen.getByRole("button", { name: "Save target" }));

    await screen.findByRole("heading", { name: "vllm-lan" });
    expect(created?.target.tls).toEqual({ mode: "tls", caSecretRef: { name: "vllm-ca" } });
  });

  it("sets a key write-only, clears the field, and shows only the masked preview", async () => {
    const puts: CredentialInput[] = [];
    let current = saved;
    server.use(
      http.get(`${API}/targets/${ID}`, () => HttpResponse.json(current)),
      http.put(`${API}/targets/${ID}/credentials/api_key`, async ({ request }) => {
        puts.push((await request.json()) as CredentialInput);
        current = withKey;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp(`/targets/${ID}`);
    const field = await screen.findByLabelText(/^New api key/);
    expect(field).toHaveValue("");
    await userEvent.type(field, KEY);
    await userEvent.click(screen.getByRole("button", { name: "Set key" }));

    expect(await screen.findByLabelText("Masked key")).toHaveTextContent("sk-l************************KLMN");
    expect(puts).toEqual([{ value: KEY }]);
    expect(screen.getByLabelText(/^New api key/)).toHaveValue("");
    expect(document.body.textContent).not.toContain(KEY);
    expect(screen.getByRole("button", { name: "Replace key" })).toBeInTheDocument();
  });

  it("tests a typed key once before setting it", async () => {
    const checks: unknown[] = [];
    server.use(
      http.get(`${API}/targets/${ID}`, () => HttpResponse.json(withKey)),
      http.post(`${API}/targets/${ID}/check`, async ({ request }) => {
        checks.push(await request.json());
        return HttpResponse.json(checkView("failed", "401 from GET /v1/models: the API key was rejected."));
      }),
    );
    renderApp(`/targets/${ID}`);
    await userEvent.type(await screen.findByLabelText(/^New api key/), "sk-typo");
    await userEvent.click(screen.getByRole("button", { name: "Test key" }));

    const result = await screen.findByLabelText("Check result");
    expect(within(result).getByText("servtuner cannot use this target yet")).toBeInTheDocument();
    expect(within(result).getByText(/the API key was rejected/)).toBeInTheDocument();
    expect(within(result).getByText(/from the api pod servtuner-api-0/)).toBeInTheDocument();
    expect(checks).toEqual([{ api_key: "sk-typo", generate: false }]);
  });

  it("checks the connection with the stored key", async () => {
    let body: unknown;
    server.use(
      http.get(`${API}/targets/${ID}`, () => HttpResponse.json(withKey)),
      http.post(`${API}/targets/${ID}/check`, async ({ request }) => {
        body = await request.json();
        return HttpResponse.json(checkView("ok", "GET /v1/models with the API key returned 200."));
      }),
    );
    renderApp(`/targets/${ID}`);
    await userEvent.click(await screen.findByRole("button", { name: "Check connection" }));
    const result = await screen.findByLabelText("Check result");
    expect(within(result).getByText("servtuner can reach and use this target")).toBeInTheDocument();
    expect(within(result).getAllByLabelText("passed")).toHaveLength(3);
    expect(body).toEqual({ generate: false });
  });
});

describe("where Prometheus finds a target's metrics", () => {
  it("explains an off-cluster target and previews the selector servtuner will use", async () => {
    let updated: TargetCreate | undefined;
    server.use(
      http.get(`${API}/targets/${ID}`, () => HttpResponse.json(saved)),
      http.put(`${API}/targets/${ID}`, async ({ request }) => {
        updated = (await request.json()) as TargetCreate;
        return HttpResponse.json(saved);
      }),
    );
    renderApp(`/targets/${ID}`);
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));

    expect(screen.getByText(/servtuner does not set up scraping/)).toBeInTheDocument();
    const preview = screen.getByLabelText("Prometheus selector");
    expect(preview).toHaveTextContent('{job="servtuner-external", namespace="servtuner-external", pod="vllm-lan"}');
    expect(preview).toHaveTextContent('DCGM_…{hostname="vllm-lan-gpu"}');

    await userEvent.clear(screen.getByLabelText(/^pod label/));
    await userEvent.type(screen.getByLabelText(/^pod label/), "gpu-box");
    expect(preview).toHaveTextContent('pod="gpu-box"');

    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await screen.findByRole("button", { name: "Edit" });
    expect(updated?.target.metrics).toEqual({ namespace: "servtuner-external", pod: "gpu-box", node: "vllm-lan-gpu" });
  });
});

describe("new run with a saved target", () => {
  it("fills the target block and label, and checks the document's target", async () => {
    let checked: { target?: { endpoint?: string; auth?: unknown } } | undefined;
    server.use(
      http.get(`${API}/targets`, () =>
        HttpResponse.json([
          {
            ...withKey,
            target: { ...withKey.target, auth: { secretRef: { name: "servtuner-target-x-api-key", key: "token" } } },
            run_target: {
              ...withKey.target,
              auth: { secretRef: { name: "servtuner-target-x-api-key", key: "token" } },
            },
          },
        ]),
      ),
      http.post(`${API}/targets/check`, async ({ request }) => {
        checked = (await request.json()) as typeof checked;
        return HttpResponse.json(checkView("ok", "200"));
      }),
    );
    renderApp("/runs/new");
    await userEvent.click(await screen.findByRole("combobox", { name: "Saved target" }));
    await userEvent.click(await screen.findByRole("option", { name: "vllm-lan (vllm)", hidden: true }));

    expect(screen.getByLabelText("Endpoint")).toHaveValue("http://vllm.example:8080/v1");
    expect(screen.getByText("API key set")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "YAML" }));
    const yaml = (screen.getByLabelText("RunSpec YAML") as HTMLTextAreaElement).value;
    expect(yaml).toContain(`servtuner.target: ${ID}`);
    expect(yaml).toContain("name: servtuner-target-x-api-key");

    await userEvent.click(screen.getByRole("tab", { name: "Form" }));
    await userEvent.click(screen.getByRole("button", { name: "Check connection" }));
    expect(await screen.findByLabelText("Check result")).toBeInTheDocument();
    expect(checked?.target?.endpoint).toBe("http://vllm.example:8080/v1");
    expect(checked?.target?.auth).toEqual({ secretRef: { name: "servtuner-target-x-api-key", key: "token" } });
  });

  it("brings the target's prices: its hardware in the target, its blocks in analysis", async () => {
    const hardware = { host: "gpu-box", accelerator: "NVIDIA GeForce RTX 3070", gpu_count: 1 };
    const infra = { source: "static", static: { gpus: { "NVIDIA GeForce RTX 3070": { usd_per_hour: 0.12 } } } };
    const token = { source: "manual", manual: { usd_per_1m_input_tokens: 0.2, usd_per_1m_output_tokens: 0.8 } };
    server.use(
      http.get(`${API}/targets`, () =>
        HttpResponse.json([
          {
            ...saved,
            run_target: { ...saved.target, hardware },
            run_analysis: { infra_pricing: infra, token_pricing: token },
          },
        ]),
      ),
    );
    renderApp("/runs/new");
    await userEvent.click(await screen.findByRole("combobox", { name: "Saved target" }));
    await userEvent.click(await screen.findByRole("option", { name: "vllm-lan (vllm)", hidden: true }));
    await userEvent.click(screen.getByRole("tab", { name: "YAML" }));
    const spec = parse((screen.getByLabelText("RunSpec YAML") as HTMLTextAreaElement).value);
    expect(spec.target.hardware).toEqual(hardware);
    expect(spec.analysis).toEqual({ infra_pricing: infra, token_pricing: token });
  });
});

describe("a saved target's prices", () => {
  it("are saved with the target and summarised on its page", async () => {
    let created: TargetCreate | undefined;
    server.use(
      http.get(`${API}/targets`, () => HttpResponse.json([])),
      http.post(`${API}/targets`, async ({ request }) => {
        created = (await request.json()) as TargetCreate;
        return HttpResponse.json({ ...saved, pricing: created.pricing }, { status: 201 });
      }),
      http.get(`${API}/targets/${ID}`, () =>
        HttpResponse.json({
          ...saved,
          pricing: created?.pricing,
          pricing_problems: ["the price book has no node type g6.xlarge"],
        }),
      ),
      http.get(`${API}/price-book`, () =>
        HttpResponse.json({
          source: "aws",
          aws_region: "us-east-1",
          discount: 1,
          nodes: { "g6.xlarge": { usd_per_hour: 0.8, cores: 4, memory_gib: 16, gpus: 1 } },
          gpus: {},
        }),
      ),
    );
    renderApp("/targets");
    await userEvent.click(await screen.findByRole("button", { name: "New target" }));
    await userEvent.type(screen.getByLabelText(/^Name/), "private-api");
    await userEvent.type(screen.getByLabelText(/^Endpoint/), "http://api.example/v1");
    await userEvent.type(screen.getByLabelText(/^Model/), "acme");
    await userEvent.click(screen.getByText("A node type"));
    const save = screen.getByRole("button", { name: "Save target" });
    expect(save).toBeDisabled(); // no node type picked yet
    await userEvent.click(screen.getByRole("combobox", { name: /^Node type/ }));
    await userEvent.click(await screen.findByRole("option", { name: "g6.xlarge", hidden: true }));
    await userEvent.click(screen.getByText("Given prices"));
    await userEvent.type(screen.getByLabelText("USD per 1M input tokens"), "0.2");
    await userEvent.type(screen.getByLabelText("USD per 1M output tokens"), "0.8");
    await userEvent.click(save);

    expect(await screen.findByText(/^Priced by the hour as 1 GPU of node type g6.xlarge/)).toBeInTheDocument();
    expect(screen.getByText("the price book has no node type g6.xlarge")).toBeInTheDocument();
    expect(created?.pricing).toEqual({
      hourly: { kind: "node_type", node_type: "g6.xlarge", gpu_count: 1, gpu_model: null },
      token: { source: "manual", usd_per_1m_input_tokens: 0.2, usd_per_1m_output_tokens: 0.8 },
    });
  }, 15_000); // types a whole form
});
