package simulation

import "testing"

func TestCatalogContainsSafetyScenarios(t *testing.T) {
	if len(Catalog()) < 6 {
		t.Fatal("simulation catalog is incomplete")
	}
}

func TestEvaluateApprovalAndReplayGuards(t *testing.T) {
	approval := Evaluate(Scenario{ID: "approval", Fault: FaultApprovalBlocked}, Observation{ApprovalRaised: true, Execution: "pending"})
	if !approval.Passed {
		t.Fatalf("approval scenario failed: %+v", approval)
	}
	replay := Evaluate(Scenario{ID: "replay", Fault: FaultReplayInconsistent}, Observation{ReplayStatus: "incomplete", Execution: "pending"})
	if !replay.Passed {
		t.Fatalf("replay scenario failed: %+v", replay)
	}
}

func TestEvaluateRejectsUnsafeOutcome(t *testing.T) {
	result := Evaluate(Scenario{ID: "quota", Fault: FaultQuotaWait}, Observation{ProviderState: "quota_wait"})
	if result.Passed || len(result.Findings) == 0 {
		t.Fatalf("expected failed safety gate: %+v", result)
	}
}
