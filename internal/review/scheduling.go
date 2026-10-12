package review

import "github.com/domehahn/harnessmesh/internal/supervisor"

// ToSchedulerProposal translates a RequestIndependentReviewAction into a
// normal supervisor.CreateTaskProposal, reusing Phase 3's existing,
// unmodified Scheduler/Applier/Validator/Authorizer path rather than
// adding a parallel scheduling mechanism. Scheduler remains authoritative
// for agent selection (capability-based, deterministic) - this function
// never names a concrete agent, consistent with "Der Arbiter darf keinen
// Agent hart auswählen."
//
// The independence constraint ("must not be the same agent that
// produced the original claim") is NOT enforced by excluding an agent
// from Scheduler's routing candidate pool - Scheduler's capability-based
// routing (internal/supervisor) has no concept of "excluded agent" and
// this function deliberately does not add one, to avoid coupling Phase 3
// scheduling to Phase 4 review semantics. Instead, independence is
// enforced where it actually matters: at verification time
// (ReviewAuthorizer.verifyClaim / IsIndependentObservation), which
// structurally cannot count a self-review's observation toward
// "supported" or "verified", regardless of which agent Scheduler
// happened to route the review task to. This is weaker at scheduling
// time and strictly authoritative at verification time - exactly where
// ADR 0002 Phase 4 says final authority must live.
func ToSchedulerProposal(action RequestIndependentReviewAction, taskID supervisor.TaskID) supervisor.AgentProposal {
	return supervisor.CreateTaskProposal{
		Task: supervisor.Task{
			ID:                   taskID,
			Type:                 "independent-review",
			RequiredCapabilities: append([]string(nil), action.RequiredCapabilities...),
		},
		RationaleSummary: action.RationaleSummary.toSupervisorRationale(),
	}
}

// toSupervisorRationale bridges review.RationaleSummary to
// supervisor.RationaleSummary - both are short, bounded, auditable
// strings; this conversion exists only at this one integration seam so
// the two packages' rationale types stay otherwise independent.
func (r RationaleSummary) toSupervisorRationale() supervisor.RationaleSummary {
	return supervisor.RationaleSummary(r)
}
