import { expect, test } from "@playwright/test";
import { setKnobs } from "./knobs";
import { signIn } from "./signIn";

test.beforeEach(async ({ page }) => signIn(page));

// One pass through the views a user needs, against the real api and bench:
// New run (validated by bench) → the run succeeds → its detail tabs → the
// runs list → the planner → deleting the run. bench's null engine offers no load, so the run's
// analysis is degraded; the point is the wiring, not the numbers.
test("create a run, watch it finish, and read it", async ({ page }) => {
  await page.goto("runs/new");
  const name = page.getByRole("textbox", { name: "Name" });
  await name.fill("smoke-run");
  await page.getByRole("button", { name: "Validate" }).click();
  await expect(page.getByText(/^Valid/)).toBeVisible();

  await page.getByRole("button", { name: "Create run" }).click();
  await expect(page.getByRole("heading", { name: "smoke-run" })).toBeVisible();
  // The header badge; the status timeline repeats it.
  await expect(page.getByText("Succeeded", { exact: true }).first()).toBeVisible({ timeout: 90_000 });

  for (const tab of ["Latency", "Config", "Files"]) {
    await page.getByRole("tab", { name: tab }).click();
    await expect(page.getByRole("tabpanel")).toBeVisible();
  }
  await expect(page.getByRole("link", { name: "manifest.json" })).toBeVisible();

  await page.getByRole("link", { name: "All runs" }).click();
  await expect(page.getByRole("link", { name: "smoke-run" })).toBeVisible();

  await page.getByRole("button", { name: "More pages" }).click();
  await page.getByRole("menuitem", { name: "Planner" }).click();
  await expect(page.getByRole("heading", { name: "Capacity planner" })).toBeVisible();
  // A user's delete, from the run's own page: the impact, then the run and its bundle are gone.
  await page.goto("runs");
  await page.getByRole("link", { name: "smoke-run" }).click();
  await page.getByRole("button", { name: "Delete run" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/the run and its \d+ bundle files/)).toBeVisible();
  await dialog.getByRole("button", { name: "Delete" }).click();
  await expect(page.getByRole("heading", { name: "Runs" })).toBeVisible();
  await expect(page.getByRole("link", { name: "smoke-run" })).toHaveCount(0);
});

// A managed vLLM from the nav's list to its own page. The smoke api has no
// Kubernetes runner, so Deploy must come back with the api's reason, shown.
test("save a managed vLLM and open its page", async ({ page }) => {
  await page.goto("runs");
  await page.getByRole("link", { name: "Managed vLLM" }).click();
  await page.getByRole("button", { name: "Add a managed vLLM" }).click();
  await page.getByRole("textbox", { name: /^Name(?!space)/ }).fill("smoke-vllm");
  await page.getByRole("textbox", { name: /^Namespace/ }).fill("servtuner-smoke");
  await page.getByRole("textbox", { name: /^Image/ }).fill("vllm/vllm-openai:v0.34.0");
  await page.getByRole("textbox", { name: /^Model$/ }).fill("Qwen/Qwen3-0.6B");
  await page.getByRole("button", { name: "Save managed target" }).click();

  await page.getByRole("link", { name: "smoke-vllm" }).click();
  await expect(page.getByRole("heading", { name: "smoke-vllm" })).toBeVisible();
  await expect(page.getByText("Not deployed yet.")).toBeVisible();
  await expect(page.getByText("http://smoke-vllm.servtuner-smoke.svc:8000/v1")).toBeVisible();

  // Replacing two chips, as a redeploy does: the 3070 UI test hung here (every chip removed).
  await setKnobs(page, "vllm.gpu_memory_utilization: 0.75\nvllm.max_num_seqs: 32");
  await setKnobs(page, "vllm.max_num_seqs: 64");
  await expect(page.getByRole("list", { name: "vLLM knobs" }).getByRole("listitem")).toHaveCount(1);
  await page.getByRole("button", { name: "Deploy" }).click();
  await expect(page.getByText(/no deployer/)).toBeVisible();

  await page.getByRole("button", { name: "Details" }).click();
  await page.getByRole("button", { name: "Edit" }).click();
  await page.getByRole("textbox", { name: "Memory", exact: true }).fill("96Gi");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("cell", { name: "8, 96Gi" })).toBeVisible();

  // Benchmark opens New run on the target's run block, and bench accepts it.
  await page.getByRole("link", { name: "Benchmark" }).click();
  await expect(page.getByRole("textbox", { name: "Name" })).toHaveValue("smoke-vllm-bench");
  await page.getByRole("button", { name: "Validate" }).click();
  await expect(page.getByText(/^Valid/)).toBeVisible();
});
