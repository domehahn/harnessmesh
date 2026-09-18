package workflow

const ReviewSchema = `{
  "type": "object",
  "properties": {
    "verdict": {
      "type": "string",
      "enum": ["approve", "changes_required", "block"]
    },
    "summary": {
      "type": "string"
    },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "severity": {
            "type": "string",
            "enum": ["critical", "high", "medium", "low", "info"]
          },
          "file": {"type": "string"},
          "line": {"type": "integer"},
          "claim": {"type": "string"},
          "evidence": {"type": "string"},
          "recommendation": {"type": "string"}
        },
        "required": ["id", "severity", "claim", "evidence", "recommendation"],
        "additionalProperties": false
      }
    },
    "questions": {
      "type": "array",
      "items": {"type": "string"}
    }
  },
  "required": ["verdict", "summary", "findings", "questions"],
  "additionalProperties": false
}`
