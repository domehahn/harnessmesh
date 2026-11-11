export class HarnessMeshError extends Error { constructor(public status: number, message: string) { super(message); } }

export interface DecisionProposal {
  space_id: string;
  title: string;
  summary: string;
  evidence_ids?: string[];
  file_scope?: string[];
}

export interface ReviewFinding {
  severity: "low" | "medium" | "high" | "critical";
  file: string;
  line?: number;
  title: string;
  detail: string;
}

export interface BrowserActionSpec {
  tenant: string;
  project?: string;
  agent: string;
  provider: "playwright" | "browser-use" | "stagehand";
  kind: "navigate" | "act" | "extract" | "observe";
  url: string;
  instruction?: string;
  approved: boolean;
}

export interface A2ATaskRequest {
  sender?: string;
  prompt: string;
  metadata: {
    space_id: string;
    channel?: string;
    [key: string]: unknown;
  };
}

export interface HarnessPatchProposal {
  skill: string;
  base_version: string;
  proposed_version: string;
  requires_approval: true;
  added_tools?: string[];
  enable_strict_order?: boolean;
  suggested_steps?: string[];
  findings?: string[];
}

export interface HarnessProbeReport {
  skill: string;
  base_version: string;
  candidate_version: string;
  proposal: HarnessPatchProposal;
  decision: "promote" | "hold" | "rollback";
  requires_approval: true;
}

export type OrchestrationFramework = "langgraph" | "crewai" | "autogen" | "agno";

export interface WorkflowCheckpoint {
  checkpoint_id: string;
  framework: OrchestrationFramework;
  session_id: string;
  step: number;
  agent: string;
  state_digest: string;
  approvals_ok: boolean;
}

export interface AgentHandoff {
  from_agent: string;
  to_agent: string;
  framework: OrchestrationFramework;
  reason: string;
  scope?: string[];
  state_digest?: string;
  approved: boolean;
}

export class OrchestrationBridge {
  readonly checkpoints: WorkflowCheckpoint[] = [];
  readonly handoffs: AgentHandoff[] = [];

  constructor(
    readonly framework: OrchestrationFramework,
    readonly sessionID: string,
  ) {}

  recordCheckpoint(step: number, agent: string, stateDigest: string, approvalsOk = true): WorkflowCheckpoint {
    const cp: WorkflowCheckpoint = {
      checkpoint_id: `${this.sessionID}-step-${step}`,
      framework: this.framework,
      session_id: this.sessionID,
      step,
      agent,
      state_digest: stateDigest,
      approvals_ok: approvalsOk,
    };
    this.checkpoints.push(cp);
    return cp;
  }

  authorizeHandoff(fromAgent: string, toAgent: string, reason: string, scope: string[] = [], stateDigest = "", approved = true): AgentHandoff {
    if (!fromAgent.trim() || !toAgent.trim() || fromAgent.trim() === toAgent.trim()) {
      throw new Error("fromAgent and toAgent must be non-empty and distinct");
    }
    if (!approved) {
      throw new Error("unapproved multi-agent handoff rejected by policy");
    }
    const ho: AgentHandoff = {
      from_agent: fromAgent,
      to_agent: toAgent,
      framework: this.framework,
      reason,
      scope,
      state_digest: stateDigest,
      approved,
    };
    this.handoffs.push(ho);
    return ho;
  }
}

export class HarnessMeshClient {
  constructor(readonly baseURL: string, readonly apiKey: string) {}
  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers); headers.set("X-HarnessMesh-API-Key", this.apiKey); headers.set("Content-Type", "application/json");
    const response = await fetch(`${this.baseURL.replace(/\/$/, "")}/api/v1/dashboard${path}`, { ...init, headers });
    if (!response.ok) throw new HarnessMeshError(response.status, await response.text());
    return await response.json() as T;
  }
  summary<T = unknown>() { return this.request<T>("/summary"); }
  skills<T = unknown>() { return this.request<T>("/skills"); }
  marketplace<T = unknown>() { return this.request<T>("/marketplace"); }
  sessions<T = unknown>() { return this.request<T>("/sessions"); }
  session<T = unknown>(id: string) { return this.request<T>(`/sessions/${encodeURIComponent(id)}`); }
  sessionReplay<T = unknown>(id: string) { return this.request<T>(`/sessions/${encodeURIComponent(id)}/replay`); }
  jobs<T = unknown>() { return this.request<T>("/jobs"); }
  job<T = unknown>(id: string) { return this.request<T>(`/jobs/${encodeURIComponent(id)}`); }
  jobAction<T = unknown>(id: string, action: "retry" | "cancel") { return this.request<T>(`/jobs/${encodeURIComponent(id)}`, { method: "POST", body: JSON.stringify({ action }) }); }
  approvals<T = unknown>() { return this.request<T>("/approvals"); }
  approval<T = unknown>(id: string) { return this.request<T>(`/approvals/${encodeURIComponent(id)}`); }
  decideApproval<T = unknown>(id: string, status: "approved" | "rejected") { return this.request<T>(`/approvals/${encodeURIComponent(id)}`, { method: "POST", body: JSON.stringify({ status }) }); }
  observabilityLogs<T = unknown>() { return this.request<T>("/observability/logs"); }
  observabilityTraces<T = unknown>() { return this.request<T>("/observability/traces"); }
  async submitA2ATask<T = unknown>(tenant: string, a2aToken: string, task: A2ATaskRequest): Promise<T> {
    const response = await fetch(`${this.baseURL.replace(/\/$/, "")}/a2a/v1/tasks`, {
      method: "POST",
      headers: {
        "Authorization": `Bearer ${a2aToken}`,
        "X-HarnessMesh-Tenant": tenant,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(task),
    });
    if (!response.ok) throw new HarnessMeshError(response.status, await response.text());
    return await response.json() as T;
  }
}
