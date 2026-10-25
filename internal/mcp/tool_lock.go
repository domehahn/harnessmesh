package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/domehahn/harnessmesh/internal/assurance"
)

type ToolLock struct {
	Version int               `json:"version"`
	Tools   map[string]string `json:"tools"`
}

func toolDigests(tools []ToolDefinition) (map[string]string, error) {
	digests := map[string]string{}
	for _, tool := range tools {
		if tool.Name == "" {
			return nil, errors.New("MCP tool name is required")
		}
		digest, err := assurance.Fingerprint(map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
		if err != nil {
			return nil, err
		}
		digests[tool.Name] = digest
	}
	return digests, nil
}

func GenerateToolLock(path string, tools []ToolDefinition) error {
	digests, err := toolDigests(tools)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(ToolLock{Version: 1, Tools: digests}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func VerifyToolLock(path string, tools []ToolDefinition) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var lock ToolLock
	if err := json.Unmarshal(raw, &lock); err != nil || lock.Version != 1 {
		return errors.New("invalid MCP tool lock")
	}
	actual, err := toolDigests(tools)
	if err != nil {
		return err
	}
	if len(actual) != len(lock.Tools) {
		return fmt.Errorf("MCP tool lock drift: expected %d tools, found %d", len(lock.Tools), len(actual))
	}
	names := make([]string, 0, len(actual))
	for name := range actual {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if lock.Tools[name] != actual[name] {
			return fmt.Errorf("MCP tool lock drift for %q", name)
		}
	}
	return nil
}
