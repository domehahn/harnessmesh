import * as vscode from 'vscode';
import { BridgeClient } from './bridgeClient';
import { FindingPayload, MeshChange, SpaceParticipant } from './protocolTypes';

/** Supplies the tree providers with the currently selected workspace/session,
 * without coupling them to extension.ts's global state shape. */
export interface Selection {
  spaceId(): string | undefined;
  sessionId(): string | undefined;
}

abstract class BaseTreeProvider<T> implements vscode.TreeDataProvider<T> {
  protected readonly emitter = new vscode.EventEmitter<T | undefined | void>();
  readonly onDidChangeTreeData: vscode.Event<T | undefined | void> = this.emitter.event;

  constructor(protected client: BridgeClient, protected selection: Selection) {}

  refresh(): void {
    this.emitter.fire();
  }

  abstract getTreeItem(element: T): vscode.TreeItem;
  abstract getChildren(element?: T): vscode.ProviderResult<T[]>;
}

// --- Participants ---

export class ParticipantsProvider extends BaseTreeProvider<SpaceParticipant> {
  getTreeItem(p: SpaceParticipant): vscode.TreeItem {
    const item = new vscode.TreeItem(p.id, vscode.TreeItemCollapsibleState.None);
    item.description = `${p.mode}${p.writable ? ' · writable' : ''}`;
    item.tooltip = `adapter: ${p.adapter}\nroles: ${p.roles?.join(', ') || '-'}\ncapabilities: ${
      p.capabilities?.join(', ') || '-'
    }`;
    item.iconPath = new vscode.ThemeIcon(p.writable ? 'account' : 'person');
    item.contextValue = 'harnessmesh.participant';
    return item;
  }

  async getChildren(): Promise<SpaceParticipant[]> {
    const spaceId = this.selection.spaceId();
    if (!spaceId) {
      return [];
    }
    const space = await this.client.getWorkspace(spaceId);
    return Object.values(space.participants ?? {});
  }
}

// --- Tasks (MeshCommit change transactions) ---

export class TasksProvider extends BaseTreeProvider<MeshChange> {
  getTreeItem(t: MeshChange): vscode.TreeItem {
    const item = new vscode.TreeItem(t.title || t.id, vscode.TreeItemCollapsibleState.None);
    item.description = t.status;
    item.tooltip = `id: ${t.id}\nauthor: ${t.author_participant}\nintent: ${t.intent}\nstatus: ${t.status}`;
    item.iconPath = new vscode.ThemeIcon(iconForTaskStatus(t.status));
    item.contextValue = 'harnessmesh.task';
    return item;
  }

  async getChildren(): Promise<MeshChange[]> {
    const spaceId = this.selection.spaceId();
    return this.client.listTasks(spaceId);
  }
}

function iconForTaskStatus(status: MeshChange['status']): string {
  switch (status) {
    case 'committed':
      return 'check';
    case 'aborted':
      return 'close';
    case 'blocked':
      return 'error';
    case 'committable':
    case 'verified':
      return 'check-all';
    case 'under_verification':
      return 'sync';
    case 'prepared':
      return 'symbol-event';
    case 'draft':
    default:
      return 'edit';
  }
}

// --- Findings / Reviews ---
// GET /api/v1/reviews is served by the same handler as GET /api/v1/findings
// (internal/bridge/bridge.go: "reviews == findings"); this class backs both
// tree views. See extensions/vscode/README.md for the mapping.

export class FindingsProvider extends BaseTreeProvider<FindingPayload> {
  constructor(client: BridgeClient, selection: Selection, private severityFilter?: (f: FindingPayload) => boolean) {
    super(client, selection);
  }

  getTreeItem(f: FindingPayload): vscode.TreeItem {
    const item = new vscode.TreeItem(f.claim || f.id, vscode.TreeItemCollapsibleState.None);
    item.description = `${f.severity} · ${f.status}`;
    item.tooltip = `category: ${f.category}\nrecommendation: ${f.recommendation}\nfile: ${f.file ?? '-'}:${
      f.line ?? ''
    }`;
    item.iconPath = new vscode.ThemeIcon(iconForSeverity(f.severity));
    item.contextValue = 'harnessmesh.finding';
    return item;
  }

  async getChildren(): Promise<FindingPayload[]> {
    const sessionId = this.selection.sessionId();
    if (!sessionId) {
      return [];
    }
    const findings = await this.client.listFindings(sessionId);
    return this.severityFilter ? findings.filter(this.severityFilter) : findings;
  }
}

function iconForSeverity(severity: string): string {
  switch (severity) {
    case 'critical':
    case 'high':
      return 'error';
    case 'medium':
      return 'warning';
    case 'low':
    case 'info':
    default:
      return 'info';
  }
}
