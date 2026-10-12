package supervisor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ExecutionID uniquely identifies one execution ATTEMPT of a task -
// distinct from TaskID, which identifies the task across every attempt
// (including retries). It is Core-owned: generated only by Executor
// itself at the actual execution boundary (see NewExecutionID, called
// from Execute), never derived from, trusted from, or overwritable by
// agent output, provider output, or LLM output. ADR 0006 Phase 7C's
// "Core-owned Execution Identity" closure requirement.
type ExecutionID string

// NewExecutionID generates a Core-owned, unique execution-attempt
// identifier. Uniqueness does not depend on wall-clock time alone (the
// random suffix guarantees this even if two attempts were somehow
// generated in the same nanosecond) - mirroring the existing
// crypto/rand-based ID pattern already used elsewhere in this repository
// (internal/provider/chatgpt_siwc.go's urn:uuid construction) rather than
// adding a UUID dependency. The TaskID prefix is purely for
// human-readable correlation during debugging; it carries no uniqueness
// guarantee by itself.
func NewExecutionID(taskID TaskID) ExecutionID {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely unlikely on any real target;
		// fall back to a timestamp-seeded value so Execute can never fail
		// outright over ID generation - mirroring the existing fallback
		// pattern in internal/provider/chatgpt_siwc.go.
		return ExecutionID(fmt.Sprintf("%s@%d", taskID, time.Now().UnixNano()))
	}
	return ExecutionID(fmt.Sprintf("%s@%s", taskID, hex.EncodeToString(b)))
}
