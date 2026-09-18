package workflow

import (
	"testing"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func TestValidateReview(t *testing.T) {
	ok := protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "fix",
		Findings: []protocol.Finding{
			{
				ID: "HM-1", Severity: "high",
				Claim: "race", Evidence: "file.go:10",
				Recommendation: "lock it",
			},
		},
	}
	if err := validateReview(ok); err != nil {
		t.Fatal(err)
	}

	bad := protocol.ReviewResult{Verdict: protocol.VerdictApprove, Findings: ok.Findings}
	if err := validateReview(bad); err == nil {
		t.Fatal("expected error")
	}
}

func TestFindingHashStable(t *testing.T) {
	a := protocol.ReviewResult{Findings: []protocol.Finding{
		{ID: "B", Claim: "two", File: "b.go"},
		{ID: "A", Claim: "one", File: "a.go"},
	}}
	b := protocol.ReviewResult{Findings: []protocol.Finding{
		{ID: "A", Claim: "one", File: "a.go"},
		{ID: "B", Claim: "two", File: "b.go"},
	}}
	if findingHash(a) != findingHash(b) {
		t.Fatal("hash should be order-independent")
	}
}
