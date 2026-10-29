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
