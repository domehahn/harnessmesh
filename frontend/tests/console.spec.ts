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
  await expect(page).toHaveScreenshot("public-shell.png", { fullPage: true, animations: "disabled", maxDiffPixelRatio: 0.02 });
});

async function mockDashboard(page: import("@playwright/test").Page) {
  await page.route("/api/v1/dashboard/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/v1/dashboard", "");
    if (path === "/summary" || path === "") return route.fulfill({ json: { tenant: "tenant-e2e", subject: "e2e-user", sessions: [], jobs: [], pending_approvals: [{ id: "approval-1", agent: "review-agent", reason: "destructive tool", status: "pending" }], agent_health: [] } });
    if (path === "/usage") return route.fulfill({ json: { observation_count: 4, job_count: 2, metered_api_calls: 0 } });
    if (path === "/optimization") return route.fulfill({ json: { by_model: [], transports: {}, connections: [], quality: [], credits: { available: false, reason: "not configured" }, optimization: { status: "observe", recommendation: "Collect more data", automatic_changes: false }, routing: { selected: "", reason: "No candidate", candidates: [] } } });
    if (path === "/incident") return route.fulfill({ json: { unhealthy_agents: [], failed_jobs: [], dead_jobs: [], requires_attention: false } });
    if (path === "/simulation") {
      return route.fulfill({ json: route.request().method() === "POST" ? { result: { passed: true, scenario_id: "tool-denied" } } : { scenarios: [{ id: "tool-denied", name: "Tool denied", fault: "tool_denied", description: "safe", expected: "deny" }] } });
    }
    if (path === "/approvals/approval-1") return route.fulfill({ json: { approval: { id: "approval-1", agent: "review-agent", reason: "destructive tool", status: "pending", requested_by: "e2e-user" }, audit: [{ id: "audit-1", from_status: "", to_status: "pending", actor: "system", reason: "created" }] } });
    return route.fulfill({ json: {} });
  });
}

async function connect(page: import("@playwright/test").Page) {
  await page.getByPlaceholder("hm_live_…").fill("hm_live_e2e");
  await page.getByRole("button", { name: "Connect" }).click();
  await expect(page.getByText("tenant-e2e")).toBeVisible();
}

test("renders authenticated monitoring with empty tenant data", async ({ page }) => {
  await mockDashboard(page);
  await page.goto("/#monitoring");
  await connect(page);
  await expect(page.getByRole("heading", { name: "Monitoring" })).toBeVisible();
  await expect(page.getByText("No active incidents")).toBeVisible();
  await expect(page.getByText("No agent health records")).toBeVisible();
});

test("runs a deterministic simulation pass", async ({ page }) => {
  await mockDashboard(page);
  await page.goto("/#simulation");
  await connect(page);
  await expect(page.getByText("Tool denied")).toBeVisible();
  await page.getByRole("button", { name: "Run safe pass" }).click();
  await expect(page.getByText("passed")).toBeVisible();
});

test("opens approval detail and audit history", async ({ page }) => {
  await mockDashboard(page);
  await page.goto("/#approvals");
  await connect(page);
  await page.getByText("review-agent").click();
  await expect(page.getByText("APPROVAL DETAIL")).toBeVisible();
  await expect(page.getByText("Audit history")).toBeVisible();
  await expect(page.getByText("created")).toBeVisible();
});
