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
    <style>
      * { box-sizing: border-box }
      body { margin:0; background:linear-gradient(135deg,#182337,#52637a); color:#d7e5f5; font:21px ui-monospace, SFMono-Regular, Menlo, monospace; display:grid; place-items:center; height:100vh }
      .window { width:1280px; height:760px; overflow:hidden; border:1px solid rgba(255,255,255,.2); border-radius:14px; background:#111318; box-shadow:0 30px 80px rgba(0,0,0,.45) }
      .titlebar { height:44px; display:flex; align-items:center; justify-content:center; position:relative; background:linear-gradient(#303238,#25262b); color:#c8c9cc; font:13px -apple-system,BlinkMacSystemFont,"SF Pro Text",sans-serif }
      .traffic { position:absolute; left:16px; display:flex; gap:8px }.traffic i { width:13px; height:13px; display:block; border-radius:50%; box-shadow:inset 0 0 0 1px rgba(0,0,0,.2) }.red { background:#ff5f57 }.yellow { background:#febc2e }.green { background:#28c840 }
      .tabbar { height:35px; display:flex; align-items:center; padding-left:24px; background:#1b1c20; color:#9b9da5; font:12px -apple-system,BlinkMacSystemFont,"SF Pro Text",sans-serif }.tab { padding:7px 18px; border-bottom:2px solid #64d99b; color:#f0f1f2 }
      main { height:681px; padding:28px 34px; line-height:1.45; overflow:hidden; font-size:18px }.muted { color:#7f96ad }.prompt { color:#64d99b }.out { color:#b6c9dc; padding-left:26px; white-space:pre-line }.cursor { animation: blink 1s steps(2,end) infinite } @keyframes blink { 50% { opacity:0 } }
    </style>
    <div class="window"><div class="titlebar"><div class="traffic"><i class="red"></i><i class="yellow"></i><i class="green"></i></div>harnessmesh — zsh — 128×42</div><div class="tabbar"><div class="tab">harnessmesh — zsh</div></div><main><div class="muted">Last login: today on ttys001</div><div id="terminal"></div></main></div>`);
  await page.evaluate(async () => {
    const terminal = document.querySelector("#terminal");
    const commands = [
      ["cp .env.example .env", "ENV=READY · local configuration copied"],
      ["make local-up", "✓ MCP · Bridge · Console · worker started"],
      ["docker compose ps", "harnessmesh-mcp   healthy   127.0.0.1:8787\nharnessmesh-console   running   127.0.0.1:3000"],
      ["harnessmesh version", "harnessmesh v0.2.0 · control plane ready"],
      ["harnessmesh config validate --config harnessmesh.json", "CONFIG=VALID · tenant and policy configuration accepted"],
      ["harnessmesh tenant create --id tenant-demo --slug demo --name \"Local Demo\"", "TENANT=CREATED · tenant-demo"],
      ["harnessmesh tenant member --id tenant-demo --subject demo-user --role owner", "MEMBERSHIP=ACTIVE · demo-user → owner"],
      ["harnessmesh tenant key --id tenant-demo --subject demo-user", "API_KEY=ISSUED · hm_live_•••••••• (shown once)"],
      ["export HARNESSMESH_API_KEY=hm_live_demo", "API_KEY=EXPORTED · console login ready"],
      ["harnessmesh agents list --config harnessmesh.json", "claude-code   configured · codex   configured · antigravity   configured"],
      ["harnessmesh mcp registry check --config harnessmesh.json --server serena --tool find_symbol --policy developer-readonly", "MCP=ALLOWED · tool and policy checks passed"],
      ["harnessmesh mcp tools-lock check --file .skil/mcp-tools.lock.json", "TOOL_LOCK=VALID · no drift detected"],
      ["harnessmesh collaborate --config configs/antigravity-codex.json --task \"Review API\" --dry-run", "PLAN=READY · writer=antigravity · reviewer=codex · approvals=enabled"],
      ["harnessmesh session list", "hm_demo_01   active   claude-code ↔ codex"],
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
    if (endpoint === "/summary" || endpoint === "") return route.fulfill({ json: { tenant: "tenant-demo", subject: "demo-operator", sessions: [], jobs: [{ id: "job-demo", kind: "agent.review", status: "running" }], pending_approvals: [{ id: "approval-demo", agent: "review-agent", reason: "destructive tool", status: "pending" }], agent_health: [{ agent: "claude-code", status: "healthy" }] } });
    if (endpoint === "/usage") return route.fulfill({ json: { observation_count: 42, job_count: 7, metered_api_calls: 12 } });
    if (endpoint === "/optimization") return route.fulfill({ json: { by_model: [{ model: "gpt-5", input_tokens: 8200, output_tokens: 3100, cost_usd: 0.42 }], transports: { websocket: "healthy" }, connections: [{ agent: "claude-code", status: "connected" }], quality: [{ model: "gpt-5", success_rate: 0.96 }], credits: { available: false, reason: "provider quota not configured" }, optimization: { status: "observe", recommendation: "Use gpt-5 for review tasks", automatic_changes: false }, routing: { selected: "gpt-5", reason: "best observed quality", candidates: ["gpt-5"] } } });
    if (endpoint === "/incident") return route.fulfill({ json: { unhealthy_agents: [], failed_jobs: [], dead_jobs: [], requires_attention: false } });
    if (endpoint === "/assurance/graph") return route.fulfill({ json: { nodes: [{ id: "agent:claude-code", kind: "agent", subject: "claude-code", digest: "demo-digest", allowed: true }], edges: {}, observed_invocations: { "agent:claude-code": 12 }, quarantined: {} } });
    if (endpoint === "/assurance/events") return route.fulfill({ json: { events: [{ event_type: "security.decision", session_id: "session-demo", timestamp: new Date().toISOString(), payload: { allowed: true, risk_score: 0.02, reason: "Policy evaluation passed" } }] } });
    if (endpoint === "/jobs") return route.fulfill({ json: { items: [{ id: "job-demo", kind: "agent.review", status: "running" }], total: 1, has_more: false } });
    if (endpoint === "/jobs/job-demo") return route.fulfill({ json: { id: "job-demo", kind: "agent.review", status: "running", attempts: 1, payload: { task: "Review API" }, events: [{ type: "job.created", message: "Job accepted by control plane", created_at: "2026-10-25T10:00:00Z" }, { type: "agent.connected", message: "Codex reviewer connected through MCP bridge", created_at: "2026-10-25T10:00:02Z" }, { type: "trace.span", message: "Policy evaluation and model routing completed", created_at: "2026-10-25T10:00:04Z" }] } });
    if (endpoint === "/jobs/job-demo/events/stream") return route.fulfill({ status: 200, contentType: "text/event-stream", body: "event: job\ndata: {\"status\":\"running\"}\n\n" });
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
  await page.waitForTimeout(2200);
  await page.mouse.wheel(0, 650);
  await page.waitForTimeout(1400);
  await page.getByRole("button", { name: "Optimization" }).click();
  await page.waitForTimeout(1800);
  await page.getByRole("button", { name: "Usage & limits" }).click();
  await page.waitForTimeout(1500);
  await page.getByRole("button", { name: "Jobs" }).click();
  await page.waitForTimeout(1500);
  await page.getByText("job-demo").click();
  await page.waitForTimeout(2200);
  await page.getByRole("button", { name: "Monitoring" }).click();
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
