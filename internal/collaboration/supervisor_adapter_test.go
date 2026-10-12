package collaboration

import (
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func TestSupervisorAgentDirectory_ReflectsConfiguredAgents(t *testing.T) {
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"codex": {
				Kind: "codex",
				Capabilities: config.AgentCapabilities{
					ReadRepository: true,
					Review:         true,
				},
			},
		},
	}
	e := &Engine{cfg: cfg}
	dir := NewSupervisorAgentDirectory(e)

	var _ supervisor.AgentDirectory = dir

	if !dir.AgentExists("codex") {
		t.Fatalf("expected codex to exist")
	}
	if dir.AgentExists("ghost") {
		t.Fatalf("expected ghost to not exist")
	}

	caps, ok := dir.Capabilities("codex")
	if !ok {
		t.Fatalf("expected codex capabilities to resolve")
	}
	want := map[string]bool{"read_repository": true, "review": true}
	if len(caps) != len(want) {
		t.Fatalf("unexpected capability set: %v", caps)
	}
	for _, c := range caps {
		if !want[c] {
			t.Fatalf("unexpected capability %q", c)
		}
	}

	if _, ok := dir.Capabilities("ghost"); ok {
		t.Fatalf("expected ghost capabilities lookup to fail")
	}
}

func TestSupervisorAgentDirectory_DoesNotImplementTenantDirectory(t *testing.T) {
	// internal/collaboration has no tenant concept yet; the adapter must
	// not pretend to resolve tenants until a real tenant model exists.
	var dir supervisor.AgentDirectory = NewSupervisorAgentDirectory(&Engine{cfg: &config.Config{}})
	if _, ok := dir.(supervisor.TenantDirectory); ok {
		t.Fatalf("SupervisorAgentDirectory must not implement TenantDirectory until collaboration has a real tenant model")
	}
}
