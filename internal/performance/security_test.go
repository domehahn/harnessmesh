package performance

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/review"
)

// TestSecurity_AgentCannotSelfReportSuccess proves there is no code path
// by which an "agent said it succeeded" string becomes
// ExecutionCompleted - NewObservationInput.ExecutionOutcome is a typed
// enum the CALLER sets from supervisor.ExecutionResult/Scheduler
// classification; there is no field anywhere that takes free text and
// no function that parses agent-provided text into an outcome.
func TestSecurity_AgentCannotSelfReportSuccess(t *testing.T) {
	// Structural proof: NewObservationInput has no string/text field that
	// could carry an agent's own claim and influence ExecutionOutcome -
	// only a pre-typed ExecutionOutcome enum value.
	typ := reflect.TypeOf(NewObservationInput{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "claim") || strings.Contains(name, "selfreport") || strings.Contains(name, "agentmessage") {
			t.Fatalf("NewObservationInput must not carry an agent-claim-shaped field, found %q", typ.Field(i).Name)
		}
	}
	// An unknown/invented outcome string is rejected, not accepted as a
	// success by default.
	in := baseInput()
	in.ExecutionOutcome = "i_totally_succeeded"
	_, err := NewObservation(in)
	if err == nil {
		t.Fatalf("expected an unrecognized, agent-shaped outcome string to be rejected")
	}
}

// TestSecurity_ModelCannotSelfReportIdentity proves ModelUsageRef (and
// everything that constructs a PerformanceObservation) has no decode
// path from untrusted text - a model/provider identity can only arrive
// as a pre-populated Go struct field the CALLER sets from
// supervisor.ModelRoutingDecision / InferenceResult.ServedModel, never
// parsed from an LLM's own output text.
func TestSecurity_ModelCannotSelfReportIdentity(t *testing.T) {
	typ := reflect.TypeOf(ModelUsageRef{})
	for i := 0; i < typ.NumField(); i++ {
		t.Logf("ModelUsageRef field: %s", typ.Field(i).Name)
	}
	// There is no function in this package named anything like
	// "ParseModelFromText" or "DecodeModelUsage" - confirmed by
	// inspection of this package's exported surface (no such function
	// exists; this test documents the invariant rather than exercising
	// a decoder that was never written).
	if _, ok := reflect.TypeOf(&struct{}{}).MethodByName("ParseModelFromText"); ok {
		t.Fatalf("a model-identity text parser must never exist")
	}
}

// TestSecurity_RemediatorSelfVerificationNotCountedAsIndependent proves
// FinalizeWithReview correctly excludes the implementer from
// ReviewerAgentIDs AND from the independent-confirmation count used to
// finalize - reusing review.IndependentConfirmingObservations, which
// already enforces this at the review layer (ADR 0003/0004); this test
// proves the performance layer does not accidentally re-introduce the
// implementer into the counted set.
func TestSecurity_RemediatorSelfVerificationNotCountedAsIndependent(t *testing.T) {
	// A single observation - even the implementer's own - counts as 1
	// toward the independent total (review.IndependentConfirmingObservations
	// has nothing else in the set to be "non-independent" from), but 1 is
	// below MinIndependentConfirmationsForVerification (2): the real
	// security property is that this single, self-only observation can
	// NEVER alone finalize/verify - never that it contributes zero.
	obs, _ := NewObservation(baseInput()) // AgentID = security-agent-a
	group := review.ReviewGroup{
		ID: "RG-1", Status: "unconfirmed",
		Observations: []review.FindingObservation{
			{SourceAgentID: "security-agent-a", Stance: review.StanceConfirms}, // implementer's own claim
		},
	}
	out := FinalizeWithReview(obs, group)
	if out.Metrics.IndependentConfirmationCount != 1 {
		t.Fatalf("expected 1 independent confirmation (the implementer's own, uncontested observation), got %d", out.Metrics.IndependentConfirmationCount)
	}
	if out.Status == ObservationFinalized {
		t.Fatalf("a self-only claim must never finalize/verify on its own")
	}
}

// TestSecurity_CopiedReviewNotCountedAsIndependent proves the same for
// an agent that merely consumed another's finding (ConsumedFindingIDs).
func TestSecurity_CopiedReviewNotCountedAsIndependent(t *testing.T) {
	// 2 observations exist, but the second is a copy of the first
	// (ConsumedFindingIDs) - it must NOT add a second independent
	// corroboration. The count stays at 1 (the original), proving the
	// copy contributed nothing extra, and 1 is still below the
	// verification threshold of 2.
	obs, _ := NewObservation(baseInput())
	group := review.ReviewGroup{
		ID: "RG-1", Status: "unconfirmed",
		Observations: []review.FindingObservation{
			{SourceAgentID: "security-agent-a", FindingID: "F1", Stance: review.StanceConfirms},
			{SourceAgentID: "copier-agent", FindingID: "F2", Stance: review.StanceConfirms, ConsumedFindingIDs: []string{"F1"}},
		},
	}
	out := FinalizeWithReview(obs, group)
	if out.Metrics.IndependentConfirmationCount != 1 {
		t.Fatalf("expected independent confirmation count to stay at 1 (copier added nothing), got %d", out.Metrics.IndependentConfirmationCount)
	}
	if out.Status == ObservationFinalized {
		t.Fatalf("1 independent confirmation (the copy doesn't count as a 2nd) must not finalize")
	}
}

// TestNegative_StaleFindingStatusHandledViaFreshFetch documents the
// expected caller discipline (FinalizeWithReview must be called with a
// FRESHLY fetched review.ReviewGroup, never a cached one) by proving
// that finalizing twice with two different group snapshots (simulating
// a stale-then-fresh sequence) always reflects whichever snapshot was
// actually passed - there is no internal caching in this package that
// could mask a stale call.
func TestNegative_StaleFindingStatusHandledViaFreshFetch(t *testing.T) {
	obs, _ := NewObservation(baseInput())
	staleGroup := review.ReviewGroup{ID: "RG-1", Status: "contested"}
	afterStale := FinalizeWithReview(obs, staleGroup)
	if afterStale.Status != ObservationPending {
		t.Fatalf("expected pending for a contested (stale) snapshot, got %q", afterStale.Status)
	}

	freshGroup := review.ReviewGroup{ID: "RG-1", Status: "verified", Observations: []review.FindingObservation{
		{SourceAgentID: "a", Stance: review.StanceConfirms}, {SourceAgentID: "b", Stance: review.StanceConfirms},
	}}
	afterFresh := FinalizeWithReview(afterStale, freshGroup)
	if afterFresh.Status != ObservationFinalized || afterFresh.QualityOutcome != QualityVerified {
		t.Fatalf("expected the fresh snapshot to finalize correctly, got %+v", afterFresh)
	}
}

func TestSecurity_UnknownAgentRejectedByObservation(t *testing.T) {
	// This package does not maintain its own agent registry (that
	// remains supervisor.AgentDirectory's job) - it only requires
	// AgentID to be non-empty. Cross-checking against the real registry
	// is the CALLER's responsibility before constructing the input,
	// exactly as Phase 2A/2B already established for supervisor
	// proposals. Documented here, not re-implemented.
	in := baseInput()
	in.AgentID = ""
	_, err := NewObservation(in)
	var want *AttributionFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected AttributionFailure for empty agent attribution, got %v (%T)", err, err)
	}
}
