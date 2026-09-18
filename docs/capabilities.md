# Capability Matrix & Discovery in HarnessMesh v0.2.0

HarnessMesh v0.2.0 decouples agent identity from peer discovery by introducing a first-class capability model. Instead of hardcoding requests to specific peer names (e.g. `claude` or `codex`), participants can discover peers dynamically by required capabilities.

## Capability Matrix

Every agent registered in HarnessMesh declares its capabilities in its configuration:

```json
{
  "capabilities": {
    "read_repository": true,
    "write_repository": true,
    "run_commands": true,
    "review": true,
    "answer_questions": true,
    "submit_evidence": true
  }
}
```

### Core Capabilities

| Capability | Description | Default for Executor | Default for Reviewer |
|---|---|:---:|:---:|
| `read_repository` | Can inspect workspace code and directory structures | Yes | Yes |
| `write_repository` | Can modify files and apply diffs | Yes (if writable) | **No** |
| `run_commands` | Permitted to run test suites, linters, and build commands | Yes | Optional |
| `review` | Can inspect proposed changes and return structured verdicts | Optional | **Yes** |
| `answer_questions` | Can respond to `peer.ask` queries | Yes | Yes |
| `submit_evidence` | Can submit findings, test outputs, and evidence artifacts | Yes | Yes |

---

## Peer Discovery via MCP (`peer.capabilities`)

An agent can discover which peers are available to satisfy a specific need by invoking `peer.capabilities`:

### Request
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "peer.capabilities",
    "arguments": {
      "capability": "review",
      "writable": false
    }
  }
}
```

### Response
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\"matches\":[{\"participant\":\"codex\",\"roles\":[\"reviewer\"],\"writable\":false}]}"
      }
    ]
  }
}
```

---

## Dynamic Routing by Capability

When calling `peer.request_review` or `peer.ask`, agents can omit the explicit `peer` name and supply a `capability`:

```json
{
  "capability": "review",
  "focus": ["security"]
}
```

The HarnessMesh collaboration engine filters available participants that satisfy the requested capability, enforces data privacy and workspace constraints, and applies the configured `peer_selection_policy` (e.g. `cheapest_suitable`).

