package supervisor

import (
	"context"
	"fmt"
)

// Authorizer is HarnessMesh Core's authorization boundary. A proposal that
// passed Validator.ValidateBatch is only structurally well-formed - it has
// NOT been authorized. Every proposal in a batch must also pass Authorize
// before Applier mutates any state. This is a deliberately separate step
// from validation (requirement: "a successfully validated proposal MUST
// NOT imply authorization").
type Authorizer interface {
	Authorize(ctx context.Context, state ExecutionState, proposal AgentProposal) error
}

// AllowAllAuthorizer authorizes everything. Useful for tests that want to
// exercise validation/application/execution without exercising policy
// denial - never appropriate for anything but tests.
type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Authorize(ctx context.Context, state ExecutionState, proposal AgentProposal) error {
	return nil
}

var _ Authorizer = AllowAllAuthorizer{}

// CoreAuthorizer is a deterministic, explicit policy authorizer for Phase
// 2A. It is not a real policy engine - it exists to prove the
// validate/authorize/apply separation end-to-end with rules a test can
// control precisely. A real HarnessMesh Core policy integration can later
// implement the same Authorizer interface without changing Applier.
type CoreAuthorizer struct {
	// Directory resolves agent tenant membership for cross-tenant checks.
	// May be nil if tenant checks are not needed by the caller.
	Directory AgentDirectory
	// DeniedAgents blocks specific agents from ever being authorized for
	// assignment or review, regardless of validation outcome.
	DeniedAgents map[AgentID]bool
	// DeniedCapabilities blocks specific capabilities from being granted
	// via assignment, even if the agent declares them (e.g. a capability
	// that requires a separate, not-yet-granted human approval).
	DeniedCapabilities map[string]bool
}

func (a *CoreAuthorizer) Authorize(ctx context.Context, state ExecutionState, proposal AgentProposal) error {
	switch prop := proposal.(type) {
	case AssignTaskProposal:
		if a.DeniedAgents[prop.CandidateAgentID] {
			return &PolicyRejection{Reason: fmt.Sprintf("agent %q is denied by policy", prop.CandidateAgentID)}
		}
		for _, c := range prop.RequiredCaps {
			if a.DeniedCapabilities[c] {
				return &PolicyRejection{Reason: fmt.Sprintf("capability %q is denied by policy", c)}
			}
		}
		if task, ok := state.Tasks.ByID(prop.TaskID); ok && task.Tenant != "" {
			// A task that declares a tenant REQUIRES tenant enforcement.
			// If this directory cannot resolve tenancy at all, that is
			// not "no tenant restriction" - it is a missing control, and
			// we fail closed rather than silently treating the absence
			// of tenant metadata as permission (see docs/decisions/0002,
			// "Tenant limitation").
			td, ok := a.directoryAsTenantDirectory()
			if !ok {
				return &AuthorizationFailure{
					AgentID: prop.CandidateAgentID,
					TaskID:  prop.TaskID,
					Reason:  fmt.Sprintf("task requires tenant %q but no TenantDirectory is configured - failing closed", task.Tenant),
				}
			}
			tenant, known := td.TenantOf(prop.CandidateAgentID)
			if !known || tenant != task.Tenant {
				return &AuthorizationFailure{
					AgentID: prop.CandidateAgentID,
					TaskID:  prop.TaskID,
					Reason:  fmt.Sprintf("cross-tenant assignment: task requires tenant %q", task.Tenant),
				}
			}
		}
	case RequestReviewProposal:
		if a.DeniedAgents[prop.ReviewerAgentID] {
			return &PolicyRejection{Reason: fmt.Sprintf("agent %q is denied by policy", prop.ReviewerAgentID)}
		}
	}
	return nil
}

func (a *CoreAuthorizer) directoryAsTenantDirectory() (TenantDirectory, bool) {
	if a.Directory == nil {
		return nil, false
	}
	td, ok := a.Directory.(TenantDirectory)
	return td, ok
}

var _ Authorizer = (*CoreAuthorizer)(nil)
