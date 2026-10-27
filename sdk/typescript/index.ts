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
  jobs<T = unknown>() { return this.request<T>("/jobs"); }
  approvals<T = unknown>() { return this.request<T>("/approvals"); }
  observabilityLogs<T = unknown>() { return this.request<T>("/observability/logs"); }
  observabilityTraces<T = unknown>() { return this.request<T>("/observability/traces"); }
}
