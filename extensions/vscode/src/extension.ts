import * as vscode from 'vscode';
import {
  BridgeAuthError,
  BridgeClient,
  BridgeRequestError,
  BridgeUnreachableError,
} from './bridgeClient';
import { BridgeEvent, CollaborationSpace, EventType } from './protocolTypes';
import { HarnessMeshStatusBar } from './statusBar';
import { FindingsProvider, ParticipantsProvider, Selection, TasksProvider } from './treeViews';

const SECRET_TOKEN_KEY = 'harnessmesh.token';

// Event types that should cause the tree views to refresh. Kept as an
// allowlist (rather than "refresh on everything") so an unrelated event
// storm on a busy space doesn't thrash the UI.
const REFRESH_EVENT_TYPES = new Set<string>([
  EventType.ParticipantJoined,
  EventType.ParticipantLeft,
  EventType.ParticipantFailed,
  EventType.ChangeCreated,
  EventType.ChangePrepared,
  EventType.ChangeUpdated,
  EventType.ChangeAborted,
  EventType.ChangeCommittable,
  EventType.ChangeCommitted,
  EventType.FindingCreated,
  EventType.FindingUpdated,
  EventType.FindingResolved,
  EventType.ReviewRequested,
  EventType.ReviewCompleted,
  EventType.EvidenceCreated,
  EventType.EvidenceAttached,
  EventType.EvidenceInvalidated,
]);

export function activate(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel('HarnessMesh');
  context.subscriptions.push(output);

  const client = new BridgeClient(bridgeUrlSetting());
  const statusBar = new HarnessMeshStatusBar();
  context.subscriptions.push(statusBar);

  let currentSpaceId: string | undefined;
  let currentSessionId: string | undefined;

  const selection: Selection = {
    spaceId: () => currentSpaceId,
    sessionId: () => currentSessionId ?? currentSpaceId,
  };

  const participantsProvider = new ParticipantsProvider(client, selection);
  const tasksProvider = new TasksProvider(client, selection);
  const reviewsProvider = new FindingsProvider(client, selection);
  const findingsProvider = new FindingsProvider(client, selection);

  context.subscriptions.push(
    vscode.window.registerTreeDataProvider('harnessmesh.participants', participantsProvider),
    vscode.window.registerTreeDataProvider('harnessmesh.tasks', tasksProvider),
    vscode.window.registerTreeDataProvider('harnessmesh.reviews', reviewsProvider),
    vscode.window.registerTreeDataProvider('harnessmesh.findings', findingsProvider)
  );

  function refreshAllTrees(): void {
    participantsProvider.refresh();
    tasksProvider.refresh();
    reviewsProvider.refresh();
    findingsProvider.refresh();
  }

  vscode.workspace.onDidChangeConfiguration(
    (e) => {
      if (e.affectsConfiguration('harnessmesh.bridgeUrl')) {
        client.setBridgeUrl(bridgeUrlSetting());
      }
    },
    undefined,
    context.subscriptions
  );

  client.on('stateChange', (state) => {
    statusBar.setState(state);
    if (state === 'connected') {
      refreshAllTrees();
    }
  });

  client.on('event', (env: BridgeEvent) => {
    output.appendLine(`[event] ${env.type} workspace=${env.workspace_id ?? ''} actor=${env.actor ?? ''}`);
    if (REFRESH_EVENT_TYPES.has(env.type)) {
      refreshAllTrees();
    }
  });

  client.on('error', (err: Error) => {
    output.appendLine(`[error] ${err.message}`);
    if (err instanceof BridgeAuthError) {
      void handleAuthFailure(context, client, output);
    }
  });

  async function loadStoredToken(): Promise<string | undefined> {
    const token = await context.secrets.get(SECRET_TOKEN_KEY);
    client.setToken(token);
    return token;
  }

  async function promptAndStoreToken(): Promise<string | undefined> {
    const token = await vscode.window.showInputBox({
      title: 'HarnessMesh Bridge Token',
      prompt: 'Enter the bearer token used to start the bridge (harnessmesh bridge serve --token ...)',
      password: true,
      ignoreFocusOut: true,
    });
    if (!token) {
      return undefined;
    }
    await context.secrets.store(SECRET_TOKEN_KEY, token);
    client.setToken(token);
    return token;
  }

  async function connect(): Promise<void> {
    let token = await loadStoredToken();
    if (!token) {
      token = await promptAndStoreToken();
    }
    if (!token) {
      void vscode.window.showWarningMessage('HarnessMesh: a bridge token is required to connect.');
      return;
    }

    const reachable = await client.healthCheck();
    if (!reachable) {
      void vscode.window.showErrorMessage(
        `HarnessMesh: bridge unreachable at ${bridgeUrlSetting()}. Is "harnessmesh bridge serve" running?`
      );
      output.appendLine(`[error] bridge unreachable at ${bridgeUrlSetting()}`);
      return;
    }

    try {
      await client.listWorkspaces();
    } catch (err) {
      handleRequestError(err, output);
      if (err instanceof BridgeAuthError) {
        await handleAuthFailure(context, client, output);
      }
      return;
    }

    client.connectWebSocket(currentSpaceId);
    output.appendLine('[info] connecting to bridge…');
  }

  function disconnect(): void {
    client.disconnectWebSocket();
    output.appendLine('[info] disconnected from bridge');
  }

  async function selectWorkspace(): Promise<void> {
    let spaces: CollaborationSpace[];
    try {
      spaces = await client.listWorkspaces();
    } catch (err) {
      handleRequestError(err, output);
      return;
    }
    if (spaces.length === 0) {
      void vscode.window.showInformationMessage('HarnessMesh: no workspaces (collaboration spaces) found.');
      return;
    }
    const picked = await vscode.window.showQuickPick(
      spaces.map((s) => ({
        label: s.title || s.id,
        description: s.id,
        detail: `${s.lifecycle_state} · writer: ${s.writer_participant}`,
        space: s,
      })),
      { title: 'Select a HarnessMesh workspace (collaboration space)', ignoreFocusOut: true }
    );
    if (!picked) {
      return;
    }
    currentSpaceId = picked.space.id;

    // HarnessMesh's findings/artifacts endpoints are scoped by session_id,
    // not space_id, and the bridge has no "list sessions for a space"
    // endpoint. We default the session id to the space id (a common 1:1
    // setup - see bridge_test.go) but let the user override it, since a
    // real deployment may run several sessions against one space.
    const sessionInput = await vscode.window.showInputBox({
      title: 'Session ID for findings/artifacts (optional)',
      prompt: 'Leave blank to use the workspace ID as the session ID',
      ignoreFocusOut: true,
    });
    currentSessionId = sessionInput || undefined;

    if (client.getState() === 'connected') {
      client.disconnectWebSocket();
      client.connectWebSocket(currentSpaceId);
    }
    refreshAllTrees();
    output.appendLine(`[info] selected workspace ${currentSpaceId} (session ${selection.sessionId()})`);
  }

  async function openCollaboration(): Promise<void> {
    if (!currentSpaceId) {
      void vscode.window.showWarningMessage('HarnessMesh: select a workspace first.');
      return;
    }
    try {
      const status = await client.getWorkspaceStatus(currentSpaceId);
      output.appendLine('--- Collaboration status ---');
      output.appendLine(JSON.stringify(status, null, 2));
      output.show(true);
    } catch (err) {
      handleRequestError(err, output);
    }
  }

  async function showInbox(): Promise<void> {
    if (!currentSpaceId) {
      void vscode.window.showWarningMessage('HarnessMesh: select a workspace first.');
      return;
    }
    try {
      const inbox = await client.getInbox(currentSpaceId);
      const items = inbox.items.map(
        (i) => `${i.requires_response ? '[!]' : '   '} ${i.from} → ${i.summary}`
      );
      output.appendLine(`--- Inbox (${inbox.unread_count} unread) ---`);
      items.forEach((i) => output.appendLine(i));
      output.show(true);
    } catch (err) {
      handleRequestError(err, output);
    }
  }

  function revealView(viewId: string): void {
    // Native VS Code UI only (per project guidance: no custom webview chat
    // client) - showing a tree view means focusing it in the sidebar.
    void vscode.commands.executeCommand(`${viewId}.focus`);
  }

  async function sendMessage(): Promise<void> {
    if (!currentSpaceId) {
      void vscode.window.showWarningMessage('HarnessMesh: select a workspace first.');
      return;
    }
    const channelId = await vscode.window.showInputBox({
      title: 'Channel ID',
      value: 'general',
      ignoreFocusOut: true,
    });
    if (!channelId) {
      return;
    }
    const message = await vscode.window.showInputBox({
      title: 'Message',
      prompt: `Send a message to #${channelId}`,
      ignoreFocusOut: true,
    });
    if (!message) {
      return;
    }
    try {
      await client.postMessage({ space_id: currentSpaceId, channel_id: channelId, message });
      void vscode.window.showInformationMessage('HarnessMesh: message sent.');
    } catch (err) {
      handleRequestError(err, output);
    }
  }

  context.subscriptions.push(
    vscode.commands.registerCommand('harnessmesh.connect', () => void connect()),
    vscode.commands.registerCommand('harnessmesh.disconnect', () => disconnect()),
    vscode.commands.registerCommand('harnessmesh.selectWorkspace', () => void selectWorkspace()),
    vscode.commands.registerCommand('harnessmesh.openCollaboration', () => void openCollaboration()),
    vscode.commands.registerCommand('harnessmesh.showInbox', () => void showInbox()),
    vscode.commands.registerCommand('harnessmesh.showTasks', () => revealView('harnessmesh.tasks')),
    vscode.commands.registerCommand('harnessmesh.showReviews', () => revealView('harnessmesh.reviews')),
    vscode.commands.registerCommand('harnessmesh.showFindings', () => revealView('harnessmesh.findings')),
    vscode.commands.registerCommand('harnessmesh.sendMessage', () => void sendMessage()),
    vscode.commands.registerCommand('harnessmesh.setToken', () => void promptAndStoreToken()),
    vscode.commands.registerCommand('harnessmesh.refreshTrees', () => refreshAllTrees())
  );

  context.subscriptions.push({ dispose: () => client.disconnectWebSocket() });

  void loadStoredToken().then((token) => {
    if (token && autoConnectSetting()) {
      void connect();
    }
  });
}

export function deactivate(): void {
  // Cleanup is handled via context.subscriptions disposables registered in
  // activate(); nothing else to do here.
}

function bridgeUrlSetting(): string {
  return vscode.workspace.getConfiguration('harnessmesh').get<string>('bridgeUrl', 'http://127.0.0.1:8788');
}

function autoConnectSetting(): boolean {
  return vscode.workspace.getConfiguration('harnessmesh').get<boolean>('autoConnect', false);
}

async function handleAuthFailure(
  context: vscode.ExtensionContext,
  client: BridgeClient,
  output: vscode.OutputChannel
): Promise<void> {
  output.appendLine('[error] bridge rejected the token (401) - prompting to re-enter it');
  const choice = await vscode.window.showErrorMessage(
    'HarnessMesh: the bridge rejected the stored token. Re-enter it?',
    'Re-enter token'
  );
  if (choice === 'Re-enter token') {
    const token = await vscode.window.showInputBox({
      title: 'HarnessMesh Bridge Token',
      password: true,
      ignoreFocusOut: true,
    });
    if (token) {
      await context.secrets.store(SECRET_TOKEN_KEY, token);
      client.setToken(token);
    }
  }
}

function handleRequestError(err: unknown, output: vscode.OutputChannel): void {
  if (err instanceof BridgeAuthError) {
    void vscode.window.showErrorMessage('HarnessMesh: unauthorized (401). Check your bridge token.');
  } else if (err instanceof BridgeUnreachableError) {
    void vscode.window.showErrorMessage(`HarnessMesh: bridge unreachable. ${err.message}`);
  } else if (err instanceof BridgeRequestError) {
    void vscode.window.showErrorMessage(`HarnessMesh: request failed (${err.status}).`);
  } else {
    void vscode.window.showErrorMessage(`HarnessMesh: unexpected error: ${(err as Error)?.message ?? err}`);
  }
  output.appendLine(`[error] ${(err as Error)?.message ?? String(err)}`);
}
