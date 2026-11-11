package knowledge

import (
	"testing"
	"time"
)

func TestCognifyExtractsEntitiesAndSubgraph(t *testing.T) {
	records := []Record{
		{
			ID:         "rec-1",
			Timestamp:  time.Now().UTC(),
			Kind:       "decision",
			From:       "claude",
			To:         "codex",
			Text:       "Approved security boundary in internal/mcp/server.go with @openhands; password=\"supersecretvalue\"",
			Tags:       []string{"security", "mcp"},
			Confidence: 0.95,
			Verified:   true,
		},
		{
			ID:         "rec-2",
			Timestamp:  time.Now().UTC(),
			Kind:       "finding",
			From:       "codex",
			Text:       "Follow-up verification on internal/mcp/server.go and sdk/python/harnessmesh/__init__.py",
			Tags:       []string{"security"},
			Supersedes: "rec-1",
			Confidence: 0.9,
			Verified:   true,
		},
	}

	g := Cognify(records)
	if g.Summary["agents"] < 3 {
		t.Fatalf("expected at least 3 agents (claude, codex, openhands), got %+v", g.Summary)
	}
	if g.Summary["files"] != 2 {
		t.Fatalf("expected 2 file nodes, got %+v", g.Summary)
	}
	if g.Summary["topics"] != 2 {
		t.Fatalf("expected 2 topic nodes, got %+v", g.Summary)
	}
	for _, n := range g.Nodes {
		if n.ID == "record:rec-1" && n.Label == "" {
			t.Fatal("expected non-empty redacted label")
		}
		if n.Label != "" && containsSubstr(n.Label, "supersecretvalue") {
			t.Fatalf("expected secret to be redacted in graph label: %q", n.Label)
		}
	}

	sub := Subgraph(g, "file:internal/mcp/server.go", 1)
	if sub.Summary["files"] != 1 || sub.Summary["edges"] == 0 {
		t.Fatalf("expected non-empty 1-hop subgraph around file:internal/mcp/server.go, got %+v", sub.Summary)
	}
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
