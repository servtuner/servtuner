import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import {
  bundleFiles,
  capacity,
  pricedCapacity,
  bifrostRecommendations,
  degradedAnalysis,
  inflight,
  serverChart,
  environment,
  snapshot,
  goldenAnalysis,
  histograms,
  recommendations,
  timeseries,
} from "../../test/bundles";
import { succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

const RUN = `${TEST_API_BASE_URL}/runs/:runId`;

function serveAnalysed(analysis = degradedAnalysis) {
  server.use(
    http.get(`${RUN}/analysis`, () => HttpResponse.json(analysis)),
    http.get(`${RUN}/charts/client_timeseries`, () => HttpResponse.json(timeseries)),
    http.get(`${RUN}/charts/histograms`, () => HttpResponse.json(histograms)),
    http.get(`${RUN}/recommendations`, () => HttpResponse.json(recommendations)),
    http.get(`${RUN}/capacity`, () => HttpResponse.json(capacity)),
    http.get(`${RUN}/files`, () => HttpResponse.json(bundleFiles)),
    http.get(`${RUN}/environment`, () => HttpResponse.json(environment)),
    http.get(`${RUN}/charts/inflight`, () => HttpResponse.json(inflight)),
    http.get(`${RUN}/charts/server`, () => HttpResponse.json(serverChart)),
    http.get(`${RUN}/snapshot`, () => HttpResponse.json(snapshot)),
  );
}

function serveNotAnalysed() {
  const notYet = () => HttpResponse.json({ detail: "not analysed" }, { status: 404 });
  server.use(
    http.get(`${RUN}/analysis`, notYet),
    http.get(`${RUN}/charts/client_timeseries`, notYet),
    http.get(`${RUN}/charts/histograms`, notYet),
    http.get(`${RUN}/recommendations`, notYet),
    http.get(`${RUN}/capacity`, notYet),
  );
}

describe("run detail tabs", () => {
  it("opens the tab named in the URL and keeps the choice in it", async () => {
    serveAnalysed();
    const { router } = renderApp(`/runs/${succeededRun.id}?tab=capacity`);
    expect(await screen.findByRole("tab", { name: "Capacity", selected: true })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Latency" }));
    expect(router.state.location.search).toBe("?tab=latency");
    await userEvent.click(screen.getByRole("tab", { name: "Overview" }));
    expect(router.state.location.search).toBe("");
  });

  it("shows latency with intervals, n/a for too few samples, and validity per stage", async () => {
    serveAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=latency`);
    expect(
      await screen.findByRole("img", { name: /Time to first token p50, p95 and p99 over time/ }),
    ).toBeInTheDocument();
    const table = (await screen.findByRole("heading", { name: "Per stage, with 95% intervals" })).parentElement!;
    const rows = within(table).getAllByRole("row");
    expect(within(rows[1]!).getByText("Valid")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("400 ms")).toBeInTheDocument();
    expect(within(rows[1]!).getAllByText("n/a (n=600)").length).toBeGreaterThan(0);
    expect(within(rows[3]!).getByText("Excluded")).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: /p95 time to first token and end-to-end latency against achieved/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /End-to-end histogram for stage 1/ })).toBeInTheDocument();
  });

  it("reads a golden analysis from bench's own test data", async () => {
    serveAnalysed(goldenAnalysis);
    renderApp(`/runs/${succeededRun.id}?tab=throughput`);
    const table = (await screen.findByRole("heading", { name: "Per stage" })).parentElement!;
    expect(within(table).getAllByRole("row")).toHaveLength(goldenAnalysis.stages.length + 1);
  });

  it("explains a run that has not been analysed yet", async () => {
    serveNotAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=latency`);
    expect(await screen.findByText("No latency over time yet")).toBeInTheDocument();
    expect(await screen.findByText("No stage statistics yet")).toBeInTheDocument();
  });

  it("shows recommendation cards with their derivation and only the patches the bundle has", async () => {
    serveAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=recommendations`);
    expect(await screen.findByText("cpu.request")).toBeInTheDocument();
    expect(screen.getByText("0.35 cores")).toBeInTheDocument();
    expect(screen.getByText("roundup(0.27 × (1 + 0.2), 0.05) = 0.35")).toBeInTheDocument();
    expect(screen.getByText(/Sized for 49.99 RPS per replica at stage 1/)).toBeInTheDocument();
    const patch = await screen.findByRole("link", { name: "Download resources patch" });
    expect(patch).toHaveAttribute("href", `${TEST_API_BASE_URL}/runs/${succeededRun.id}/files/patches/resources.yaml`);
    expect(screen.queryByRole("link", { name: "Download bifrost config patch" })).toBeNull();
  });

  it("shows a memory recommendation and its range in binary units", async () => {
    const document = structuredClone(recommendations);
    Object.assign(document.recommendations[0]!, {
      unit: "bytes",
      current: null,
      recommended: 512 * 1024 ** 2,
      min: 256 * 1024 ** 2,
      max: 1.5 * 1024 ** 3,
    });
    serveAnalysed();
    server.use(http.get(`${RUN}/recommendations`, () => HttpResponse.json(document)));
    renderApp(`/runs/${succeededRun.id}?tab=recommendations`);
    expect(await screen.findByText("512 MiB")).toBeInTheDocument();
    expect(screen.getByText("range 256 MiB to 1.5 GiB")).toBeInTheDocument();
  });

  it("says an operating-point caveat once, above the cards", async () => {
    const caveat = "No stage failed the SLOs, so 49.99 RPS is the highest load tested.";
    const document = structuredClone(recommendations);
    document.operating_point.caveats = [caveat];
    document.recommendations[0]!.caveats = [...document.recommendations[0]!.caveats, caveat];
    serveAnalysed();
    server.use(http.get(`${RUN}/recommendations`, () => HttpResponse.json(document)));
    renderApp(`/runs/${succeededRun.id}?tab=recommendations`);
    expect(await screen.findByText("About the load these sizes are for")).toBeInTheDocument();
    expect(screen.getAllByText(caveat)).toHaveLength(1);
    expect(screen.getByText("The CPU peak is a 1-minute average.")).toBeInTheDocument();
  });

  it("prices the capacity rows and shows how a replica was priced", async () => {
    serveAnalysed();
    server.use(http.get(`${RUN}/capacity`, () => HttpResponse.json(pricedCapacity)));
    renderApp(`/runs/${succeededRun.id}?tab=capacity`);
    expect(await screen.findByRole("columnheader", { name: "Cost per hour" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "$4.20" })).toBeInTheDocument();
    expect(screen.getByText(/\$1\.40 per replica-hour from aws prices/)).toBeInTheDocument();
    expect(screen.getByText(/g5\.12xlarge at 5\.6 USD\/h ÷ 4 GPUs/)).toBeInTheDocument();
    expect(screen.getByText(/Tokens cost \$0\.5 per 1k requests and \$4\.00 per 1M output tokens/)).toBeInTheDocument();
    expect(screen.getByText(/Retries and fallbacks added \$0\.1 per 1k requests/)).toBeInTheDocument();
    expect(screen.getByText(/differ by 20\.0%/)).toBeInTheDocument();
  });

  it("says on the overview what the run cost, or why it is unpriced", async () => {
    serveAnalysed();
    server.use(http.get(`${RUN}/capacity`, () => HttpResponse.json(pricedCapacity)));
    const { unmount } = renderApp(`/runs/${succeededRun.id}`);
    expect(await screen.findByText("$1.40 per replica-hour (aws prices)")).toBeInTheDocument();
    expect(screen.getByText(/Tokens: \$0\.5 per 1k requests/)).toBeInTheDocument();
    unmount();
    server.use(
      http.get(`${RUN}/capacity`, () =>
        HttpResponse.json({ ...pricedCapacity, infra_cost: undefined, token_cost: undefined }),
      ),
    );
    renderApp(`/runs/${succeededRun.id}`);
    expect(await screen.findByText(/Not priced: the RunSpec names no prices/)).toBeInTheDocument();
  });

  it("highlights the capacity row that covers the traffic entered", async () => {
    serveAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=capacity`);
    await userEvent.type(await screen.findByRole("textbox", { name: /Your traffic/ }), "90");
    const covering = screen.getByRole("row", { current: true });
    expect(within(covering).getByText(/100 RPS/)).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: /Your traffic/ }), "0");
    expect(screen.getByText(/900 RPS is beyond the table/)).toBeInTheDocument();
  });

  it("shows declared against actual knobs, the transport per hop and the target", async () => {
    serveAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=config`);
    const mismatch = await screen.findByRole("row", { name: /vllm.max_num_seqs/ });
    expect(within(mismatch).getByText("Mismatch")).toBeInTheDocument();
    expect(within(mismatch).getByText("128")).toBeInTheDocument();
    expect(screen.getByRole("row", { name: /vllm.enable_prefix_caching/ })).toHaveTextContent("not exposed");
    expect(screen.getByRole("heading", { name: "Transport per hop" })).toBeInTheDocument();
    expect(await screen.findByText("The target, as captured before the load")).toBeInTheDocument();
    expect(screen.queryByText(/HF_TOKEN=/)).toBeNull();
  });

  it("shows how each hop was secured", async () => {
    serveAnalysed();
    server.use(
      http.get(`${RUN}/environment`, () =>
        HttpResponse.json({
          ...environment,
          transport: {
            target: { tls: true, client_cert: false },
            prometheus: { tls: false, client_cert: false },
            api: { tls: true, client_cert: true },
          },
        }),
      ),
    );
    renderApp(`/runs/${succeededRun.id}?tab=config`);
    const target = await screen.findByRole("row", { name: /^Target/ });
    expect(within(target).getByText("TLS")).toBeInTheDocument();
    expect(within(screen.getByRole("row", { name: /^Prometheus/ })).getByText("No TLS")).toBeInTheDocument();
    expect(within(screen.getByRole("row", { name: /^API/ })).getByText("TLS + client certificate")).toBeInTheDocument();
  });

  it("puts the model tested first, checked against what the server listed", async () => {
    serveAnalysed();
    const spec = (model: string) =>
      [
        "target:",
        "  type: vllm",
        "  endpoint: https://vllm.llm.svc:8000/v1",
        `  model: ${model}`,
        "workload:",
        "  api: chat",
        "  streaming: true",
        "  dataset: {type: synthetic, input_tokens: {mean: 256, stddev: 32}, output_tokens: {mean: 128}}",
      ].join("\n");
    server.use(http.get(`${RUN}/files/spec.yaml`, () => HttpResponse.text(spec("example/model-8b"))));
    renderApp(`/runs/${succeededRun.id}?tab=config`);
    const card = await screen.findByLabelText("Model tested");
    expect(within(card).getByLabelText("Model example/model-8b")).toBeInTheDocument();
    expect(within(card).getByText("Listed by the server")).toBeInTheDocument();
    expect(within(card).getByText(/vllm 0\.0\.0 at/)).toBeInTheDocument();
    expect(within(card).getByText("chat, streaming, input 256 ± 32 and output 128 tokens")).toBeInTheDocument();
    // The model also leads the run header, on every tab.
    expect(screen.getAllByLabelText("Model example/model-8b").length).toBeGreaterThan(1);

    server.use(http.get(`${RUN}/files/spec.yaml`, () => HttpResponse.text(spec("other/model"))));
    renderApp(`/runs/${succeededRun.id}?tab=config`);
    expect(await screen.findByText("Not in the server's model list")).toBeInTheDocument();
  });

  it("says so when there is no snapshot", async () => {
    serveAnalysed();
    server.use(http.get(`${RUN}/snapshot`, () => HttpResponse.json({ detail: "none" }, { status: 404 })));
    renderApp(`/runs/${succeededRun.id}?tab=config`);
    expect(await screen.findByText(/No target snapshot/)).toBeInTheDocument();
  });

  it("shows L(t), Little's law per stage and how C and B were sized", async () => {
    serveAnalysed();
    server.use(http.get(`${RUN}/recommendations`, () => HttpResponse.json(bifrostRecommendations)));
    renderApp(`/runs/${succeededRun.id}?tab=queueing`);
    expect(await screen.findByRole("img", { name: /requests in flight over time/ })).toBeInTheDocument();
    const stage0 = screen.getByRole("row", { name: /^0 10\/s/ });
    expect(within(stage0).getByText("2%")).toBeInTheDocument();
    expect(await screen.findByText("Recommended concurrency C")).toBeInTheDocument();
    expect(screen.getByText(/^max\(1, clamp\(/)).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "How concurrency (C) and queue size (B) were sized" }),
    ).toBeInTheDocument();
    const key = screen.getByLabelText("What C and B mean");
    expect(within(key).getByText("C · concurrency")).toBeInTheDocument();
    expect(within(key).getByText("B · queue size")).toBeInTheDocument();
  });

  it("explains C and B on the recommendations that use them", async () => {
    serveAnalysed();
    server.use(http.get(`${RUN}/recommendations`, () => HttpResponse.json(bifrostRecommendations)));
    renderApp(`/runs/${succeededRun.id}?tab=recommendations`);
    expect(await screen.findByLabelText("What C and B mean")).toBeInTheDocument();
    const concurrencyCard = screen.getByText("bifrost.concurrency").closest(".mantine-Paper-root") as HTMLElement;
    expect(within(concurrencyCard).getByText("C · concurrency")).toBeInTheDocument();
    const queueCard = screen.getByText("bifrost.buffer_size").closest(".mantine-Paper-root") as HTMLElement;
    expect(within(queueCard).getByText("B · queue size")).toBeInTheDocument();
  });

  it("shows no C and B key when no recommendation uses them", async () => {
    serveAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=recommendations`);
    expect(await screen.findByText("cpu.request")).toBeInTheDocument();
    expect(screen.queryByLabelText("What C and B mean")).toBeNull();
  });

  it("draws server series with their definitions, and resources against requests and limits", async () => {
    serveAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=server`);
    expect(await screen.findByRole("img", { name: "KV cache usage" })).toBeInTheDocument();
    expect(screen.getByText(/kv_cache_usage_perc of the fullest replica/)).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /CPU, llm\/vllm/ })).toBeNull();
    renderApp(`/runs/${succeededRun.id}?tab=resources`);
    expect(await screen.findByRole("img", { name: "CPU, llm/vllm" })).toBeInTheDocument();
  });

  it("says why a group has no series", async () => {
    serveAnalysed();
    server.use(
      http.get(`${RUN}/charts/server`, () =>
        HttpResponse.json({ ...serverChart, panels: serverChart.panels.filter((panel) => panel.group === "server") }),
      ),
    );
    renderApp(`/runs/${succeededRun.id}?tab=resources`);
    expect(await screen.findByText(/No CPU or memory series/)).toBeInTheDocument();
  });

  it("lists the bundle's files as downloads", async () => {
    serveAnalysed();
    renderApp(`/runs/${succeededRun.id}?tab=files`);
    const link = await screen.findByRole("link", { name: "requests.parquet" });
    expect(link).toHaveAttribute("href", `${TEST_API_BASE_URL}/runs/${succeededRun.id}/files/requests.parquet`);
    expect(screen.getByText("3 MiB")).toBeInTheDocument();
  });
});
