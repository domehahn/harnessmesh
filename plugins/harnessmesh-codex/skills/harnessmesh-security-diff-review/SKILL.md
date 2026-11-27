---
name: harnessmesh-security-diff-review
description: Review security-sensitive code changes using the changed lines and their callers.
---

# Review security-sensitive code changes using the changed lines and their callers

1. Identify changed entry points, trust boundaries, and sensitive data flows. Read the callers and tests that establish the expected behavior.
2. Check whether the change widens permissions, weakens validation, exposes secrets, or alters outbound destinations. Identify the smallest relevant diff hunk and the behavior a regression test should assert.
3. Report only findings supported by the diff and its reachable paths. Include file, line, impact, and a verification result or an explicit outstanding check.
