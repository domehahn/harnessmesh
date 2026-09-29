import * as http from 'node:http';
import * as https from 'node:https';
import { URL } from 'node:url';
import { EventEmitter } from 'node:events';
import WebSocket from 'ws';

import {
  AgentInbox,
  BRIDGE_PROTOCOL_VERSION,
  BridgeEvent,
  CollaborationSpace,
  CollaborationStatusResponse,
  CreateChangeRequest,
  EvidencePayload,
  FindingPayload,
  MeshChange,
  PublishRequest,
  PublishResponse,
  TaskDetail,
} from './protocolTypes';

/** Thrown when the bridge responds 401 (missing/invalid bearer token). */
export class BridgeAuthError extends Error {
  constructor(message = 'HarnessMesh bridge rejected the configured token (401)') {
    super(message);
    this.name = 'BridgeAuthError';
  }
}

/** Thrown when the bridge process cannot be reached at all (connection refused, DNS, etc). */
export class BridgeUnreachableError extends Error {
  constructor(cause: unknown, url: string) {
    super(`HarnessMesh bridge unreachable at ${url}: ${(cause as Error)?.message ?? String(cause)}`);
    this.name = 'BridgeUnreachableError';
  }
}

/** Thrown for any other non-2xx bridge response. */
export class BridgeRequestError extends Error {
  constructor(public status: number, public body: string) {
    super(`HarnessMesh bridge request failed (${status}): ${body}`);
    this.name = 'BridgeRequestError';
  }
}

export type ConnectionState = 'disconnected' | 'connecting' | 'connected';

const RECONNECT_BASE_DELAY_MS = 1000;
const RECONNECT_MAX_DELAY_MS = 30000;
const SEEN_EVENT_ID_CAPACITY = 512;

/**
 * BridgeClient is a plain Node HTTP + WebSocket client for the HarnessMesh
 * bridge (internal/bridge in the Go module). It deliberately runs in the
 * extension host process, not a webview, so there is no CORS/CSP surface to
 * manage: this process never sends a browser Origin header.
 *
 * Emits: 'stateChange' (ConnectionState), 'event' (BridgeEvent), 'error' (Error)
 */
export class BridgeClient extends EventEmitter {
  private token: string | undefined;
  private ws: WebSocket | undefined;
  private wsSpaceId: string | undefined;
  private reconnectTimer: NodeJS.Timeout | undefined;
  private reconnectAttempt = 0;
  private manualDisconnect = false;
  private state: ConnectionState = 'disconnected';

  // Dedup ring for event_id: the bridge replays a short backfill on
  // reconnect (internal/bridge/websocket.go: recentEvents), so a naive
  // reconnect can hand tree views the same event twice. Keep a small
  // bounded set of recently-seen event ids and drop repeats.
  private seenEventIds: Set<string> = new Set();
  private seenEventOrder: string[] = [];

  constructor(private bridgeUrl: string) {
    super();
  }

  setBridgeUrl(url: string): void {
    this.bridgeUrl = url;
  }

  setToken(token: string | undefined): void {
    this.token = token;
  }

  getState(): ConnectionState {
    return this.state;
  }

  private setState(next: ConnectionState): void {
    if (this.state !== next) {
      this.state = next;
      this.emit('stateChange', next);
    }
  }

  /** GET /healthz - unauthenticated liveness check, used before connecting. */
  async healthCheck(): Promise<boolean> {
    try {
      await this.rawRequest('GET', '/healthz', undefined, false);
      return true;
    } catch {
      return false;
    }
  }

  // --- REST API ---

  listWorkspaces(): Promise<CollaborationSpace[]> {
    return this.request<{ workspaces: CollaborationSpace[] }>('GET', '/api/v1/workspaces').then(
      (r) => r.workspaces ?? []
    );
  }

  getWorkspace(id: string): Promise<CollaborationSpace> {
    return this.request<CollaborationSpace>('GET', `/api/v1/workspaces/${encodeURIComponent(id)}`);
  }

  getWorkspaceStatus(id: string): Promise<CollaborationStatusResponse> {
    return this.request<CollaborationStatusResponse>(
      'GET',
      `/api/v1/workspaces/${encodeURIComponent(id)}/status`
    );
  }

  getInbox(spaceId: string, participant?: string): Promise<AgentInbox> {
    const q = new URLSearchParams({ space_id: spaceId });
    if (participant) {
      q.set('participant', participant);
    }
    return this.request<AgentInbox>('GET', `/api/v1/inbox?${q.toString()}`);
  }

  postMessage(req: PublishRequest): Promise<PublishResponse> {
    return this.request<PublishResponse>('POST', '/api/v1/messages', req);
  }

  listTasks(spaceId?: string, status?: string): Promise<MeshChange[]> {
    const q = new URLSearchParams();
    if (spaceId) q.set('space_id', spaceId);
    if (status) q.set('status', status);
    const qs = q.toString();
    return this.request<{ tasks: MeshChange[] }>('GET', `/api/v1/tasks${qs ? `?${qs}` : ''}`).then(
      (r) => r.tasks ?? []
    );
  }

  createTask(req: CreateChangeRequest): Promise<MeshChange> {
    return this.request<MeshChange>('POST', '/api/v1/tasks', req);
  }

  getTask(id: string): Promise<TaskDetail> {
    return this.request<TaskDetail>('GET', `/api/v1/tasks/${encodeURIComponent(id)}`);
  }

  patchTask(id: string, action: 'prepare' | 'abort', reason?: string): Promise<MeshChange> {
    return this.request<MeshChange>('PATCH', `/api/v1/tasks/${encodeURIComponent(id)}`, {
      action,
      reason,
    });
  }

  listArtifacts(sessionId: string): Promise<EvidencePayload[]> {
    const q = new URLSearchParams({ session_id: sessionId });
    return this.request<{ artifacts: EvidencePayload[] }>('GET', `/api/v1/artifacts?${q.toString()}`).then(
      (r) => r.artifacts ?? []
    );
  }

  postArtifact(sessionId: string, payload: Partial<EvidencePayload>): Promise<EvidencePayload> {
    return this.request<EvidencePayload>('POST', '/api/v1/artifacts', {
      session_id: sessionId,
      ...payload,
    });
  }

  /** Findings and reviews are the same bridge resource - see protocolTypes.ts. */
  listFindings(sessionId: string): Promise<FindingPayload[]> {
    const q = new URLSearchParams({ session_id: sessionId });
    return this.request<{ findings: FindingPayload[] }>('GET', `/api/v1/findings?${q.toString()}`).then(
      (r) => r.findings ?? []
    );
  }

  postFinding(sessionId: string, payload: Partial<FindingPayload>): Promise<FindingPayload> {
    return this.request<FindingPayload>('POST', '/api/v1/findings', {
      session_id: sessionId,
      ...payload,
    });
  }

  pollEvents(limit = 100): Promise<BridgeEvent[]> {
    return this.request<{ events: BridgeEvent[] }>('GET', `/api/v1/events?limit=${limit}`).then(
      (r) => r.events ?? []
    );
  }

  // --- WebSocket streaming ---

  connectWebSocket(spaceId?: string): void {
    this.manualDisconnect = false;
    this.wsSpaceId = spaceId;
    this.reconnectAttempt = 0;
    this.openSocket();
  }

  disconnectWebSocket(): void {
    this.manualDisconnect = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
    }
    if (this.ws) {
      this.ws.removeAllListeners();
      try {
        this.ws.close();
      } catch {
        // ignore
      }
      this.ws = undefined;
    }
    this.setState('disconnected');
  }

  private openSocket(): void {
    if (!this.token) {
      this.emit('error', new BridgeAuthError('cannot connect: no bridge token configured'));
      this.setState('disconnected');
      return;
    }
    this.setState('connecting');

    const url = new URL(this.bridgeUrl);
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    url.pathname = joinPath(url.pathname, '/api/v1/events/ws');
    if (this.wsSpaceId) {
      url.searchParams.set('space_id', this.wsSpaceId);
    }

    // Node's `ws` client can set arbitrary headers (unlike a browser), so we
    // prefer the Authorization header over the ?token= query fallback.
    const socket = new WebSocket(url.toString(), {
      headers: { Authorization: `Bearer ${this.token}` },
    });
    this.ws = socket;

    socket.on('open', () => {
      this.reconnectAttempt = 0;
      this.setState('connected');
    });

    socket.on('message', (data: WebSocket.RawData) => {
      let env: BridgeEvent;
      try {
        env = JSON.parse(data.toString());
      } catch (err) {
        this.emit('error', new Error(`failed to parse bridge event envelope: ${String(err)}`));
        return;
      }
      if (env.protocol !== BRIDGE_PROTOCOL_VERSION) {
        // Unrecognized protocol version: ignore rather than fail, per
        // internal/bridge/websocket.go's documented contract.
        return;
      }
      if (this.isDuplicateEvent(env.event_id)) {
        return;
      }
      this.emit('event', env);
    });

    socket.on('close', (code: number) => {
      this.ws = undefined;
      this.setState('disconnected');
      if (!this.manualDisconnect) {
        this.scheduleReconnect();
      }
      void code;
    });

    socket.on('error', (err: Error) => {
      this.emit('error', err);
      // 'close' will typically follow and trigger reconnect logic.
    });
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) {
      return;
    }
    const delay = Math.min(
      RECONNECT_MAX_DELAY_MS,
      RECONNECT_BASE_DELAY_MS * 2 ** this.reconnectAttempt
    );
    this.reconnectAttempt += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (!this.manualDisconnect) {
        this.openSocket();
      }
    }, delay);
  }

  private isDuplicateEvent(eventId: string): boolean {
    if (!eventId) {
      return false;
    }
    if (this.seenEventIds.has(eventId)) {
      return true;
    }
    this.seenEventIds.add(eventId);
    this.seenEventOrder.push(eventId);
    if (this.seenEventOrder.length > SEEN_EVENT_ID_CAPACITY) {
      const evicted = this.seenEventOrder.shift();
      if (evicted !== undefined) {
        this.seenEventIds.delete(evicted);
      }
    }
    return false;
  }

  // --- HTTP plumbing ---

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    if (!this.token) {
      throw new BridgeAuthError('no bridge token configured; run "HarnessMesh: Set Bridge Token"');
    }
    const { status, text } = await this.rawRequest(method, path, body, true);
    if (status === 401) {
      throw new BridgeAuthError();
    }
    if (status < 200 || status >= 300) {
      throw new BridgeRequestError(status, text);
    }
    if (!text) {
      return undefined as unknown as T;
    }
    return JSON.parse(text) as T;
  }

  private rawRequest(
    method: string,
    path: string,
    body: unknown,
    authorize: boolean
  ): Promise<{ status: number; text: string }> {
    return new Promise((resolve, reject) => {
      let target: URL;
      try {
        // `path` always starts with "/", so resolving it against bridgeUrl
        // replaces the base's path entirely (per WHATWG URL semantics) -
        // this is simpler and safer than manually concatenating strings.
        target = new URL(path, this.bridgeUrl);
      } catch (err) {
        reject(err);
        return;
      }
      const transport = target.protocol === 'https:' ? https : http;
      const payload = body !== undefined ? JSON.stringify(body) : undefined;
      const headers: Record<string, string> = {};
      if (payload) {
        headers['Content-Type'] = 'application/json';
        headers['Content-Length'] = Buffer.byteLength(payload).toString();
      }
      if (authorize && this.token) {
        headers['Authorization'] = `Bearer ${this.token}`;
      }

      const req = transport.request(
        target,
        { method, headers },
        (res: http.IncomingMessage) => {
          const chunks: Buffer[] = [];
          res.on('data', (c: Buffer) => chunks.push(c));
          res.on('end', () => {
            resolve({ status: res.statusCode ?? 0, text: Buffer.concat(chunks).toString('utf8') });
          });
        }
      );
      req.on('error', (err: Error) => {
        reject(new BridgeUnreachableError(err, this.bridgeUrl));
      });
      if (payload) {
        req.write(payload);
      }
      req.end();
    });
  }
}

function joinPath(base: string, addition: string): string {
  const trimmedBase = base.endsWith('/') ? base.slice(0, -1) : base;
  const trimmedAddition = addition.startsWith('/') ? addition : `/${addition}`;
  return `${trimmedBase}${trimmedAddition}`;
}
