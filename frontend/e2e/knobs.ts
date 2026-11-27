import { expect, type Page } from "@playwright/test";

/**
 * Set a deploy form's knobs through the chips, from "name: value" lines (bench's knob names):
 * every chip is removed, then each knob is found in the picker by its name and given its value.
 */
export async function setKnobs(page: Page, lines: string) {
  const knobs = page.getByRole("list", { name: "vLLM knobs" });
  // Always the first: .all() would hand out nth(0), nth(1)… and nth(1) is gone once one chip is.
  const removes = knobs.getByRole("button", { name: /^Remove / });
  while ((await removes.count()) > 0) await removes.first().click();
  await expect(removes).toHaveCount(0);
  for (const line of lines.split("\n").filter((text) => text.trim())) {
    const [name = "", value = ""] = line.split(":").map((part) => part.trim());
    const picker = page.getByRole("combobox", { name: "Add a knob" });
    await expect(picker).toBeEnabled();
    await picker.fill(name);
    await page.getByRole("option").first().click();
    await page.getByRole("textbox", { name: "Value" }).fill(value);
    await page.getByRole("button", { name: "Done" }).click();
  }
}
