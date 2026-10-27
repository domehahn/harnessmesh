package agentreliability

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreakerRecoveryAndQuarantine(t *testing.T) {
	now := time.Now()
	c := New(Config{FailureThreshold: 2, QuarantineAfter: 3, RecoveryTimeout: time.Second, QuarantineDuration: time.Minute})
	c.Failure("codex", errors.New("one"), now)
	c.Failure("codex", errors.New("two"), now)
	if err := c.Before("codex", now); err == nil {
		t.Fatal("expected open circuit")
	}
	c.Failure("codex", errors.New("three"), now)
	if c.Snapshot("codex").State != Quarantined {
		t.Fatal("expected quarantine")
	}
	if err := c.Before("codex", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	c.Success("codex")
	if c.Snapshot("codex").State != Closed {
		t.Fatal("expected recovery")
	}
}
