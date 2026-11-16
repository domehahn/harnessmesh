package collaboration

import (
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

func TestValidateSpecAndGrillMeQuestions(t *testing.T) {
	incomplete := ValidateSpec(SpecContract{
		Title:  "Fix auth",
		Intent: "TODO maybe fix token check",
		Scope:  []string{"*"},
	})
	if incomplete.Ready || len(incomplete.GrillMeQuestions) < 5 {
		t.Fatalf("expected incomplete spec to fail and generate Grill-Me questions, got %+v", incomplete)
	}

	complete := ValidateSpec(SpecContract{
		Title:                "Enforce token expiry on MCP sessions",
		Intent:               "Reject expired session tokens in internal/mcp/server.go with deterministic HTTP 401.",
		NonGoals:             []string{"Do not refactor OAuth provider configuration"},
		Scope:                []string{"internal/mcp/server.go", "internal/mcp/server_test.go"},
		MaxChangedFiles:      2,
		MaxChangedLines:      80,
		NeverCut:             []string{"internal/creditguard/**", ".github/workflows/**"},
		VerificationCommands: []string{"go test -count=1 ./internal/mcp/..."},
	})
	if !complete.Ready || complete.Score != 7 || len(complete.GrillMeQuestions) != 0 {
		t.Fatalf("expected complete spec to pass all 7 rules, got %+v", complete)
	}
}

func TestDiffBudgetNeverCutCodeownersAndTrustScore(t *testing.T) {
	cc := config.ChangeControlConfig{
		MaxChangedFiles: 2,
		MaxChangedLines: 100,
		NeverTouchPaths: []string{".github/workflows/**", "internal/creditguard/**"},
	}
	violations := cc.CheckDiffBudgetAndNeverCut([]string{"internal/mcp/server.go", ".github/workflows/ci.yml", "docs/skills.md"}, 120)
	if len(violations) != 3 {
		t.Fatalf("expected 3 violations (files budget, lines budget, never-cut path), got %+v", violations)
	}

	entries := config.ParseCodeowners("# Comment\ninternal/mcp/** @sec-team @core-team\n*.md @docs-team\n")
	owners := config.ResolveCodeowners(entries, []string{"internal/mcp/server.go", "README.md"})
	if len(owners["internal/mcp/server.go"]) != 2 || owners["README.md"][0] != "@docs-team" {
		t.Fatalf("unexpected resolved codeowners: %+v", owners)
	}

	score, tier := ComputeChangeTrustScore(3, 3, 0, 2, 0)
	if score != 100 || tier != "fast-track" {
		t.Fatalf("expected 100 fast-track, got score=%d tier=%s", score, tier)
	}
	scoreBlocked, tierBlocked := ComputeChangeTrustScore(3, 3, 0, 2, 1)
	if scoreBlocked != 0 || tierBlocked != "human-escalation" {
		t.Fatalf("expected policy violation to force 0/human-escalation, got %d/%s", scoreBlocked, tierBlocked)
	}
}
