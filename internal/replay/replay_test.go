package replay

import "testing"

func TestBuildAndVerifyRedactedPlan(t *testing.T) {
	plan, err := Build("session-1", "tenant-1", []Event{{Sequence: 2, Type: "message", Payload: map[string]any{"token": "secret-token-123456"}}, {Sequence: 1, Type: "start"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(plan); err != nil {
		t.Fatal(err)
	}
	if plan.Events[0].Sequence != 1 || plan.Digest == "" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	plan.Events[1].Type = "tampered"
	if err := Verify(plan); err == nil {
		t.Fatal("expected tampered replay plan to fail")
	}
}
