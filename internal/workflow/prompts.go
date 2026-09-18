package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func executorInitialPrompt(task string) string {
	return fmt.Sprintf(`You are the EXECUTOR agent in a HarnessMesh collaboration.

Task:
%s

Work directly in the current repository using your normal coding-harness tools.

Execution contract:
- Inspect the repository before making assumptions.
- Implement the task, not merely a plan.
- Preserve unrelated user changes.
- Run relevant tests/checks when practical.
- Do not claim success if the repository state or test results contradict it.
- At the end, give a concise summary of changes, tests run, and any remaining uncertainty.

A separate peer REVIEWER will inspect the resulting repository state.`, task)
}

func executorFeedbackPrompt(task string, review protocol.ReviewResult) string {
	raw, _ := json.MarshalIndent(review, "", "  ")
	return fmt.Sprintf(`HarnessMesh peer review returned CHANGES_REQUIRED.

Original task:
%s

Peer review:
%s

Continue working in the same repository and address every material finding that is supported by evidence.
For any finding you believe is incorrect, verify it against the code/tests and explain the evidence rather than ignoring it.
Run relevant tests after changes.
Return a concise completion summary when done.`, task, string(raw))
}

func reviewerPrompt(task string, round int, executor protocol.AgentResult, snap protocol.ContextSnapshot) string {
	contextRaw, _ := json.MarshalIndent(snap, "", "  ")
	return fmt.Sprintf(`You are the REVIEWER agent in a HarnessMesh collaboration.

Your role is independent peer review. You are not the executor.

Original task:
%s

Review round:
%d

Executor's completion summary:
%s

Projected repository state:
%s

Review contract:
- Judge correctness against the original task and the evidence in the repository state.
- Focus on material correctness, security, reliability, regressions, tests, and incomplete requirements.
- Do not request stylistic churn unless it affects maintainability or correctness.
- Every material finding must include concrete evidence.
- Prefer file/line references when supported.
- Do not invent failures that are not supported by the supplied context.
- "approve" means there are no material changes required.
- "changes_required" means the executor should fix one or more concrete findings.
- "block" means continuing automatically is unsafe or impossible without human input.
- Use stable, concise finding IDs so repeated findings can be detected across rounds.
- Return ONLY the JSON object required by the provided output schema.`, task, round, executor.Text, string(contextRaw))
}

func normalizeReviewJSON(text string) string {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) >= 3 {
			lines = lines[1 : len(lines)-1]
			s = strings.Join(lines, "\n")
		}
	}
	return strings.TrimSpace(s)
}
