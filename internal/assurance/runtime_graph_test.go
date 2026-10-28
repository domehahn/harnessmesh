package assurance

import "testing"

func TestRuntimeGraphSeparatesDeclaredAndObservedDrift(t *testing.T) {
	g := NewRuntimeGraph()
	if err := g.Declare(Capability{ID: "agent.codex", Kind: "agent", Allowed: true}); err != nil {
		t.Fatal(err)
	}
	if !g.Observe("agent.codex") {
		t.Fatal("declared capability should be allowed")
	}
	if g.Observe("tool.unknown") {
		t.Fatal("undeclared capability must be denied")
	}
	_, observed, drift := g.Snapshot()
	if observed["tool.unknown"] != 1 || !drift["tool.unknown"] {
		t.Fatalf("unexpected graph state: %#v %#v", observed, drift)
	}
}
