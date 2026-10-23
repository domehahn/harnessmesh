package governance

import "testing"

func TestContractSatisfiesWriteAndToolPolicy(t *testing.T) {
	c := AgentContract{Agent: "reviewer", Capabilities: []string{"review"}, AllowedTools: []string{"read_file"}, Filesystem: "read"}
	d := c.Satisfies(TaskRequirements{Capabilities: []string{"review"}, Tools: []string{"write_file"}, WriteAccess: true})
	if d.Allowed || len(d.Reasons) != 2 {
		t.Fatalf("expected denied decision with two reasons, got %+v", d)
	}
}

func TestContractRequiresApproval(t *testing.T) {
	c := AgentContract{Agent: "executor", Capabilities: []string{"execute"}, AllowedTools: []string{"*"}, Filesystem: "write", Network: "allow"}
	d := c.Satisfies(TaskRequirements{Capabilities: []string{"execute"}, Approval: true})
	if !d.Allowed || !d.RequiresApprove {
		t.Fatalf("expected approval boundary, got %+v", d)
	}
}

func TestTrustScoreIsBoundedAndPenalizesIncidents(t *testing.T) {
	good := TrustScore(TrustSignals{SuccessRate: 1, ReviewAcceptance: 1, SampleCount: 10})
	bad := TrustScore(TrustSignals{SuccessRate: 1, ReviewAcceptance: 1, SecurityIncidents: 30, SampleCount: 10})
	if good <= bad || good > 1 || bad < 0 {
		t.Fatalf("unexpected trust scores good=%v bad=%v", good, bad)
	}
}
