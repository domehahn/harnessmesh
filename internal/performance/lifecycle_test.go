package performance

import (
	"errors"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
)

func baseInput() NewObservationInput {
	return NewObservationInput{
		Tenant: "tenant-a", TaskID: "T1", ExecutionID: "T1#1",
		TaskClass: TaskClassSecurityReview, AgentID: "security-agent-a",
		ExecutionOutcome: ExecutionCompleted,
		StartedAt:        time.Now().Add(-time.Minute),
		FinishedAt:       time.Now(),
		Metrics:          PerformanceMetrics{Duration: time.Minute},
	}
}

func TestNewObservation_Valid(t *testing.T) {
	obs, err := NewObservation(baseInput())
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	if obs.Status != ObservationPending {
		t.Fatalf("expected pending, got %q", obs.Status)
	}
	if obs.QualityOutcome != QualityUnreviewed {
		t.Fatalf("expected unreviewed quality before any review, got %q", obs.QualityOutcome)
	}
}

func TestNewObservation_UnknownTaskClassRejected(t *testing.T) {
	in := baseInput()
	in.TaskClass = "made_up_class"
	_, err := NewObservation(in)
	var want *UnknownTaskClass
	if !errors.As(err, &want) {
		t.Fatalf("expected UnknownTaskClass, got %v (%T)", err, err)
	}
}

func TestNewObservation_NoAgentAttributionRejected(t *testing.T) {
	in := baseInput()
	in.AgentID = ""
	_, err := NewObservation(in)
	var want *AttributionFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected AttributionFailure, got %v (%T)", err, err)
	}
}

func TestNegative_NegativeDurationRejected(t *testing.T) {
	in := baseInput()
	in.Metrics.Duration = -time.Second
	_, err := NewObservation(in)
	var want *MalformedMetrics
	if !errors.As(err, &want) {
		t.Fatalf("expected MalformedMetrics for negative duration, got %v (%T)", err, err)
	}
}

func TestNegative_EndBeforeStartRejected(t *testing.T) {
	in := baseInput()
	in.StartedAt = time.Now()
	in.FinishedAt = in.StartedAt.Add(-time.Minute)
	_, err := NewObservation(in)
	var want *MalformedMetrics
	if !errors.As(err, &want) {
		t.Fatalf("expected MalformedMetrics for finished before started, got %v (%T)", err, err)
	}
}

func TestNegative_MalformedCostDataRejected(t *testing.T) {
	in := baseInput()
	in.Metrics.CostUnits = -5
	_, err := NewObservation(in)
	var want *MalformedMetrics
	if !errors.As(err, &want) {
		t.Fatalf("expected MalformedMetrics for negative cost, got %v (%T)", err, err)
	}
}

func TestFinalizeWithReview_VerifiedFinalizes(t *testing.T) {
	obs, _ := NewObservation(baseInput())
	group := review.ReviewGroup{
		ID: "RG-1", Status: "verified",
		Observations: []review.FindingObservation{
			{SourceAgentID: "security-agent-a", Stance: review.StanceConfirms},
			{SourceAgentID: "reviewer-b", Stance: review.StanceConfirms},
			{SourceAgentID: "reviewer-c", Stance: review.StanceConfirms},
		},
	}
	out := FinalizeWithReview(obs, group)
	if out.Status != ObservationFinalized {
		t.Fatalf("expected finalized, got %q", out.Status)
	}
	if out.QualityOutcome != QualityVerified {
		t.Fatalf("expected verified, got %q", out.QualityOutcome)
	}
	// implementer excluded from reviewer attribution
	for _, r := range out.ReviewerAgentIDs {
		if r == obs.AgentID {
			t.Fatalf("implementer must not appear in ReviewerAgentIDs")
		}
	}
	if len(out.ReviewerAgentIDs) != 2 {
		t.Fatalf("expected 2 reviewers, got %d: %v", len(out.ReviewerAgentIDs), out.ReviewerAgentIDs)
	}
}

func TestFinalizeWithReview_SupportedStaysPending(t *testing.T) {
	// "Supported" is agreement, not authority - must NOT finalize.
	obs, _ := NewObservation(baseInput())
	group := review.ReviewGroup{ID: "RG-1", Status: "supported", Observations: []review.FindingObservation{
		{SourceAgentID: "security-agent-a", Stance: review.StanceConfirms},
		{SourceAgentID: "reviewer-b", Stance: review.StanceConfirms},
	}}
	out := FinalizeWithReview(obs, group)
	if out.Status != ObservationPending {
		t.Fatalf("expected a merely-'supported' claim to stay pending, not be finalized, got %q", out.Status)
	}
}

func TestNegative_UnreviewedCompletionNotCountedAsVerified(t *testing.T) {
	obs, _ := NewObservation(baseInput())
	if obs.QualityOutcome == QualityVerified {
		t.Fatalf("a freshly observed execution must never default to verified")
	}
	if obs.Status == ObservationFinalized {
		t.Fatalf("a freshly observed execution must never default to finalized")
	}
}
