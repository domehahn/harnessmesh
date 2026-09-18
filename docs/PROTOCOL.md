# Peer protocol

The long-term HarnessMesh protocol is a structured message envelope between
harnesses.

v0.1 uses the same concepts internally while invoking the peer through its CLI.

## Envelope

```json
{
  "schema_version": 1,
  "session_id": "hm_...",
  "message_id": "msg_...",
  "from": "claude-executor",
  "to": "codex-reviewer",
  "type": "review_request",
  "created_at": "2026-09-18T20:00:00Z",
  "task": "Implement token refresh.",
  "round": 1,
  "context": {}
}
```

Planned message types:

```text
task
status
review_request
review_result
question
answer
challenge
evidence
decision
handoff
stop
```

## Review result

```json
{
  "verdict": "changes_required",
  "summary": "...",
  "findings": [
    {
      "id": "HM-001",
      "severity": "high",
      "file": "path/to/file.go",
      "line": 42,
      "claim": "...",
      "evidence": "...",
      "recommendation": "..."
    }
  ],
  "questions": []
}
```

## Evidence over authority

HarnessMesh should never resolve disagreements by simply choosing the supposedly
"stronger" model.

The preferred resolution chain is:

```text
claim
  ↓
repository evidence
  ↓
test / static analysis / reproducible command
  ↓
decision
```

Future `evidence-resolution` mode will expose this as a first-class protocol.
