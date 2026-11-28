import { expect, test, type Page } from "@playwright/test";
import { setKnobs } from "./knobs";

// The deploy path entirely from the UI, on a real cluster (`just e2e-managed-ui`, or
// `e2e/cluster.py ... --tuning-host --ui` for a GPU host): add a managed server, deploy it, read its
// log, redeploy with other flags, benchmark it, free it, tune it, deploy the recommendation, and
// delete it. e2e/tuning.py
// passes the target as SERVTUNER_UI_TARGET (UiTarget); without it (the smoke run) this test skips.
type UiTarget = {
  name: string;
  placement: "kubernetes" | "host";
  // The managed target form's fields by label, as a person would fill them.
  fields: Record<string, string>;
  // Two deploys: the first, then a redeploy with other flags. Each is "name: value" lines.
  knobs: [string, string];
  // A flag the redeploy's container args must show.
  redeployedArg: string;
  // Text the first deploy's vLLM log must contain.
  logText: string;
  // The tuning session: its search space (YAML) and SLOs by label.
  space: string;
  slos: Record<string, string>;
};

const target: UiTarget | undefined = process.env.SERVTUNER_UI_TARGET
  ? (JSON.parse(process.env.SERVTUNER_UI_TARGET) as UiTarget)
  : undefined;

// A first pull of a fresh image into kind, or a real model loading on a GPU.
const BOOT_MS = 8 * 60_000;
// The starter run: 30 s warmup and two 60 s stages, plus the Job's start and the analysis.
const RUN_MS = 8 * 60_000;
// One run and its boot probes (stages of 90 s), plus freeing the target at the end.
const SESSION_MS = 40 * 60_000;

test.skip(!target, "runs only on a cluster: e2e/cluster.py --tuning --ui");

// The header's nav link: the overview home page links its sections by the same names.
const nav = (page: Page, name: string) => page.getByRole("banner").getByRole("link", { name, exact: true });

async function openTarget(page: Page, name: string) {
  await nav(page, "Managed Servers").click();
  await page.getByRole("link", { name }).click();
  await expect(page.getByRole("heading", { name })).toBeVisible();
}

async function deploy(page: Page, knobs: string, label: "Deploy" | "Redeploy") {
  await setKnobs(page, knobs);
  await page.getByRole("button", { name: label, exact: true }).click();
  await expect(page.getByText("Ready", { exact: true })).toBeVisible({ timeout: BOOT_MS });
}

async function expectFreed(page: Page, ui: UiTarget) {
  await expect(page.getByText("Ready", { exact: true })).toBeHidden({ timeout: BOOT_MS });
  if (ui.placement === "kubernetes")
    await expect(page.getByText(/no pod: not deployed, or scaled to zero/)).toBeVisible({ timeout: BOOT_MS });
}

test("deploy, benchmark, tune and tear down a managed server from the UI", async ({ page }) => {
  const ui = target!;
  test.setTimeout(BOOT_MS * 6 + RUN_MS + SESSION_MS);

  await test.step("add the managed server", async () => {
    await page.goto("");
    await nav(page, "Managed Servers").click();
    await page.getByRole("button", { name: "Add a managed server" }).click();
    if (ui.placement === "host") await page.getByText("A host with servtuner launcher").click();
    await page.getByRole("textbox", { name: "Name", exact: true }).fill(ui.name);
    for (const [label, value] of Object.entries(ui.fields)) {
      const input = page.getByRole("textbox", { name: label, exact: true });
      // Metrics labels and named Secrets sit in the host form's collapsed Advanced section.
      if (!(await input.isVisible())) await page.getByRole("button", { name: /^Advanced/ }).click();
      await input.fill(value);
    }
    await page.getByRole("button", { name: "Save managed target" }).click();
    await page.getByRole("link", { name: ui.name }).click();
  });

  await test.step("deploy it and read its log", async () => {
    await deploy(page, ui.knobs[0], "Deploy");
    await expect(page.getByLabel("vLLM log")).toContainText(ui.logText);
  });

  await test.step("redeploy with other flags", async () => {
    await deploy(page, ui.knobs[1], "Redeploy");
    await expect(page.getByLabel("Container args")).toContainText(ui.redeployedArg);
  });

  await test.step("benchmark it", async () => {
    await page.getByRole("link", { name: "Benchmark" }).click();
    await expect(page.getByRole("textbox", { name: "Name" })).toHaveValue(`${ui.name}-bench`);
    await page.getByRole("button", { name: "Create run" }).click();
    await expect(page.getByRole("heading", { name: `${ui.name}-bench` })).toBeVisible();
    await expect(page.getByText("Succeeded", { exact: true }).first()).toBeVisible({ timeout: RUN_MS });
  });

  await test.step("free the GPU", async () => {
    await openTarget(page, ui.name);
    await page.getByRole("button", { name: ui.placement === "host" ? "Stop vLLM" : "Scale to zero" }).click();
    await expectFreed(page, ui);
  });

  await test.step("tune it", async () => {
    await nav(page, "Tuning").click();
    await page.getByRole("link", { name: "New session" }).click();
    await page.getByRole("combobox", { name: /^Managed target/ }).click();
    await page.getByRole("option", { name: new RegExp(`^${ui.name} `) }).click();
    await page.getByRole("combobox", { name: /^Workload preset/ }).click();
    await page.getByRole("option", { name: "chat-short@1" }).click();
    for (const [label, value] of Object.entries(ui.slos))
      await page.getByRole("textbox", { name: label, exact: true }).fill(value);
    await page.getByRole("button", { name: "Edit as YAML" }).click();
    await page.getByRole("textbox", { name: /^Search space/ }).fill(ui.space);
    await page.getByRole("textbox", { name: "Max runs" }).fill("1");
    await page.getByRole("button", { name: "Start session" }).click();
    // Wait for any end, so a failed or fruitless session fails here with its reason on screen.
    await expect(page.getByText(/^(Done|Failed|Cancelled)$/).first()).toBeVisible({ timeout: SESSION_MS });
    await expect(page.getByText("Done", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("Recommended command line")).toBeVisible();
    // The session's runs compared, and the configurations nothing beat on both axes.
    await expect(page.getByRole("heading", { name: "Pareto set" })).toBeVisible();
    await expect(page.getByRole("link", { name: "the full comparison" })).toBeVisible();
  });

  await test.step("the session freed it; deploy its recommendation", async () => {
    await page.getByRole("button", { name: "Deploy this configuration" }).click();
    await expect(page.getByRole("heading", { name: ui.name })).toBeVisible();
    await expect(page.getByText("Ready", { exact: true })).toBeVisible({ timeout: BOOT_MS });
  });

  await test.step("its page lists its benchmark and its session's runs", async () => {
    await expect(page.getByRole("link", { name: `${ui.name}-bench` })).toBeVisible();
    await expect(page.getByRole("link", { name: /^tune-/ }).first()).toBeVisible();
  });

  await test.step("free it, see it freed in the list, and delete it", async () => {
    const freed = ui.placement === "host" ? "Stopped" : "Scaled to zero";
    await page.getByRole("button", { name: ui.placement === "host" ? "Stop vLLM" : "Scale to zero" }).click();
    await expectFreed(page, ui);
    await nav(page, "Managed Servers").click();
    await expect(page.getByRole("row", { name: new RegExp(ui.name) }).getByText(freed)).toBeVisible();
    await page.getByRole("link", { name: ui.name }).click();
    await page.getByRole("button", { name: "Delete managed target" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
    await expect(page.getByText("No managed servers yet.")).toBeVisible();
  });
});
