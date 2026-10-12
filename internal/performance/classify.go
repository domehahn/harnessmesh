package performance

import (
	"context"
	"errors"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// ClassifyExecutionOutcome derives an authoritative ExecutionOutcome
// from Executor's own typed errors and ExecutionResult.Status - never
// from any agent-reported text. This is a pure function over Core/
// Executor state, mirroring supervisor/scheduler.go's
// classifyExecutionFailure taxonomy exactly (reused conceptually, not
// duplicated logic, since that function lives in a different package
// and returns a different, event-shaped type).
func ClassifyExecutionOutcome(result supervisor.ExecutionResult, execErr error) ExecutionOutcome {
	if execErr == nil {
		if result.Status == supervisor.TaskStatusFailed {
			return ExecutionFailed
		}
		return ExecutionCompleted
	}

	if errors.Is(execErr, context.Canceled) {
		return ExecutionCancelled
	}

	switch execErr.(type) {
	case *supervisor.ExecutionTimeout:
		return ExecutionTimedOut
	case *supervisor.AuthorizationFailure:
		return ExecutionAuthorizationRejected
	case *supervisor.PolicyRejection:
		return ExecutionPolicyRejected
	case *supervisor.AgentRoutingFailure:
		return ExecutionBlocked
	case *supervisor.StateTransitionFailure:
		return ExecutionBlocked
	default:
		return ExecutionFailed
	}
}
