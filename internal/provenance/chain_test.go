package provenance

import (
	"testing"
	"time"
)

func TestChainIsTamperEvident(t *testing.T) {
	c := New()
	_, _ = c.Append("intent", "user", map[string]string{"task": "review"}, time.Unix(1, 0))
	_, _ = c.Append("execution", "agent", map[string]string{"tool": "peer.list"}, time.Unix(2, 0))
	if err := c.Verify(); err != nil {
		t.Fatal(err)
	}
	events := c.Events()
	events[0].Digest = "tampered"
	if events[0].Digest == c.Events()[0].Digest {
		t.Fatal("Events must be copied")
	}
}
