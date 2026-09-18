package economy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type TaskTier string

const (
	TaskTierTrivial  TaskTier = "trivial"
	TaskTierRoutine  TaskTier = "routine"
	TaskTierComplex  TaskTier = "complex"
	TaskTierCritical TaskTier = "critical"
)

type DifficultySignals struct {
	OperationType     string
	WritableNeeded    bool
	ChangedFiles      int
	DiffChars         int
	ContextChars      int
	HasFailingTests   bool
	HasBuildFailures  bool
	OpenHighFindings  int
	HasSensitivePaths bool
	Scope             []string
}

type ParticipantCandidate struct {
	ID           string
	Adapter      string
	Roles        []string
	Writable     bool
	IsLocal      bool
	Economy      config.EconomyProfile
	Capabilities config.AgentCapabilities
	CallsCount   int
	AllowedPaths []string
	DeniedPaths  []string
}

type RoutingDecision struct {
	ID                  string    `json:"id"`
	SessionID           string    `json:"session_id"`
	RequestedCapability string    `json:"requested_capability"`
	TaskTier            TaskTier  `json:"task_tier"`
	Eligible            []string  `json:"eligible"`
	Selected            string    `json:"selected"`
	Reason              []string  `json:"reason"`
	Timestamp           time.Time `json:"timestamp"`
}

type Controller struct {
	mu          sync.RWMutex
	escalations map[string]int // sessionID:capability -> count
	recentTiers map[string]TaskTier
}

func NewController() *Controller {
	return &Controller{
		escalations: make(map[string]int),
		recentTiers: make(map[string]TaskTier),
	}
}

// ClassifyTaskDifficulty inspects deterministic signals and task text to determine effort tier.
func ClassifyTaskDifficulty(task string, signals DifficultySignals) TaskTier {
	taskLower := strings.ToLower(task)

	// Critical signals: security, crypto, auth, tenant isolation
	if strings.Contains(taskLower, "security") ||
		strings.Contains(taskLower, "oauth") ||
		strings.Contains(taskLower, "tenant") ||
		strings.Contains(taskLower, "crypto") ||
		strings.Contains(taskLower, "authorization") ||
		strings.Contains(taskLower, "authentication") ||
		strings.Contains(taskLower, "token refresh") ||
		strings.Contains(taskLower, "race condition") ||
		signals.OpenHighFindings > 0 {
		return TaskTierCritical
	}

	// Complex signals: concurrency, migration, large diffs, repeated test failures
	if strings.Contains(taskLower, "concurrency") ||
		strings.Contains(taskLower, "mutex") ||
		strings.Contains(taskLower, "migration") ||
		strings.Contains(taskLower, "database schema") ||
		signals.ChangedFiles > 5 ||
		signals.DiffChars > 30000 ||
		(signals.HasFailingTests && signals.HasBuildFailures) {
		return TaskTierComplex
	}

	// Trivial signals: simple explanation, formatting, small read-only query
	if (strings.Contains(taskLower, "explain") ||
		strings.Contains(taskLower, "summarize") ||
		strings.Contains(taskLower, "comment") ||
		strings.Contains(taskLower, "typo")) &&
		signals.DiffChars < 2000 && signals.ChangedFiles <= 1 && !signals.WritableNeeded {
		return TaskTierTrivial
	}

	// Default to routine
	return TaskTierRoutine
}

// SelectParticipant implements the cheapest_suitable selection pipeline.
func (c *Controller) SelectParticipant(
	sessionID string,
	capability string,
	explicitPeer string,
	task string,
	signals DifficultySignals,
	candidates []ParticipantCandidate,
	policy string,
) (*ParticipantCandidate, *RoutingDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tier := ClassifyTaskDifficulty(task, signals)
	escalationKey := fmt.Sprintf("%s:%s", sessionID, capability)
	isEscalated := c.escalations[escalationKey] > 0

	decision := &RoutingDecision{
		ID:                  fmt.Sprintf("rd_%d", time.Now().UnixNano()),
		SessionID:           sessionID,
		RequestedCapability: capability,
		TaskTier:            tier,
		Timestamp:           time.Now().UTC(),
	}

	// If an explicit peer was requested
	if explicitPeer != "" {
		for _, cand := range candidates {
			if cand.ID == explicitPeer {
				decision.Eligible = []string{explicitPeer}
				decision.Selected = explicitPeer
				decision.Reason = []string{"explicit_peer_specified"}
				return &cand, decision, nil
			}
		}
		return nil, nil, &protocol.PeerUnavailableError{Peer: explicitPeer, Reason: "requested peer not registered"}
	}

	var eligible []ParticipantCandidate
	var reasons []string

	for _, cand := range candidates {
		// 1. Capability check
		hasCap := false
		if capability == "" {
			hasCap = true
		} else {
			for _, r := range cand.Roles {
				if strings.EqualFold(r, capability) {
					hasCap = true
					break
				}
			}
			if !hasCap {
				switch strings.ToLower(capability) {
				case "review":
					hasCap = cand.Capabilities.Review
				case "answer_questions", "ask":
					hasCap = cand.Capabilities.AnswerQuestions
				case "submit_evidence", "evidence":
					hasCap = cand.Capabilities.SubmitEvidence
				case "executor", "write":
					hasCap = cand.Capabilities.WriteRepository && cand.Writable
				}
			}
		}
		if !hasCap {
			continue
		}

		// 2. Data policy & sensitivity check
		if signals.HasSensitivePaths && !cand.IsLocal {
			// Cloud participant denied due to sensitive repository scope
			continue
		}

		// 3. Workspace writer invariant check
		if signals.WritableNeeded && !cand.Writable {
			continue
		}

		eligible = append(eligible, cand)
	}

	if len(eligible) == 0 {
		return nil, nil, &protocol.CapabilityUnsupportedError{
			Agent:      "any",
			Capability: capability,
		}
	}

	for _, cand := range eligible {
		decision.Eligible = append(decision.Eligible, cand.ID)
	}

	if policy == "" {
		policy = "cheapest_suitable"
	}

	var selected ParticipantCandidate

	switch policy {
	case "cheapest_suitable":
		// Filter by tier or escalation
		var tierMatches []ParticipantCandidate
		if isEscalated || tier == TaskTierCritical || tier == TaskTierComplex {
			// Prefer capable or premium participants
			for _, cand := range eligible {
				class := strings.ToLower(cand.Economy.Class)
				if class == "capable" || class == "premium" || cand.Economy.RelativeCost >= 3 {
					tierMatches = append(tierMatches, cand)
				}
			}
			if len(tierMatches) > 0 {
				reasons = append(reasons, "tier_escalated_capable_match")
			}
		} else {
			// Routine or trivial: prefer local, efficient, low cost
			for _, cand := range eligible {
				class := strings.ToLower(cand.Economy.Class)
				if class == "local" || class == "efficient" || cand.Economy.RelativeCost <= 2 {
					tierMatches = append(tierMatches, cand)
				}
			}
			if len(tierMatches) > 0 {
				reasons = append(reasons, "tier_efficient_match")
			}
		}

		if len(tierMatches) == 0 {
			// Fall back to all eligible candidates
			tierMatches = eligible
		}

		// Sort by relative cost ascending, then call count
		sort.Slice(tierMatches, func(i, j int) bool {
			ci := tierMatches[i].Economy.RelativeCost
			cj := tierMatches[j].Economy.RelativeCost
			if ci != cj {
				return ci < cj
			}
			return tierMatches[i].CallsCount < tierMatches[j].CallsCount
		})

		selected = tierMatches[0]
		reasons = append(reasons, "capability_match", "policy_allowed", "within_budget", "lowest_relative_cost")

	case "round_robin":
		sort.Slice(eligible, func(i, j int) bool {
			return eligible[i].CallsCount < eligible[j].CallsCount
		})
		selected = eligible[0]
		reasons = append(reasons, "round_robin_policy")

	case "least_busy":
		sort.Slice(eligible, func(i, j int) bool {
			return eligible[i].CallsCount < eligible[j].CallsCount
		})
		selected = eligible[0]
		reasons = append(reasons, "least_busy_policy")

	default: // "first"
		sort.Slice(eligible, func(i, j int) bool {
			return eligible[i].ID < eligible[j].ID
		})
		selected = eligible[0]
		reasons = append(reasons, "first_policy")
	}

	decision.Selected = selected.ID
	decision.Reason = reasons
	c.recentTiers[sessionID] = tier

	return &selected, decision, nil
}

// Escalate registers an escalation for a capability in a session when verification fails.
func (c *Controller) Escalate(sessionID, capability string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s:%s", sessionID, capability)
	c.escalations[key]++
}

// Deescalate resets the escalation state once a difficult issue has been resolved.
func (c *Controller) Deescalate(sessionID, capability string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s:%s", sessionID, capability)
	delete(c.escalations, key)
}
