package collaboration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/economy"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

type ActivationControllerConfig struct {
	Store           store.Store
	Config          *config.Config
	Projector       *contextpack.Projector
	Harnesses       map[string]agent.Harness
	EconomyCtrl     *economy.Controller
	DefaultCooldown time.Duration
}

type ActivationController struct {
	store           store.Store
	cfg             *config.Config
	projector       *contextpack.Projector
	harnesses       map[string]agent.Harness
	economyCtrl     *economy.Controller
	defaultCooldown time.Duration

	mu             sync.Mutex
	lastActivation map[string]time.Time // key: spaceID:participantID -> time
	recentHashes   map[string][]string  // key: spaceID:threadID -> list of content hashes
	recentSenders  map[string][]string  // key: spaceID:threadID -> list of participant IDs
}

func NewActivationController(cfg ActivationControllerConfig) *ActivationController {
	cd := cfg.DefaultCooldown
	if cd <= 0 {
		cd = 500 * time.Millisecond
	}
	return &ActivationController{
		store:           cfg.Store,
		cfg:             cfg.Config,
		projector:       cfg.Projector,
		harnesses:       cfg.Harnesses,
		economyCtrl:     cfg.EconomyCtrl,
		defaultCooldown: cd,
		lastActivation:  make(map[string]time.Time),
		recentHashes:    make(map[string][]string),
		recentSenders:   make(map[string][]string),
	}
}

// ShouldActivate evaluates whether a participant should be automatically activated.
func (ac *ActivationController) ShouldActivate(ctx context.Context, space *protocol.CollaborationSpace, p *protocol.SpaceParticipant, env *protocol.PeerEnvelope, evt *protocol.CollaborationEvent) (bool, string, error) {
	// 1. Space Lifecycle State check
	if space.LifecycleState == protocol.SpaceStatePaused {
		return false, "space_paused", &protocol.ParticipantPausedError{Participant: p.ID, SpaceID: space.ID}
	}
	if space.LifecycleState == protocol.SpaceStateStopped || space.LifecycleState == protocol.SpaceStateArchived {
		return false, "space_stopped", &protocol.HumanInterruptedError{Reason: "collaboration space is stopped"}
	}

	// 2. Participant Mode check
	switch p.Mode {
	case protocol.ParticipantModePaused:
		return false, "participant_paused", &protocol.ParticipantPausedError{Participant: p.ID, SpaceID: space.ID}
	case protocol.ParticipantModePassive:
		// Passive participants never auto-activate; their inbox receives messages
		return false, "passive_mode", nil
	case protocol.ParticipantModeOnDemand:
		// Activate ONLY if directly mentioned or explicitly targeted
		isTargeted := false
		if env != nil {
			if env.To == p.ID || strings.HasPrefix(env.To, "participant:"+p.ID) {
				isTargeted = true
			}
			for _, m := range env.Mentions {
				if m == p.ID || m == "@"+p.ID {
					isTargeted = true
					break
				}
			}
		}
		if !isTargeted {
			return false, "on_demand_not_mentioned", nil
		}
	case protocol.ParticipantModeActive:
		// Active mode activates on mention or subscribed events
	}

	// 3. Cooldown check
	ac.mu.Lock()
	actKey := fmt.Sprintf("%s:%s", space.ID, p.ID)
	lastTime, exists := ac.lastActivation[actKey]
	if exists && time.Since(lastTime) < ac.defaultCooldown {
		ac.mu.Unlock()
		return false, "cooldown_active", nil
	}
	ac.mu.Unlock()

	// 4. Cycle Detection
	if env != nil {
		if err := ac.detectCycle(space.ID, env.ThreadID, p.ID, env); err != nil {
			return false, "cycle_detected", err
		}
	}

	return true, "", nil
}

func (ac *ActivationController) detectCycle(spaceID, threadID, candidateID string, env *protocol.PeerEnvelope) error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	threadKey := fmt.Sprintf("%s:%s", spaceID, threadID)
	hash := hashPayload(env.Payload)

	hashes := ac.recentHashes[threadKey]
	senders := ac.recentSenders[threadKey]

	// Check if this payload has been repeated > 2 times in recent history
	repeatCount := 0
	for _, h := range hashes {
		if h == hash {
			repeatCount++
		}
	}
	if repeatCount >= 2 {
		return &protocol.CollaborationCycleDetectedError{
			Participants: []string{env.From, candidateID},
			Depth:        env.Depth,
		}
	}

	// Check ping-pong cycle: candidate -> sender -> candidate -> sender with depth > 3
	if env.Depth > 3 && len(senders) >= 3 {
		n := len(senders)
		if senders[n-1] == env.From && senders[n-2] == candidateID && senders[n-3] == env.From {
			return &protocol.CollaborationCycleDetectedError{
				Participants: []string{env.From, candidateID},
				Depth:        env.Depth,
			}
		}
	}

	// Update history
	ac.recentHashes[threadKey] = append(hashes, hash)
	if len(ac.recentHashes[threadKey]) > 10 {
		ac.recentHashes[threadKey] = ac.recentHashes[threadKey][1:]
	}
	ac.recentSenders[threadKey] = append(senders, candidateID)
	if len(ac.recentSenders[threadKey]) > 10 {
		ac.recentSenders[threadKey] = ac.recentSenders[threadKey][1:]
	}

	return nil
}

func hashPayload(p []byte) string {
	h := sha256.Sum256(p)
	return hex.EncodeToString(h[:8])
}

// CheckPrivacyPolicy verifies if the candidate participant is authorized for the given scope.
func (ac *ActivationController) CheckPrivacyPolicy(candidate *protocol.SpaceParticipant, scope []string) (bool, string) {
	for _, file := range scope {
		isSensitive := strings.Contains(file, "secret") || strings.HasSuffix(file, ".key") || strings.HasSuffix(file, ".pem") || strings.Contains(file, "token")
		if isSensitive {
			// Cloud adapters (e.g. codex, claude) without local flag cannot access sensitive files
			if candidate.Adapter == "codex" || candidate.Adapter == "claude" {
				return false, fmt.Sprintf("policy denied: cloud adapter %q cannot access sensitive file %q", candidate.Adapter, file)
			}
		}
	}
	return true, ""
}

// MarkActivated updates the activation cooldown timestamp.
func (ac *ActivationController) MarkActivated(spaceID, participantID string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.lastActivation[fmt.Sprintf("%s:%s", spaceID, participantID)] = time.Now().UTC()
}
