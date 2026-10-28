export class HarnessMeshError extends Error { constructor(public status: number, message: string) { super(message); } }
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
}
