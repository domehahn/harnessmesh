import { chromium } from "@playwright/test";
import { mkdir, rename } from "node:fs/promises";
import path from "node:path";

const outputDir = path.resolve(process.env.HARNESSMESH_VIDEO_DIR ?? "../artifacts/videos");
await mkdir(outputDir, { recursive: true });

const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  recordVideo: { dir: outputDir, size: { width: 1440, height: 900 } },
});
const page = await context.newPage();

await page.route("**/api/v1/dashboard/**", async route => {
  const requestURL = new URL(route.request().url());
  const endpoint = requestURL.pathname.replace("/api/v1/dashboard", "");
  if (endpoint === "/summary" || endpoint === "") {
    return route.fulfill({ json: {
      tenant: "tenant-demo",
      subject: "demo-operator",
      sessions: [],
      jobs: [],
      pending_approvals: [{ id: "approval-demo", agent: "review-agent", reason: "destructive tool", status: "pending" }],
      agent_health: [{ agent: "claude-code", status: "healthy" }],
    } });
  }
  if (endpoint === "/usage") return route.fulfill({ json: { observation_count: 42, job_count: 7, metered_api_calls: 12 } });
  if (endpoint === "/optimization") return route.fulfill({ json: { by_model: [{ model: "gpt-5", input_tokens: 8200, output_tokens: 3100, cost_usd: 0.42 }], transports: { websocket: "healthy" }, connections: [{ agent: "claude-code", status: "connected" }], quality: [{ model: "gpt-5", success_rate: 0.96 }], credits: { available: false, reason: "provider quota not configured" }, optimization: { status: "observe", recommendation: "Use gpt-5 for review tasks", automatic_changes: false }, routing: { selected: "gpt-5", reason: "best observed quality", candidates: ["gpt-5"] } } });
  if (endpoint === "/incident") return route.fulfill({ json: { unhealthy_agents: [], failed_jobs: [], dead_jobs: [], requires_attention: false } });
  if (endpoint === "/spaces") return route.fulfill({ json: { tenant: "tenant-demo", spaces: [{ id: "space-demo", title: "Release review", lifecycle_state: "active", participants: { "claude-code": "connected" } }] } });
  if (endpoint.startsWith("/spaces/")) return route.fulfill({ json: { ok: true } });
  if (endpoint === "/simulation") return route.fulfill({ json: route.request().method() === "POST" ? { result: { passed: true, scenario_id: "tool-denied" } } : { scenarios: [{ id: "tool-denied", name: "Tool denied", fault: "tool_denied", description: "Safe approval boundary", expected: "deny" }] } });
  if (endpoint === "/approvals/approval-demo") return route.fulfill({ json: { approval: { id: "approval-demo", agent: "review-agent", reason: "destructive tool", status: "pending", requested_by: "demo-operator" }, audit: [{ id: "audit-demo", from_status: "", to_status: "pending", actor: "system", reason: "created" }] } });
  return route.fulfill({ json: {} });
});

await page.setContent(`
  <style>
    body { margin:0; background:#07111f; color:#d7e5f5; font:24px ui-monospace, SFMono-Regular, Menlo, monospace; display:grid; place-items:center; height:100vh }
    main { width:1120px } .prompt { color:#64d99b } .muted { color:#7f96ad } .cursor { animation: blink 1s steps(2,end) infinite } @keyframes blink { 50% { opacity:0 } }
  </style>
  <main><div class="muted">HarnessMesh · CLI walkthrough</div><br>
  <div><span class="prompt">$</span> cp .env.example .env</div>
  <div><span class="prompt">$</span> make local-up</div>
  <div class="muted">✓ MCP, Bridge, Console and worker started</div>
  <div><span class="prompt">$</span> harnessmesh agents list</div>
  <div>claude-code   connected   websocket</div>
  <div>codex         connected   mcp-bridge</div>
  <div><span class="prompt">$</span> harnessmesh session create --agent claude-code</div>
  <div class="muted">✓ session created · approval policy enabled</div>
  <br><div class="muted">Next: open the console to monitor the same workflow <span class="cursor">▊</span></div></main>`);
await page.waitForTimeout(3500);

await page.goto("http://127.0.0.1:5173/#monitoring");
await page.getByPlaceholder("hm_live_…").fill("hm_live_demo");
await page.getByRole("button", { name: "Connect" }).click();
await page.waitForTimeout(1800);
await page.getByRole("button", { name: "Approvals" }).click();
await page.waitForTimeout(1600);
await page.getByText("review-agent").click();
await page.waitForTimeout(1600);
await page.getByRole("button", { name: "Orchestrator" }).click();
await page.waitForTimeout(1600);
await page.getByRole("button", { name: "Simulation" }).click();
await page.waitForTimeout(1200);
await page.getByRole("button", { name: "Run safe pass" }).click();
await page.waitForTimeout(2200);

const videoPath = await page.video().path();
await context.close();
await browser.close();
const target = path.join(outputDir, "harnessmesh-walkthrough.webm");
await rename(videoPath, target);
console.log(`VIDEO=PASS path=${target}`);
