/**
 * Mirrors the wire shapes defined in the Go module at
 * internal/protocol/types.go and internal/bridge/{bridge,handlers,websocket}.go.
 *
 * This file intentionally only carries transport-shape types: HarnessMesh's
 * bridge never puts model-generated reasoning in these payloads, only
 * collaboration-plane state (messages, MeshCommit change transactions,
 * findings/evidence, and events). Field names and JSON tags below match the
 * Go structs exactly - keep them in sync if the Go side changes.
 */

// --- MeshCommit change transactions ("tasks" in the bridge API) ---
// See internal/protocol/types.go: MeshChangeStatus, MeshChange.
export type MeshChangeStatus =
  | 'draft'
  | 'prepared'
  | 'under_verification'
  | 'blocked'
  | 'verified'
  | 'committable'
  | 'committed'
  | 'aborted';

export interface MeshChange {
  id: string;
  session_id?: string;
  space_id?: string;
  repository_id: string;
  title: string;
  intent: string;
  author_participant: string;
  base_commit: string;
  base_tree_hash: string;
  current_tree_hash: string;
  verified_tree_hash?: string;
  status: MeshChangeStatus;
  proof_policy_json: string;
  policy_source: string;
  commit_sha?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  committed_at?: string | null;
  aborted_at?: string | null;
}

export interface CreateChangeRequest {
  change_id?: string;
  space_id?: string;
  session_id?: string;
  repository_id?: string;
  author_participant?: string;
  title: string;
  intent?: string;
  base_commit?: string;
}

export interface ChangePath {
  change_id: string;
  path: string;
  change_type: 'added' | 'modified' | 'deleted' | 'renamed' | string;
  content_hash_before?: string;
  content_hash_after?: string;
  created_at: string;
}

export type ProofObligationStatus =
  | 'pending'
  | 'running'
  | 'passed'
  | 'failed'
  | 'stale'
  | 'waived'
  | 'requires_human';

export interface ProofObligation {
  id: string;
  change_id: string;
  type: string;
  name: string;
  description: string;
  required: boolean;
  status: ProofObligationStatus;
  policy_source: string;
  scope?: string[];
  required_capability?: string;
  required_participant?: string;
  command?: string;
  expected_exit_code: number;
  freshness_policy: string;
  current_evidence_id?: string;
  waived_by?: string;
  waived_reason?: string;
  created_at: string;
  updated_at: string;
}

export interface TaskDetail {
  task: MeshChange;
  paths: ChangePath[] | null;
  obligations: ProofObligation[] | null;
}

// --- findings / reviews (GET /api/v1/findings and the aliased
// GET /api/v1/reviews both serve this same shape - see
// internal/bridge/bridge.go: "reviews == findings") ---
export type FindingStatus =
  | 'open'
  | 'acknowledged'
  | 'disputed'
  | 'resolved'
  | 'dismissed'
  | 'superseded';

export interface FindingPayload {
  id: string;
  session_id?: string;
  source_agent?: string;
  source_participant?: string;
  source_adapter?: string;
  timestamp: string;
  severity: 'info' | 'low' | 'medium' | 'high' | 'critical' | string;
  category: string;
  claim: string;
  evidence?: string;
  evidence_refs?: string[];
  recommendation: string;
  file?: string;
  line?: number;
  line_start?: number;
  end_line?: number;
  line_end?: number;
  status: FindingStatus;
  created_at?: string;
  updated_at?: string;
  duplicate_of?: string;
  related_findings?: string[];
}

// --- evidence ("artifacts" in the bridge API) ---
export type EvidenceType = string;

export interface EvidencePayload {
  id: string;
  finding_id?: string;
  message_id?: string;
  source_agent: string;
  type: EvidenceType;
  command?: string;
  result?: string;
  excerpt?: string;
  exit_code?: number;
  metadata?: Record<string, unknown>;
  created_at: string;
}

// --- collaboration spaces ("workspaces" in the bridge API) ---
export type SpaceLifecycleState = 'active' | 'paused' | 'stopped' | 'archived';
export type ParticipantActivityMode = 'active' | 'passive' | 'on_demand' | 'paused';

export interface SpaceParticipant {
  id: string;
  adapter: string;
  execution_mode?: string;
  roles: string[];
  capabilities: string[];
  mode: ParticipantActivityMode;
  writable: boolean;
  joined_at: string;
  updated_at: string;
}

export interface Channel {
  id: string;
  space_id: string;
  name: string;
  description: string;
  visibility: string;
  allowed_participants?: string[];
  allowed_capabilities?: string[];
  created_by: string;
  created_at: string;
  archived_at?: string | null;
}

export interface BudgetStatus {
  known: boolean;
  strict_token_ceiling?: boolean;
  max_input_tokens?: number;
  used_input_tokens?: number;
  max_output_tokens?: number;
  used_output_tokens?: number;
  max_total_tokens?: number;
  used_total_tokens?: number;
  max_cost_usd?: number;
  used_cost_usd?: number;
}

export interface CollaborationSpace {
  id: string;
  workspace_id: string;
  title: string;
  purpose: string;
  lifecycle_state: SpaceLifecycleState;
  writer_participant: string;
  budget: BudgetStatus;
  participants: Record<string, SpaceParticipant>;
  channels: Record<string, Channel>;
  metadata?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface CollaborationStatusResponse {
  space_id: string;
  title: string;
  lifecycle_state: SpaceLifecycleState;
  writer_participant: string;
  participants: Record<string, SpaceParticipant>;
  channels: string[];
  open_findings: number;
  disputed_findings: number;
  resolved_findings: number;
  decisions_count: number;
  total_messages: number;
  remaining_peer_rounds: number;
}

// --- inbox ---
export interface InboxItem {
  id: string;
  space_id: string;
  channel_id: string;
  thread_id?: string;
  from: string;
  type: string;
  summary: string;
  created_at: string;
  requires_response: boolean;
  mentions_me: boolean;
  is_direct: boolean;
  payload?: unknown;
}

export interface AgentInbox {
  participant_id: string;
  space_id: string;
  unread_count: number;
  items: InboxItem[];
}

// --- messages ---
export interface PublishRequest {
  space_id?: string;
  channel_id?: string;
  channel?: string;
  thread_id?: string;
  from?: string;
  to?: string;
  type?: string;
  subject?: string;
  message?: string;
  payload?: unknown;
  mentions?: string[];
  reply_to?: string;
  scope?: string[];
  causation_id?: string;
  idempotency_key?: string;
  metadata?: Record<string, unknown>;
  priority?: number;
}

export interface PublishResponse {
  message_id: string;
  thread_id?: string;
  channel_id?: string;
  status: string;
  delivered_to?: string[];
  pending_inbox?: string[];
  active_runs?: string[];
}

// --- WebSocket event envelope (protocol "harnessmesh.bridge/v1") ---
// See internal/bridge/websocket.go: BridgeProtocolVersion, Event.
export const BRIDGE_PROTOCOL_VERSION = 'harnessmesh.bridge/v1';

export interface BridgeEvent {
  protocol: string;
  event_id: string;
  timestamp: string;
  workspace_id?: string;
  session_id?: string;
  type: string;
  actor?: string;
  correlation_id?: string;
  payload?: Record<string, unknown>;
}

// Event type strings the bridge can emit (internal/protocol/types.go).
// Not exhaustive of every constant in the Go source, but covers everything
// the tree views react to.
export const EventType = {
  MessageCreated: 'message.created',
  ThreadCreated: 'thread.created',
  ParticipantJoined: 'participant.joined',
  ParticipantLeft: 'participant.left',
  ParticipantFailed: 'participant.failed',
  ReviewRequested: 'review.requested',
  ReviewCompleted: 'review.completed',
  DecisionCreated: 'decision.created',
  FindingCreated: 'finding.created',
  FindingUpdated: 'finding.updated',
  FindingResolved: 'finding.resolved',
  EvidenceCreated: 'evidence.created',
  EvidenceAttached: 'evidence.attached',
  EvidenceInvalidated: 'evidence.invalidated',
  ChangeCreated: 'change.created',
  ChangePrepared: 'change.prepared',
  ChangeUpdated: 'change.updated',
  ChangeAborted: 'change.aborted',
  ChangeCommittable: 'change.committable',
  ChangeCommitted: 'change.committed',
  WorkspaceChanged: 'workspace.changed',
} as const;
