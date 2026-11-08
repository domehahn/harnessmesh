package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/domehahn/harnessmesh/internal/skills"
)

func TestSelectedSkillNarrowsListAndCalls(t *testing.T) {
	s := NewServer(nil, "", "reviewer")
	s.SetSkill(&skills.Entry{Manifest: skills.Manifest{Name: "review", Tools: []string{"peer.list"}}})
	if s.SkillSnapshot()["execution_plan"] == nil {
		t.Fatal("expected selected skill to produce an execution plan")
	}
	tools := s.ListTools()
	if len(tools) != 1 || tools[0].Name != "peer.list" {
		t.Fatalf("unexpected skill tool projection: %#v", tools)
	}
	raw, _ := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: json.RawMessage(`{"name":"change.commit","arguments":{}}`)})
	response, err := s.HandleMessage(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32003 {
		t.Fatalf("expected skill denial, got %#v", response)
	}
}

func TestSkillSnapshotExposesEvaluation(t *testing.T) {
	registry := &skills.Registry{Entries: map[string]skills.Entry{
		"review": {Manifest: skills.Manifest{Name: "review", Version: "1.0.0", Tools: []string{"peer.list"}}},
	}}
	entry := registry.Entries["review"]
	s := NewServer(nil, "", "reviewer")
	s.SetSkillRegistry(registry)
	s.SetSkill(&entry)
	registry.ObserveOutcome("review", false)
	registry.ObserveOutcome("review", true)
	evaluation := registry.Evaluation("review")
	if evaluation.Runs != 2 || evaluation.Successful != 1 || evaluation.SuccessRate != 0.5 {
		t.Fatalf("unexpected evaluation: %#v", evaluation)
	}
	snapshot := s.SkillSnapshot()
	items := snapshot["skills"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("unexpected skill snapshot: %#v", snapshot)
	}
	if got := items[0]["evaluation"]; got != evaluation {
		t.Fatalf("snapshot evaluation = %#v, want %#v", got, evaluation)
	}
}
