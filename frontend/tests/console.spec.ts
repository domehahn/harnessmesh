import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

test("renders the unauthenticated console safely", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveTitle("HarnessMesh Console");
  await expect(page.getByText("HarnessMesh").first()).toBeVisible();
  await expect(page.getByText("Connect your tenant")).toBeVisible();
  await expect(page.getByRole("button", { name: "Connect" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Overview" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Settings" })).toBeVisible();
});

test("supports keyboard navigation for the connection form", async ({ page }) => {
  await page.goto("/");
  const input = page.getByPlaceholder("hm_live_…");
  await input.focus();
  await expect(input).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Connect" })).toBeFocused();
});

test("has no critical accessibility violations on the public shell", async ({ page }) => {
  await page.goto("/");
  const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
  expect(results.violations.filter(item => item.impact === "critical")).toEqual([]);
});

test("keeps a stable public-shell visual baseline", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveScreenshot("public-shell.png", { fullPage: true, animations: "disabled" });
});
