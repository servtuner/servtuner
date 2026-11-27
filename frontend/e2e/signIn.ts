import { expect, type Page } from "@playwright/test";

/** Signs in through the login page when the harness made an admin; an api under
 * auth.mode none shows the app at once, and then the harness sets no credentials. */
export async function signIn(page: Page) {
  const username = process.env.SERVTUNER_SMOKE_USERNAME;
  const password = process.env.SERVTUNER_SMOKE_PASSWORD;
  if (!username || !password) return;
  await page.goto("runs");
  await page.getByLabel("Username").fill(username);
  await page.locator("input[autocomplete=current-password]").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("button", { name: "Account menu" })).toBeVisible();
}
