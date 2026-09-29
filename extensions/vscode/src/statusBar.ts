import * as vscode from 'vscode';
import { ConnectionState } from './bridgeClient';

/**
 * A single StatusBarItem reflecting the bridge connection state. Clicking it
 * runs harnessmesh.connect when disconnected, or harnessmesh.disconnect when
 * connected/connecting.
 */
export class HarnessMeshStatusBar implements vscode.Disposable {
  private readonly item: vscode.StatusBarItem;
  private state: ConnectionState = 'disconnected';

  constructor() {
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    this.render();
    this.item.show();
  }

  setState(state: ConnectionState): void {
    this.state = state;
    this.render();
  }

  private render(): void {
    switch (this.state) {
      case 'connected':
        this.item.text = '$(plug) HarnessMesh: Connected';
        this.item.tooltip = 'Connected to the HarnessMesh bridge. Click to disconnect.';
        this.item.command = 'harnessmesh.disconnect';
        this.item.backgroundColor = undefined;
        break;
      case 'connecting':
        this.item.text = '$(sync~spin) HarnessMesh: Connecting…';
        this.item.tooltip = 'Connecting to the HarnessMesh bridge…';
        this.item.command = 'harnessmesh.disconnect';
        this.item.backgroundColor = undefined;
        break;
      case 'disconnected':
      default:
        this.item.text = '$(debug-disconnect) HarnessMesh: Disconnected';
        this.item.tooltip = 'Not connected to the HarnessMesh bridge. Click to connect.';
        this.item.command = 'harnessmesh.connect';
        this.item.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
        break;
    }
  }

  dispose(): void {
    this.item.dispose();
  }
}
