package config

import (
	"strings"
	"testing"
)

func TestParseV1Config(t *testing.T) {
	raw := []byte(`{
		"schema_version": 1,
		"workflow": {
			"executor": "claude",
			"reviewer": "codex",
			"max_rounds": 4
		},
		"agents": {
			"claude": {
				"kind": "claude"
			},
			"codex": {
				"kind": "codex"
			}
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if cfg.Version != 1 {
		t.Fatalf("expected version 1, got %d", cfg.Version)
	}
	if cfg.Collaboration.MaxPeerRounds != 4 {
		t.Fatalf("expected max_peer_rounds 4, got %d", cfg.Collaboration.MaxPeerRounds)
	}
	if cfg.Collaboration.MaxPeerDepth != 2 {
		t.Fatalf("expected max_peer_depth 2, got %d", cfg.Collaboration.MaxPeerDepth)
	}
	if !cfg.Agents["claude"].Writable {
		t.Fatal("expected claude executor to default to writable")
	}
	if cfg.Agents["codex"].Writable {
		t.Fatal("expected codex reviewer to be non-writable")
	}
}

func TestParseV2Config(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"collaboration": {
			"max_peer_rounds": 5,
			"max_peer_depth": 3,
			"max_peer_calls": 12,
			"session_timeout": "30m"
		},
		"context": {
			"max_context_chars": 80000,
			"allowed_paths": ["internal/**"]
		},
		"agents": {
			"claude": {
				"adapter": "claude-code",
				"role": "executor",
				"writable": true
			},
			"codex": {
				"adapter": "codex",
				"role": "reviewer",
				"writable": false
			}
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if cfg.Version != 2 {
		t.Fatalf("expected version 2, got %d", cfg.Version)
	}
	if cfg.Collaboration.MaxPeerDepth != 3 {
		t.Fatalf("expected max depth 3, got %d", cfg.Collaboration.MaxPeerDepth)
	}
	if cfg.Collaboration.SessionTimeoutDuration().Minutes() != 30 {
		t.Fatalf("expected 30m timeout, got %v", cfg.Collaboration.SessionTimeoutDuration())
	}
}

func TestSingleWriterConstraint(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {
			"claude": {
				"adapter": "claude-code",
				"writable": true
			},
			"codex": {
				"adapter": "codex",
				"writable": true
			}
		}
	}`)

	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected single-writer invariant violation error, got nil")
	}
	if !strings.Contains(err.Error(), "single-writer invariant violated") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestMigrateV1ToV2(t *testing.T) {
	v1Raw := []byte(`{
		"schema_version": 1,
		"workflow": {
			"executor": "claude",
			"reviewer": "codex"
		},
		"agents": {
			"claude": {
				"kind": "claude",
				"role": "executor"
			},
			"codex": {
				"kind": "codex",
				"role": "reviewer"
			}
		}
	}`)

	v2Bytes, err := MigrateV1ToV2(v1Raw)
	if err != nil {
		t.Fatalf("MigrateV1ToV2 failed: %v", err)
	}

	cfg, err := Parse(v2Bytes)
	if err != nil {
		t.Fatalf("failed to parse migrated config: %v", err)
	}

	if cfg.Version != 2 {
		t.Errorf("expected version 2, got %d", cfg.Version)
	}
	if !cfg.Workspace.SingleWriter {
		t.Errorf("expected single writer true")
	}
	if !cfg.Agents["codex"].HasRole("reviewer") {
		t.Errorf("expected codex to have reviewer role")
	}
}

func TestConfigV2RolesAndRouting(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {
			"main": {
				"adapter": "antigravity",
				"roles": ["executor"],
				"writable": true
			},
			"sec": {
				"adapter": "copilot-cli",
				"roles": ["security", "dependencies"],
				"writable": false
			}
		},
		"capability_routing": {
			"security": ["sec"]
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if !cfg.Agents["sec"].HasRole("security") {
		t.Errorf("expected sec to have security role")
	}
	if !cfg.Agents["sec"].HasRole("dependencies") {
		t.Errorf("expected sec to have dependencies role")
	}
	if len(cfg.CapabilityRouting["security"]) != 1 || cfg.CapabilityRouting["security"][0] != "sec" {
		t.Errorf("expected capability_routing security to map to [sec]")
	}
}
