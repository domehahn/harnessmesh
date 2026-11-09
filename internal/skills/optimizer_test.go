package skills

import (
	"testing"

	"github.com/domehahn/harnessmesh/internal/evaluation"
)

func TestProposeAndProbeHarnessCycle(t *testing.T) {
	entry := Entry{
		Manifest: Manifest{
			Name:        "harnessmesh-review",
			Version:     "1.0.0",
			Tools:       []string{"collaboration.inbox"},
			StrictOrder: false,
		},
		Content: "# Review Skill\nStep 1: [fn: collaboration.inbox]\n",
	}

	baseline := []evaluation.Run{
		{
			Tenant:        "t1",
			SessionID:     "s1",
			TaskID:        "task-1",
			Success:       false,
			PolicyOK:      true,
			HumanApproved: true,
			Steps: []evaluation.Step{
				{Name: "collaboration.inbox", Kind: "tool", Status: "completed", PolicyOK: true},
				{Name: "peer.request_review", Kind: "tool", Status: "completed", PolicyOK: true},
				{Name: "retry-step", Kind: "retry", Status: "failed", PolicyOK: true},
			},
		},
	}

	proposal, err := ProposeHarnessPatch(entry, baseline)
	if err != nil {
		t.Fatalf("ProposeHarnessPatch failed: %v", err)
	}
	if !proposal.RequiresApproval {
		t.Fatal("expected proposal to require human approval")
	}
	if proposal.ProposedVersion != "1.0.1" {
		t.Fatalf("expected proposed version 1.0.1, got %q", proposal.ProposedVersion)
	}
	if len(proposal.AddedTools) != 1 || proposal.AddedTools[0] != "peer.request_review" {
		t.Fatalf("expected peer.request_review in AddedTools, got %#v", proposal.AddedTools)
	}
	if !proposal.EnableStrictOrder {
		t.Fatal("expected EnableStrictOrder to be true after retry/failure")
	}

	candidate := []evaluation.Run{
		{
			Tenant:        "t1",
			SessionID:     "s2",
			TaskID:        "task-1",
			Success:       true,
			PolicyOK:      true,
			HumanApproved: true,
			Steps: []evaluation.Step{
				{Name: "collaboration.inbox", Kind: "tool", Status: "completed", PolicyOK: true},
				{Name: "peer.request_review", Kind: "tool", Status: "completed", PolicyOK: true},
			},
		},
	}

	report, err := ProbeHarness(entry, baseline, candidate, 0.8)
	if err != nil {
		t.Fatalf("ProbeHarness failed: %v", err)
	}
	if report.Decision != "promote" || !report.RequiresApproval {
		t.Fatalf("expected promote decision with approval gate, got %#v", report)
	}
}
