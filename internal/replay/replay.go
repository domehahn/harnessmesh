// Package replay builds deterministic, redacted replay plans. A plan is an
// evidence artifact, not an instruction to execute tools or call providers.
package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/domehahn/harnessmesh/internal/runbundle"
)

type Event struct {
	Sequence int       `json:"sequence"`
	Type     string    `json:"type"`
	Payload  any       `json:"payload,omitempty"`
	At       time.Time `json:"at,omitempty"`
}

type Plan struct {
	Version   int     `json:"version"`
	SessionID string  `json:"session_id"`
	TenantID  string  `json:"tenant_id"`
	Events    []Event `json:"events"`
	Digest    string  `json:"digest"`
}

func Build(sessionID, tenantID string, events []Event) (Plan, error) {
	if sessionID == "" || tenantID == "" {
		return Plan{}, errors.New("session and tenant are required")
	}
	copyEvents := append([]Event(nil), events...)
	sort.SliceStable(copyEvents, func(i, j int) bool { return copyEvents[i].Sequence < copyEvents[j].Sequence })
	for i := range copyEvents {
		copyEvents[i].Payload = redact(copyEvents[i].Payload)
	}
	plan := Plan{Version: 1, SessionID: sessionID, TenantID: tenantID, Events: copyEvents}
	unsigned := plan
	unsigned.Digest = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return Plan{}, err
	}
	sum := sha256.Sum256(raw)
	plan.Digest = hex.EncodeToString(sum[:])
	return plan, nil
}

func Verify(plan Plan) error {
	if plan.Version != 1 || plan.SessionID == "" || plan.TenantID == "" || plan.Digest == "" {
		return errors.New("invalid replay plan")
	}
	unsigned := plan
	unsigned.Digest = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != plan.Digest {
		return errors.New("replay plan digest mismatch")
	}
	return nil
}

func marshal(value any) []byte { raw, _ := json.Marshal(value); return raw }

func redact(value any) any {
	redacted := runbundle.Redact(marshal(value))
	var decoded any
	if json.Unmarshal(redacted, &decoded) == nil {
		return decoded
	}
	return string(redacted)
}
