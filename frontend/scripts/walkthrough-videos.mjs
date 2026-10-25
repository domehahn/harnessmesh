import { chromium } from "@playwright/test";
import { mkdir, rename } from "node:fs/promises";
import path from "node:path";

const outputDir = path.resolve(process.env.HARNESSMESH_VIDEO_DIR ?? "../artifacts/videos");
await mkdir(outputDir, { recursive: true });

async function saveVideo(context, page, name) {
  const videoPath = await page.video().path();
  await context.close();
  await rename(videoPath, path.join(outputDir, name));
}

async function recordCLI(browser) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, recordVideo: { dir: outputDir, size: { width: 1440, height: 900 } } });
  const page = await context.newPage();
  await page.setContent(`
    <style>body { margin:0; background:#07111f; color:#d7e5f5; font:21px ui-monospace, SFMono-Regular, Menlo, monospace; display:grid; place-items:center; height:100vh } main { width:1240px; line-height:1.45 } .prompt { color:#64d99b } .muted { color:#7f96ad } .out { color:#b6c9dc; padding-left:26px } .cursor { animation: blink 1s steps(2,end) infinite } @keyframes blink { 50% { opacity:0 } }</style>
    <main><div class="muted">HarnessMesh · interaktiver CLI-Walkthrough</div><br><div id="terminal"></div></main>`);
  await page.evaluate(async () => {
    const terminal = document.querySelector("#terminal");
    const commands = [
      ["harnessmesh version", "harnessmesh v0.2.0 · control plane ready"],
      ["harnessmesh config validate --config harnessmesh.json", "CONFIG=VALID · tenant and policy configuration accepted"],
      ["harnessmesh agents list --config harnessmesh.json", "claude-code   configured · codex   configured · antigravity   configured"],
      ["harnessmesh session list", "hm_demo_01   active   claude-code ↔ codex"],
      ["harnessmesh collaborate --config configs/antigravity-codex.json --task \"Review API\" --dry-run", "PLAN=READY · writer=antigravity · reviewer=codex · approvals=enabled"],
      ["harnessmesh mcp registry check --config harnessmesh.json --server serena --tool find_symbol --policy developer-readonly", "MCP=ALLOWED · tool and policy checks passed"],
      ["harnessmesh mcp tools-lock check --file .skil/mcp-tools.lock.json", "TOOL_LOCK=VALID · no drift detected"],
    ];
    const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
    for (const [command, output] of commands) {
      const line = document.createElement("div");
      line.innerHTML = '<span class="prompt">$</span> ';
      const text = document.createElement("span");
      line.append(text);
      terminal.append(line);
      for (const character of command) { text.textContent += character; await sleep(18); }
      await sleep(300);
      const result = document.createElement("div");
      result.className = "out";
      result.textContent = output;
      terminal.append(result);
      await sleep(550);
    }
    const done = document.createElement("div");
    done.className = "muted";
    done.innerHTML = 'Danach: Console öffnen und denselben Lauf live überwachen <span class="cursor">▊</span>';
    terminal.append(document.createElement("br"), done);
  });
  await page.waitForTimeout(1000);
  await saveVideo(context, page, "harnessmesh-cli-walkthrough.webm");
}

async function routeDashboard(page) {
  await page.route("**/api/v1/dashboard/**", async route => {
    const endpoint = new URL(route.request().url()).pathname.replace("/api/v1/dashboard", "");
    if (endpoint === "/summary" || endpoint === "") return route.fulfill({ json: { tenant: "tenant-demo", subject: "demo-operator", sessions: [], jobs: [], pending_approvals: [{ id: "approval-demo", agent: "review-agent", reason: "destructive tool", status: "pending" }], agent_health: [{ agent: "claude-code", status: "healthy" }] } });
    if (endpoint === "/usage") return route.fulfill({ json: { observation_count: 42, job_count: 7, metered_api_calls: 12 } });
    if (endpoint === "/optimization") return route.fulfill({ json: { by_model: [{ model: "gpt-5", input_tokens: 8200, output_tokens: 3100, cost_usd: 0.42 }], transports: { websocket: "healthy" }, connections: [{ agent: "claude-code", status: "connected" }], quality: [{ model: "gpt-5", success_rate: 0.96 }], credits: { available: false, reason: "provider quota not configured" }, optimization: { status: "observe", recommendation: "Use gpt-5 for review tasks", automatic_changes: false }, routing: { selected: "gpt-5", reason: "best observed quality", candidates: ["gpt-5"] } } });
    if (endpoint === "/incident") return route.fulfill({ json: { unhealthy_agents: [], failed_jobs: [], dead_jobs: [], requires_attention: false } });
    if (endpoint === "/assurance/graph") return route.fulfill({ json: { nodes: [{ id: "agent:claude-code", kind: "agent", subject: "claude-code", digest: "demo-digest", allowed: true }], edges: {}, observed_invocations: { "agent:claude-code": 12 }, quarantined: {} } });
    if (endpoint === "/assurance/events") return route.fulfill({ json: { events: [{ event_type: "security.decision", session_id: "session-demo", timestamp: new Date().toISOString(), payload: { allowed: true, risk_score: 0.02, reason: "Policy evaluation passed" } }] } });
    if (endpoint === "/spaces") return route.fulfill({ json: { tenant: "tenant-demo", spaces: [{ id: "space-demo", title: "Release review", lifecycle_state: "active", participants: { "claude-code": "connected" } }] } });
    if (endpoint.startsWith("/spaces/")) return route.fulfill({ json: { ok: true } });
    if (endpoint === "/simulation") return route.fulfill({ json: route.request().method() === "POST" ? { result: { passed: true, scenario_id: "tool-denied" } } : { scenarios: [{ id: "tool-denied", name: "Tool denied", fault: "tool_denied", description: "Safe approval boundary", expected: "deny" }] } });
    if (endpoint === "/approvals/approval-demo") return route.fulfill({ json: { approval: { id: "approval-demo", agent: "review-agent", reason: "destructive tool", status: "pending", requested_by: "demo-operator" }, audit: [{ id: "audit-demo", from_status: "", to_status: "pending", actor: "system", reason: "created" }] } });
    return route.fulfill({ json: {} });
  });
}

async function recordUI(browser) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, recordVideo: { dir: outputDir, size: { width: 1440, height: 900 } } });
  const page = await context.newPage();
  await routeDashboard(page);
  await page.goto("http://127.0.0.1:5173/#monitoring");
  await page.getByPlaceholder("hm_live_…").fill("hm_live_demo");
  await page.getByRole("button", { name: "Connect" }).click();
  await page.waitForTimeout(1800);
  await page.getByRole("button", { name: "Approvals" }).click();
  await page.waitForTimeout(1200);
  await page.getByText("review-agent").click();
  await page.waitForTimeout(1200);
  await page.getByRole("button", { name: "Orchestrator" }).click();
  await page.waitForTimeout(1200);
  await page.getByRole("button", { name: "Simulation" }).click();
  await page.waitForTimeout(900);
  await page.getByRole("button", { name: "Run safe pass" }).click();
  await page.waitForTimeout(1800);
  await saveVideo(context, page, "harnessmesh-ui-walkthrough.webm");
}

const browser = await chromium.launch({ headless: true });
await recordCLI(browser);
await recordUI(browser);
await browser.close();
console.log(`VIDEO=PASS cli=${path.join(outputDir, "harnessmesh-cli-walkthrough.webm")} ui=${path.join(outputDir, "harnessmesh-ui-walkthrough.webm")}`);
