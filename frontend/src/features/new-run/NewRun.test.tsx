import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { succeededRun } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL } from "../../test/server";

async function yamlText(): Promise<string> {
  await userEvent.click(screen.getByRole("tab", { name: "YAML" }));
  return (screen.getByRole("textbox", { name: "RunSpec YAML" }) as HTMLTextAreaElement).value;
}

describe("new run", () => {
  it("edits one document from the form, keeping the YAML's comments", async () => {
    renderApp("/runs/new");
    const name = await screen.findByRole("textbox", { name: "Name" });
    await userEvent.clear(name);
    await userEvent.type(name, "ladder-a");
    await userEvent.click(screen.getByRole("button", { name: "Add a stage" }));
    const text = await yamlText();
    expect(text).toContain("name: ladder-a");
    expect(text).toContain("# experiment: my-experiment");
    expect(text).toMatch(/- rate: 40\n\s+duration: 60s/);
  });

  it("picks a workload preset, fills its SLOs and a starting ladder, and customizes it", async () => {
    renderApp("/runs/new");
    const workload = await screen.findByRole("combobox", { name: /^Workload/ });
    await waitFor(() => expect(workload).toBeEnabled());
    await userEvent.click(workload);
    await userEvent.click(await screen.findByRole("option", { name: "rag-prefill@1", hidden: true }));
    expect(screen.getByText(/512-token prefix shared by 4 groups/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: /^GPU class/ }));
    await userEvent.click(await screen.findByRole("option", { name: "a100-80gb", hidden: true }));
    await userEvent.click(screen.getByRole("button", { name: "Use starting ladder" }));
    let text = await yamlText();
    expect(text).toContain("preset: rag-prefill@1");
    expect(text).not.toContain("dataset:");
    expect(text).toContain("ttft_p95: 2s");
    expect(text).toMatch(/- rate: 32\n\s+duration: 90s/);

    await userEvent.click(screen.getByRole("tab", { name: "Form" }));
    await userEvent.click(screen.getByRole("button", { name: "Customize" }));
    text = await yamlText();
    expect(text).not.toContain("preset:");
    expect(text).toContain("shared_prefix:");
  });

  it("validates with bench and creates the run as JSON", async () => {
    let validated = "";
    let created: unknown = null;
    server.use(
      http.post(`${TEST_API_BASE_URL}/specs/validate`, async ({ request }) => {
        validated = await request.text();
        return HttpResponse.json({
          valid: true,
          name: "my-run",
          errors: [],
          warnings: ["load.stages[0].duration 60s is short"],
        });
      }),
      http.post(`${TEST_API_BASE_URL}/runs`, async ({ request }) => {
        created = await request.json();
        return HttpResponse.json({ ...succeededRun, status: "pending" }, { status: 201 });
      }),
    );
    const { router } = renderApp("/runs/new");
    await userEvent.click(await screen.findByRole("button", { name: "Validate" }));
    expect(await screen.findByText("Valid, with warnings")).toBeInTheDocument();
    expect(screen.getByText(/Warning: load.stages\[0\]/)).toBeInTheDocument();
    expect(validated).toContain("apiVersion: servtuner.dev/v2");
    await userEvent.click(screen.getByRole("button", { name: "Create run" }));
    await screen.findByRole("heading", { name: succeededRun.name });
    expect(created).toMatchObject({ apiVersion: "servtuner.dev/v2", kind: "RunSpec", load: { pacing: "poisson" } });
    expect(router.state.location.pathname).toBe(`/runs/${succeededRun.id}`);
  });

  it("clones a run from its submitted spec and blocks a YAML that does not parse", async () => {
    server.use(
      http.get(`${TEST_API_BASE_URL}/runs/:runId/files/spec.yaml`, () =>
        HttpResponse.text("apiVersion: servtuner.dev/v2\nkind: RunSpec\nname: cloned-run\n"),
      ),
    );
    renderApp(`/runs/new?from=${succeededRun.id}`);
    expect(await screen.findByDisplayValue("cloned-run")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "YAML" }));
    const editor = screen.getByRole("textbox", { name: "RunSpec YAML" });
    await userEvent.clear(editor);
    await userEvent.type(editor, "name: [[unclosed"); // "[[" types one "["
    expect(screen.getByRole("button", { name: "Create run" })).toBeDisabled();
    await userEvent.click(screen.getByRole("tab", { name: "Form" }));
    expect(screen.getByText("The YAML does not parse")).toBeInTheDocument();
  });
});
