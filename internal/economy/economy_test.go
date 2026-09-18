package economy

import (
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

func TestClassifyTaskDifficulty(t *testing.T) {
	// 1. Trivial
	tier1 := ClassifyTaskDifficulty("Explain this function comment", DifficultySignals{
		DiffChars:    500,
		ChangedFiles: 1,
	})
	if tier1 != TaskTierTrivial {
		t.Errorf("expected trivial, got %s", tier1)
	}

	// 2. Routine
	tier2 := ClassifyTaskDifficulty("Fix typo in readme and format imports", DifficultySignals{
		DiffChars:    3000,
		ChangedFiles: 2,
	})
	if tier2 != TaskTierRoutine {
		t.Errorf("expected routine, got %s", tier2)
	}

	// 3. Complex
	tier3 := ClassifyTaskDifficulty("Refactor database schema and mutex locking in worker", DifficultySignals{
		DiffChars:    15000,
		ChangedFiles: 4,
	})
	if tier3 != TaskTierComplex {
		t.Errorf("expected complex, got %s", tier3)
	}

	// 4. Critical
	tier4 := ClassifyTaskDifficulty("Audit OAuth tenant isolation and token refresh security", DifficultySignals{
		DiffChars:    5000,
		ChangedFiles: 2,
	})
	if tier4 != TaskTierCritical {
		t.Errorf("expected critical, got %s", tier4)
	}
}

func TestCheapestSuitable_RoutineSelectsCheap(t *testing.T) {
	ctrl := NewController()
	candidates := []ParticipantCandidate{
		{
			ID:           "cheap-reviewer",
			Roles:        []string{"reviewer", "correctness"},
			Economy:      config.EconomyProfile{Class: "efficient", RelativeCost: 1},
			Capabilities: config.AgentCapabilities{Review: true},
		},
		{
			ID:           "strong-reviewer",
			Roles:        []string{"reviewer", "correctness"},
			Economy:      config.EconomyProfile{Class: "capable", RelativeCost: 5},
			Capabilities: config.AgentCapabilities{Review: true},
		},
	}

	selected, dec, err := ctrl.SelectParticipant(
		"sess-1",
		"correctness",
		"",
		"Routine lint check",
		DifficultySignals{DiffChars: 1000},
		candidates,
		"cheapest_suitable",
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "cheap-reviewer" {
		t.Fatalf("expected cheap-reviewer to be selected, got %s", selected.ID)
	}
	if dec.Selected != "cheap-reviewer" {
		t.Fatalf("unexpected decision record: %+v", dec)
	}
}

func TestCheapestSuitable_EscalationAndDeescalation(t *testing.T) {
	ctrl := NewController()
	candidates := []ParticipantCandidate{
		{
			ID:           "cheap-reviewer",
			Roles:        []string{"reviewer", "correctness"},
			Economy:      config.EconomyProfile{Class: "efficient", RelativeCost: 1},
			Capabilities: config.AgentCapabilities{Review: true},
		},
		{
			ID:           "strong-reviewer",
			Roles:        []string{"reviewer", "correctness"},
			Economy:      config.EconomyProfile{Class: "capable", RelativeCost: 5},
			Capabilities: config.AgentCapabilities{Review: true},
		},
	}

	// 1. Initial routine task selects cheap-reviewer
	sel1, _, err := ctrl.SelectParticipant("sess-1", "correctness", "", "Routine check", DifficultySignals{}, candidates, "cheapest_suitable")
	if err != nil || sel1.ID != "cheap-reviewer" {
		t.Fatalf("expected cheap-reviewer, got %v (err: %v)", sel1, err)
	}

	// 2. Verification fails -> Escalate
	ctrl.Escalate("sess-1", "correctness")

	// 3. Next review selects strong-reviewer
	sel2, _, err := ctrl.SelectParticipant("sess-1", "correctness", "", "Follow-up verification", DifficultySignals{}, candidates, "cheapest_suitable")
	if err != nil || sel2.ID != "strong-reviewer" {
		t.Fatalf("expected strong-reviewer after escalation, got %v (err: %v)", sel2, err)
	}

	// 4. Issue is resolved -> De-escalate
	ctrl.Deescalate("sess-1", "correctness")

	// 5. Subsequent routine task selects cheap-reviewer again
	sel3, _, err := ctrl.SelectParticipant("sess-1", "correctness", "", "Subsequent routine task", DifficultySignals{}, candidates, "cheapest_suitable")
	if err != nil || sel3.ID != "cheap-reviewer" {
		t.Fatalf("expected cheap-reviewer after de-escalation, got %v (err: %v)", sel3, err)
	}
}

func TestDataSensitivity_PrivacyPrecedesCost(t *testing.T) {
	ctrl := NewController()
	candidates := []ParticipantCandidate{
		{
			ID:           "cloud-security",
			Roles:        []string{"reviewer", "security"},
			IsLocal:      false,                                                      // Cloud
			Economy:      config.EconomyProfile{Class: "efficient", RelativeCost: 1}, // Cheaper!
			Capabilities: config.AgentCapabilities{Review: true},
		},
		{
			ID:           "local-security",
			Roles:        []string{"reviewer", "security"},
			IsLocal:      true,                                                     // Local
			Economy:      config.EconomyProfile{Class: "capable", RelativeCost: 3}, // More expensive
			Capabilities: config.AgentCapabilities{Review: true},
		},
	}

	// Repository scope contains sensitive data -> Cloud candidate must be denied
	selected, dec, err := ctrl.SelectParticipant(
		"sess-priv",
		"security",
		"",
		"Security audit",
		DifficultySignals{HasSensitivePaths: true},
		candidates,
		"cheapest_suitable",
	)
	if err != nil {
		t.Fatal(err)
	}

	if selected.ID != "local-security" {
		t.Fatalf("expected local-security due to privacy constraint, got %s", selected.ID)
	}
	if len(dec.Eligible) != 1 || dec.Eligible[0] != "local-security" {
		t.Fatalf("expected only local-security in eligible list, got %+v", dec.Eligible)
	}
}
