---
name: harnessmesh-review
description: Run evidence-oriented peer reviews through HarnessMesh and turn findings into actionable, traceable follow-up work.
---

# HarnessMesh review

Use this skill when the user requests a review, second opinion, security check,
test assessment or structured finding from another coding agent.

## Workflow

1. Inspect `peer.status` and `collaboration.status` to understand the current session.
2. Use `peer.request_review` with a narrow focus and explicit evidence expectations.
3. Record reproducible observations with `peer.submit_finding` and attach proof with `peer.submit_evidence`.
4. Use `peer.challenge` when evidence is incomplete or a conclusion is disputed.
5. Use `peer.resolve` only after the finding has a clear, evidence-backed resolution.

Do not present an unverified peer response as a passed test. Distinguish
observations, hypotheses and verified results in the final summary.
