package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Scenario F: Merge-safe Claude Code .mcp.json installation.
func TestScenarioF_MergeSafeClaudeMCPInstall(t *testing.T) {
	tmpDir := t.TempDir()
	mcpPath := filepath.Join(tmpDir, ".mcp.json")

	// Pre-populate .mcp.json with an existing custom MCP server and settings
	initialConfig := map[string]any{
		"mcpServers": map[string]any{
			"existing-server": map[string]any{
				"command": "custom-binary",
				"args":    []any{"--verbose"},
			},
		},
		"userPreference": "keep-me",
	}
	raw, err := json.MarshalIndent(initialConfig, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	// Run installClaudeMCP in project scope
	if err := installClaudeMCP("project", tmpDir); err != nil {
		t.Fatalf("installClaudeMCP failed: %v", err)
	}

	// Read back .mcp.json
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("failed to read back .mcp.json: %v", err)
	}

	var resultConfig map[string]any
	if err := json.Unmarshal(data, &resultConfig); err != nil {
		t.Fatalf("failed to unmarshal .mcp.json: %v", err)
	}

	// 1. Verify custom preference preserved
	if resultConfig["userPreference"] != "keep-me" {
		t.Errorf("expected userPreference to be preserved, got: %v", resultConfig["userPreference"])
	}

	// 2. Verify existing server preserved
	mcpServers, ok := resultConfig["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers not found in resultConfig: %+v", resultConfig)
	}
	if _, exists := mcpServers["existing-server"]; !exists {
		t.Errorf("expected existing-server to be preserved in mcpServers")
	}

	// 3. Verify harnessmesh server added
	hmServer, exists := mcpServers["harnessmesh"].(map[string]any)
	if !exists {
		t.Fatalf("expected harnessmesh server in mcpServers")
	}
	if hmServer["command"] == "" {
		t.Errorf("expected non-empty command for harnessmesh")
	}
	args, ok := hmServer["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "mcp" || args[1] != "serve" {
		t.Errorf("expected args ['mcp', 'serve'], got: %+v", hmServer["args"])
	}
}

// Scenario G: CLI command testing (integrate chatgpt --dry-run).
func TestScenarioG_IntegrateChatGPTDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	configProfile := filepath.Join("..", "..", "configs", "chatgpt-claude.json")

	// 1. Dry run should not write any files
	argsDryRun := []string{
		"--dry-run",
		"--repo", tmpDir,
		"--config", configProfile,
	}
	if err := integrateChatGPT(argsDryRun); err != nil {
		t.Fatalf("integrateChatGPT dry-run failed: %v", err)
	}

	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Errorf("expected .mcp.json NOT to exist in dry-run mode, but found it")
	}

	// 2. Non-dry run should create .mcp.json
	argsLive := []string{
		"--dry-run=false",
		"--repo", tmpDir,
		"--config", configProfile,
	}
	if err := integrateChatGPT(argsLive); err != nil {
		t.Fatalf("integrateChatGPT non-dry-run failed: %v", err)
	}

	if _, err := os.Stat(mcpPath); err != nil {
		t.Errorf("expected .mcp.json to exist after non-dry-run, got error: %v", err)
	}
}
