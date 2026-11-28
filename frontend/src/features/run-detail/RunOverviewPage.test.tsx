import { screen, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { managedTarget, runningRun, succeededRun, succeededRunDetail } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

describe("run overview", () => {
  it("renders metadata, the ordered status timeline and the summary", async () => {
    renderApp(`/runs/${succeededRun.id}`);
    expect(await screen.findByRole("heading", { name: succeededRun.name })).toBeInTheDocument();
    expect(screen.getByText("max_num_seqs=128")).toBeInTheDocument();
    expect(screen.getByText("team=serving")).toBeInTheDocument();

    const titles = (await screen.findAllByText(/^(resolve|load|finalize): /)).map((node) => node.textContent);
    expect(titles).toEqual(["resolve: running", "load: running (stage-1)", "finalize: succeeded"]);
    expect(screen.getByText("bundle written")).toBeInTheDocument();

    expect(screen.getByText("Max sustainable RPS: 10 req/s")).toBeInTheDocument();
    const invalidStage = screen.getByRole("row", { name: /invalid, client-bound/ });
    expect(within(invalidStage).getAllByText("n/a")).toHaveLength(2);
  });

  it("links the run to its managed server and tuning session, from its labels", async () => {
    const labelled = {
      ...succeededRunDetail,
      labels: {
        "servtuner.managed-target": managedTarget.id.toLowerCase(),
        "servtuner.dev/tuning-session": "01jsession0000000000000000a",
      },
    };
    server.use(http.get(`${TEST_API_BASE_URL}/runs/:runId`, () => HttpResponse.json(labelled)));
    renderApp(`/runs/${succeededRun.id}`);
    expect(await screen.findByRole("link", { name: managedTarget.name })).toHaveAttribute(
      "href",
      `/managed-targets/${managedTarget.id}`,
    );
    expect(screen.getByRole("link", { name: "01JSESSION0000000000000000A" })).toHaveAttribute(
      "href",
      "/tuning/01JSESSION0000000000000000A",
    );
    expect(screen.getByRole("link", { name: succeededRun.experiment! })).toHaveAttribute(
      "href",
      `/experiments/${succeededRun.experiment}`,
    );
  });

  it("omits the summary when the run has none", async () => {
    renderApp(`/runs/${runningRun.id}`);
    expect(await screen.findByRole("heading", { name: runningRun.name })).toBeInTheDocument();
    expect(await screen.findByText("No events reported yet.")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Summary" })).toBeNull();
  });

  it("reports an unknown run", async () => {
    renderApp("/runs/does-not-exist");
    expect(await screen.findByRole("alert")).toHaveTextContent("No run has the ID does-not-exist.");
  });

  it("shows an error when the API fails", async () => {
    server.use(http.get(`${TEST_API_BASE_URL}/runs/:runId`, () => new HttpResponse(null, { status: 500 })));
    renderApp(`/runs/${succeededRun.id}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("failed with HTTP 500");
  });
});
