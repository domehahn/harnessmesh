# Collaboration Patterns

HarnessMesh supports multiple agent-to-agent collaboration patterns while strictly maintaining the **single-writer invariant**.

## 1. Executor / Reviewer (Default)

The executor (Claude Code) actively implements code in the working tree. During or after implementation, Claude invokes `peer.request_review` to ask Codex for a structured review.
- Claude receives structured findings with line numbers, claims, and recommendations.
- Claude implements fixes and re-requests review.
- Codex returns `approve`.

## 2. Reverse Reviewer

Codex serves as the primary reviewer for an external developer or agent, while Claude acts in an advisory or secondary capacity.

## 3. Peer Question / Inspection

An active agent pauses implementation to ask a specific question without initiating a full review:
- Claude calls `peer.ask(peer="codex", question="Check token refresh logic for race conditions", scope=["internal/auth"])`.
- Codex receives bounded context, analyzes the code in a read-only sandbox, and returns the direct answer.
- Claude incorporates the advice and continues.

## 4. Challenge & Resolution

When agents disagree on a finding:
1. Agent A submits a finding: `peer.submit_finding(id="HM-001", claim="Deadlock potential")`.
2. Agent B challenges: `peer.challenge(finding_id="HM-001", claim="Lock is already held by caller")`. Finding status changes to `disputed`.
3. Evidence is submitted: `peer.submit_evidence(finding_id="HM-001", type="test_result", command="go test -race ./...", result="PASS")`.
4. The finding is resolved based on the concrete evidence: `peer.resolve(finding_id="HM-001", status="rejected", rationale="Race test passed cleanly")`.

