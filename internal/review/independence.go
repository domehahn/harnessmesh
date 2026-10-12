package review

import "github.com/domehahn/harnessmesh/internal/supervisor"

// IsIndependentObservation reports whether obs is an independent
// observation relative to the OTHER observations already in group - the
// central anti-majority-voting, anti-self-review check this package is
// built around. Agreement is not authority: this function is what Core
// consults before ever letting multiple "confirms" stances count toward
// verification.
//
// An observation is independent of the group's existing observations
// only if BOTH:
//  1. obs.SourceAgentID differs from every existing observation's
//     SourceAgentID (an agent cannot corroborate its own claim).
//  2. obs.ConsumedFindingIDs is empty, OR none of the IDs it lists
//     belong to an observation already in this group (an agent that
//     read another agent's finding and merely echoed it back is not an
//     independent reviewer of the underlying artifact, even if its
//     SourceAgentID differs).
//
// Independence is deliberately a separate concern from artifact-version
// freshness - IsIndependentObservation does not check ArtifactVersion at
// all. In normal operation this never matters (ReviewState.Ingest/
// FindGroup only ever join observations that already share one
// ArtifactVersion into the same group), but keeping the concerns
// separate means a version mismatch introduced some other way (e.g. a
// future ingestion path, or a bug) surfaces as the precise
// StaleArtifactEvidence error from ReviewAuthorizer.checkArtifactFreshness
// (authorize.go), not as a confusingly-unrelated independence failure.
func IsIndependentObservation(group ReviewGroup, obs FindingObservation) bool {
	existingFindingIDs := make(map[string]bool, len(group.Observations))
	for _, existing := range group.Observations {
		if existing.SourceAgentID == obs.SourceAgentID {
			return false
		}
		existingFindingIDs[existing.FindingID] = true
	}
	for _, consumed := range obs.ConsumedFindingIDs {
		if existingFindingIDs[consumed] {
			return false
		}
	}
	return true
}

// IndependentConfirmingObservations returns every observation in group
// with StanceConfirms that is independent of the OTHER confirming
// observations that precede it (in slice order) - i.e. the maximal set
// of mutually independent confirmations. This is deliberately not just
// "count distinct agents": an agent whose ConsumedFindingIDs references
// an earlier confirmation in this set is excluded even if no other
// agent-identity collision exists.
func IndependentConfirmingObservations(group ReviewGroup) []FindingObservation {
	return independentObservationsWithStance(group, StanceConfirms)
}

// IndependentRefutingObservations is the StanceRefutes analogue of
// IndependentConfirmingObservations, used to detect a genuine,
// evidence-backed conflict rather than one agent echoing another's
// refutation.
func IndependentRefutingObservations(group ReviewGroup) []FindingObservation {
	return independentObservationsWithStance(group, StanceRefutes)
}

func independentObservationsWithStance(group ReviewGroup, stance ObservationStance) []FindingObservation {
	var accepted []FindingObservation
	for _, obs := range group.Observations {
		if obs.Stance != stance {
			continue
		}
		probe := ReviewGroup{ArtifactVersion: group.ArtifactVersion, Observations: accepted}
		if IsIndependentObservation(probe, obs) {
			accepted = append(accepted, obs)
		}
	}
	return accepted
}

// SelfReviewAttempt reports whether candidateAgent would be reviewing its
// own claim if assigned to the given group - i.e. candidateAgent already
// has an observation recorded in this group. Used to block self-review
// at proposal-authorization time (see authorize.go), independent of
// whatever an Arbiter or Scheduler might otherwise select.
func SelfReviewAttempt(group ReviewGroup, candidateAgent supervisor.AgentID) bool {
	for _, obs := range group.Observations {
		if obs.SourceAgentID == candidateAgent {
			return true
		}
	}
	return false
}
