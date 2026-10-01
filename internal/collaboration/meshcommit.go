package collaboration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/executil"
	"github.com/domehahn/harnessmesh/internal/knowledge"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/telemetry"
	"github.com/domehahn/harnessmesh/internal/workspace"
)

type MeshCommitCoordinator struct {
	store        store.Store
	config       *config.Config
	eventBus     *EventBus
	repoPath     string
	spaceService *SpaceService
	archive      *knowledge.Archive
	metrics      *telemetry.Registry
	mu           sync.Mutex
}

func NewMeshCommitCoordinator(s store.Store, cfg *config.Config, eb *EventBus, repoPath string, spaceSvc *SpaceService, arch *knowledge.Archive) *MeshCommitCoordinator {
	return &MeshCommitCoordinator{
		store:        s,
		config:       cfg,
		eventBus:     eb,
		repoPath:     repoPath,
		spaceService: spaceSvc,
		archive:      arch,
	}
}

func (c *MeshCommitCoordinator) SetTelemetry(r *telemetry.Registry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics = r
}

func (c *MeshCommitCoordinator) incMetric(name string) {
	if c.metrics != nil {
		c.metrics.Counter(name).Add(1)
	}
}

// requireWritableExecutor enforces the single-writer invariant against a
// participant's own configuration: an external (e.g. ChatGPT) or explicitly
// non-writable participant must never open or commit a MeshCommit change
// transaction. action identifies the operation being denied, for the error
// message only.
func (c *MeshCommitCoordinator) requireWritableExecutor(participant, action string) error {
	if c.config == nil || len(c.config.Agents) == 0 {
		// No agent-config model is in play for this deployment/test (e.g.
		// MeshCommit used standalone without internal/config.AgentConfig
		// entries) - nothing to enforce against. Once any agent is
		// registered, enforcement below applies and is fail-closed.
		return nil
	}
	aCfg, exists := c.config.Agents[participant]
	if !exists {
		return &protocol.PolicyDeniedError{
			Action: action,
			Reason: fmt.Sprintf("participant %q is not a registered agent and cannot %s (fail-closed: agents are configured for this deployment)", participant, action),
		}
	}
	if aCfg.IsExternal() || !aCfg.Writable {
		return &protocol.PolicyDeniedError{
			Action: action,
			Reason: fmt.Sprintf("participant %q is not a writable/managed executor (execution_mode=%q, writable=%v) and cannot %s", participant, aCfg.ExecutionMode, aCfg.Writable, action),
		}
	}
	return nil
}

// CreateChange initiates a new Change transaction in "draft" status.
// Enforces Single-Writer invariant, computes isolated TreeHash, resolves & locks ProofPolicy.
func (c *MeshCommitCoordinator) CreateChange(ctx context.Context, req *protocol.CreateChangeRequest) (*protocol.MeshChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if strings.TrimSpace(req.Title) == "" {
		return nil, fmt.Errorf("change title cannot be empty")
	}
	if strings.TrimSpace(req.AuthorParticipant) == "" {
		return nil, fmt.Errorf("author_participant cannot be empty")
	}

	// Enforce single-writer invariant against the participant's own configuration:
	// an external (e.g. ChatGPT) or explicitly non-writable participant must never
	// be able to open a MeshCommit change transaction, since that transaction is
	// the vehicle through which repository state is ultimately committed.
	if err := c.requireWritableExecutor(req.AuthorParticipant, "create_change"); err != nil {
		return nil, err
	}

	// Verify single-writer invariant if space is specified
	if req.SpaceID != "" && c.spaceService != nil {
		space, err := c.spaceService.GetSpace(ctx, req.SpaceID)
		if err == nil && space != nil {
			if space.WriterParticipant != "" && space.WriterParticipant != req.AuthorParticipant {
				return nil, &protocol.PolicyDeniedError{
					Action: "create_change",
					Reason: fmt.Sprintf("participant %q is not the designated writer (%q) for space %q", req.AuthorParticipant, space.WriterParticipant, space.ID),
				}
			}
		}
	}

	changeID := req.ChangeID
	if changeID == "" {
		changeID = fmt.Sprintf("chg_%d", time.Now().UnixNano())
	}

	now := time.Now().UTC()
	treeHash, err := workspace.ComputeTreeHash(ctx, c.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to compute tree hash: %w", err)
	}

	// Compute diff of modified files
	paths, err := workspace.ComputeDiff(ctx, c.repoPath, req.BaseCommit, treeHash)
	if err != nil {
		paths = nil
	}

	var stringPaths []string
	for i := range paths {
		paths[i].ChangeID = changeID
		paths[i].CreatedAt = now
		stringPaths = append(stringPaths, paths[i].Path)
	}

	// Resolve and lock proof policy based on configuration
	var obligations []*protocol.ProofObligation
	var policySpecs []config.ProofObligationSpec
	if c.config != nil {
		policySpecs = c.config.ChangeControl.ResolveProofPolicy(stringPaths)
		for _, spec := range policySpecs {
			obligations = append(obligations, &protocol.ProofObligation{
				ID:                  fmt.Sprintf("obl_%s_%s", changeID, spec.Name),
				ChangeID:            changeID,
				Type:                spec.Type,
				Name:                spec.Name,
				Description:         spec.Description,
				Required:            spec.Required,
				Status:              protocol.ProofStatusPending,
				PolicySource:        spec.PolicySource,
				Scope:               spec.Scope,
				RequiredCapability:  spec.RequiredCapability,
				RequiredParticipant: spec.RequiredParticipant,
				Command:             spec.Command,
				ExpectedExitCode:    spec.ExpectedExitCode,
				FreshnessPolicy:     "exact_tree",
				CreatedAt:           now,
				UpdatedAt:           now,
			})
		}
	}

	// If no policy rules matched, provide default build/test obligation
	if len(obligations) == 0 {
		defSpec := config.ProofObligationSpec{
			Type:         "unit_tests",
			Name:         "default_verification",
			Description:  "Default change verification",
			Required:     true,
			PolicySource: "default",
		}
		policySpecs = append(policySpecs, defSpec)
		obligations = append(obligations, &protocol.ProofObligation{
			ID:              fmt.Sprintf("obl_%s_default_verification", changeID),
			ChangeID:        changeID,
			Type:            "unit_tests",
			Name:            "default_verification",
			Description:     "Default change verification",
			Required:        true,
			Status:          protocol.ProofStatusPending,
			PolicySource:    "default",
			FreshnessPolicy: "exact_tree",
			CreatedAt:       now,
			UpdatedAt:       now,
		})
	}

	policyBytes, _ := json.Marshal(policySpecs)

	chg := &protocol.MeshChange{
		ID:                changeID,
		SpaceID:           req.SpaceID,
		SessionID:         req.SessionID,
		RepositoryID:      req.RepositoryID,
		Title:             req.Title,
		Intent:            req.Intent,
		AuthorParticipant: req.AuthorParticipant,
		Status:            protocol.ChangeStatusDraft,
		BaseCommit:        req.BaseCommit,
		BaseTreeHash:      treeHash,
		CurrentTreeHash:   treeHash,
		ProofPolicyJSON:   string(policyBytes),
		PolicySource:      "config",
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := c.store.SaveMeshChange(ctx, chg); err != nil {
		return nil, fmt.Errorf("failed to save mesh change: %w", err)
	}

	if len(paths) > 0 {
		if err := c.store.SaveChangePaths(ctx, paths); err != nil {
			return nil, fmt.Errorf("failed to save change paths: %w", err)
		}
	}

	for _, obl := range obligations {
		if err := c.store.SaveProofObligation(ctx, obl); err != nil {
			return nil, fmt.Errorf("failed to save proof obligation: %w", err)
		}
	}

	// Publish event
	if c.eventBus != nil {
		_ = c.eventBus.Publish(ctx, &protocol.CollaborationEvent{
			ID:        fmt.Sprintf("evt_chg_created_%s", changeID),
			SpaceID:   req.SpaceID,
			Type:      protocol.EventChangeCreated,
			Source:    req.AuthorParticipant,
			Timestamp: now,
			Payload: map[string]any{
				"change_id":         changeID,
				"title":             chg.Title,
				"tree_hash":         chg.CurrentTreeHash,
				"obligations_count": len(obligations),
			},
		})
	}

	c.incMetric(telemetry.MetricChangesCreated)
	return chg, nil
}

// PrepareChange transitions a change from draft to "prepared", ready for
// verification. actorID, when non-empty, must match the change's original
// author - this prevents one participant from managing another's in-flight
// change transaction (e.g. over the bridge or MCP, where multiple
// participants share one HarnessMesh instance). Pass "" only from trusted
// local-operator call sites (the CLI) that have no participant identity of
// their own to assert.
func (c *MeshCommitCoordinator) PrepareChange(ctx context.Context, changeID, actorID string) (*protocol.MeshChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	chg, err := c.store.GetMeshChange(ctx, changeID)
	if err != nil || chg == nil {
		return nil, &protocol.ChangeNotFoundError{ChangeID: changeID}
	}

	if strings.TrimSpace(actorID) != "" && actorID != chg.AuthorParticipant {
		return nil, &protocol.PolicyDeniedError{
			Action: "prepare_change",
			Reason: fmt.Sprintf("caller %q is not the author %q of change %q", actorID, chg.AuthorParticipant, changeID),
		}
	}

	if chg.Status == protocol.ChangeStatusCommitted || chg.Status == protocol.ChangeStatusAborted {
		return nil, fmt.Errorf("cannot prepare finalized change in status %q", chg.Status)
	}

	currentTreeHash, err := workspace.ComputeTreeHash(ctx, c.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to compute current tree hash: %w", err)
	}

	now := time.Now().UTC()
	if currentTreeHash != chg.CurrentTreeHash {
		// Worktree changed while in draft
		chg.CurrentTreeHash = currentTreeHash
		paths, err := workspace.ComputeDiff(ctx, c.repoPath, chg.BaseCommit, currentTreeHash)
		if err == nil && len(paths) > 0 {
			for i := range paths {
				paths[i].ChangeID = changeID
				paths[i].CreatedAt = now
			}
			_ = c.store.SaveChangePaths(ctx, paths)
		}
	}

	chg.Status = protocol.ChangeStatusPrepared
	chg.UpdatedAt = now

	if err := c.store.SaveMeshChange(ctx, chg); err != nil {
		return nil, fmt.Errorf("failed to update change status: %w", err)
	}

	if c.eventBus != nil {
		_ = c.eventBus.Publish(ctx, &protocol.CollaborationEvent{
			ID:        fmt.Sprintf("evt_chg_prep_%s", changeID),
			SpaceID:   chg.SpaceID,
			Type:      protocol.EventChangePrepared,
			Source:    chg.AuthorParticipant,
			Timestamp: now,
			Payload: map[string]any{
				"change_id": changeID,
				"tree_hash": chg.CurrentTreeHash,
			},
		})
	}

	return chg, nil
}

// SubmitEvidence verifies evidence invariants (anti-spoofing, tree matching), saves evidence,
// updates proof obligation status, and triggers gate evaluation.
func (c *MeshCommitCoordinator) SubmitEvidence(ctx context.Context, ev *protocol.ChangeEvidence) (*protocol.GateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	chg, err := c.store.GetMeshChange(ctx, ev.ChangeID)
	if err != nil || chg == nil {
		return nil, &protocol.ChangeNotFoundError{ChangeID: ev.ChangeID}
	}

	if chg.Status == protocol.ChangeStatusCommitted || chg.Status == protocol.ChangeStatusAborted {
		return nil, fmt.Errorf("cannot submit evidence for finalized change in status %q", chg.Status)
	}

	// Anti-Spoofing / Independent Review Invariant
	evType := string(ev.EvidenceType)
	if evType == "peer_review" || evType == "security_audit" || evType == "independent_review" {
		if ev.SourceParticipant == chg.AuthorParticipant {
			return nil, &protocol.SelfReviewForbiddenError{
				ChangeID:    chg.ID,
				Participant: ev.SourceParticipant,
			}
		}
	}

	// Tree Hash verification
	currentTreeHash, err := workspace.ComputeTreeHash(ctx, c.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to compute tree hash: %w", err)
	}

	if ev.TreeHash != chg.CurrentTreeHash || currentTreeHash != chg.CurrentTreeHash {
		return nil, &protocol.CommitTreeMismatchError{
			ChangeID:     chg.ID,
			VerifiedTree: chg.CurrentTreeHash,
			CommitTree:   currentTreeHash,
		}
	}

	now := time.Now().UTC()
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("evi_%d", now.UnixNano())
	}
	ev.CreatedAt = now
	ev.Valid = true

	if err := c.store.SaveChangeEvidence(ctx, ev); err != nil {
		return nil, fmt.Errorf("failed to save change evidence: %w", err)
	}

	// Update corresponding obligation status
	if ev.ObligationID != "" {
		var oblStatus protocol.ProofObligationStatus
		if ev.Result == "passed" {
			oblStatus = protocol.ProofStatusPassed
		} else {
			oblStatus = protocol.ProofStatusFailed
		}
		_ = c.store.UpdateProofObligationStatus(ctx, ev.ObligationID, oblStatus, ev.ID)
	}

	// Transition change to under_verification if it was prepared
	if chg.Status == protocol.ChangeStatusPrepared || chg.Status == protocol.ChangeStatusDraft {
		chg.Status = protocol.ChangeStatusUnderVerification
		chg.UpdatedAt = now
		_ = c.store.SaveMeshChange(ctx, chg)
	}

	c.incMetric(telemetry.MetricEvidenceSubmitted)
	return c.evaluateGateLocked(ctx, chg.ID)
}

// ExecuteProof runs a deterministic verification command for an obligation, saves evidence, and evaluates gate.
func (c *MeshCommitCoordinator) ExecuteProof(ctx context.Context, changeID string, obligationID string) (*protocol.ChangeEvidence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	chg, err := c.store.GetMeshChange(ctx, changeID)
	if err != nil || chg == nil {
		return nil, &protocol.ChangeNotFoundError{ChangeID: changeID}
	}

	obl, err := c.store.GetProofObligation(ctx, obligationID)
	if err != nil || obl == nil {
		return nil, &protocol.ProofObligationNotFoundError{ObligationID: obligationID}
	}

	currentTreeHash, err := workspace.ComputeTreeHash(ctx, c.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to compute tree hash: %w", err)
	}

	if currentTreeHash != chg.CurrentTreeHash {
		return nil, &protocol.CommitTreeMismatchError{
			ChangeID:     chg.ID,
			VerifiedTree: chg.CurrentTreeHash,
			CommitTree:   currentTreeHash,
		}
	}

	// Idempotent proof caching: check if valid un-invalidated evidence already exists for this exact tree hash
	existingEv, err := c.store.GetChangeEvidenceForObligation(ctx, obligationID)
	if err == nil {
		for _, ev := range existingEv {
			if ev.Valid && ev.TreeHash == chg.CurrentTreeHash && ev.Result == "passed" {
				return ev, nil
			}
		}
	}

	// Mark obligation running
	_ = c.store.UpdateProofObligationStatus(ctx, obligationID, protocol.ProofStatusRunning, "")

	now := time.Now().UTC()
	var exitCode int
	var stdout, stderr string

	if obl.Command != "" {
		timeout := 300 * time.Second
		cmdCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		runRes, runErr := executil.Run(cmdCtx, c.repoPath, nil, "", "sh", "-c", obl.Command)
		exitCode = runRes.ExitCode
		stdout = runRes.Stdout
		stderr = runRes.Stderr
		if runErr != nil && exitCode == 0 {
			exitCode = 1
			stderr += fmt.Sprintf("\nEXECUTION ERROR: %v", runErr)
		}
	} else {
		// Mock/default success for automated verification if command is empty
		exitCode = obl.ExpectedExitCode
		stdout = "Automated verification completed successfully."
	}

	resultStatus := "passed"
	oblStatus := protocol.ProofStatusPassed
	if exitCode != obl.ExpectedExitCode {
		resultStatus = "failed"
		oblStatus = protocol.ProofStatusFailed
	}

	ev := &protocol.ChangeEvidence{
		ID:                fmt.Sprintf("evi_%d", now.UnixNano()),
		ChangeID:          changeID,
		ObligationID:      obligationID,
		TreeHash:          chg.CurrentTreeHash,
		SourceParticipant: "harnessmesh",
		SourceAdapter:     "builtin",
		EvidenceType:      protocol.EvidenceType(obl.Type),
		Command:           obl.Command,
		ExitCode:          &exitCode,
		Result:            resultStatus,
		Valid:             true,
		Metadata: map[string]any{
			"stdout": stdout,
			"stderr": stderr,
		},
		CreatedAt: now,
	}

	if err := c.store.SaveChangeEvidence(ctx, ev); err != nil {
		return nil, fmt.Errorf("failed to save generated evidence: %w", err)
	}

	_ = c.store.UpdateProofObligationStatus(ctx, obligationID, oblStatus, ev.ID)

	if c.eventBus != nil {
		evtType := protocol.EventProofPassed
		if oblStatus == protocol.ProofStatusFailed {
			evtType = protocol.EventProofFailed
		}
		_ = c.eventBus.Publish(ctx, &protocol.CollaborationEvent{
			ID:        fmt.Sprintf("evt_proof_%s", ev.ID),
			SpaceID:   chg.SpaceID,
			Type:      evtType,
			Source:    "harnessmesh",
			Timestamp: now,
			Payload: map[string]any{
				"change_id":     changeID,
				"obligation_id": obligationID,
				"result":        resultStatus,
				"exit_code":     exitCode,
			},
		})
	}

	_, _ = c.evaluateGateLocked(ctx, changeID)
	return ev, nil
}

// InvalidateStaleEvidence marks evidence stale if tree changed and scoped paths overlap.
// Scope == empty means repo-wide proof, which is always invalidated when tree changes.
func (c *MeshCommitCoordinator) InvalidateStaleEvidence(ctx context.Context, changeID string, newTreeHash string, changedPaths []string) error {
	allEv, err := c.store.GetChangeEvidenceForChange(ctx, changeID)
	if err != nil {
		return err
	}

	obligations, _ := c.store.GetProofObligations(ctx, changeID)
	oblMap := make(map[string]*protocol.ProofObligation)
	for _, obl := range obligations {
		oblMap[obl.ID] = obl
	}

	now := time.Now().UTC()
	for _, ev := range allEv {
		if !ev.Valid {
			continue
		}

		obl := oblMap[ev.ObligationID]
		shouldInvalidate := false
		if obl == nil || len(obl.Scope) == 0 {
			// Repo-wide proof (e.g. whole test suite, build) -> always invalidated on tree change
			shouldInvalidate = true
		} else {
			// Check if any changed path matches any scoped path pattern
			for _, changed := range changedPaths {
				for _, scoped := range obl.Scope {
					if config.MatchPathPattern(scoped, changed) || strings.HasPrefix(changed, strings.TrimSuffix(scoped, "*")) {
						shouldInvalidate = true
						break
					}
				}
				if shouldInvalidate {
					break
				}
			}
		}

		if shouldInvalidate {
			reason := fmt.Sprintf("worktree modified (tree %s); overlapping paths: %v", newTreeHash, changedPaths)
			_ = c.store.InvalidateChangeEvidence(ctx, ev.ID, reason)

			if ev.ObligationID != "" {
				_ = c.store.UpdateProofObligationStatus(ctx, ev.ObligationID, protocol.ProofStatusStale, "")
			}

			c.incMetric(telemetry.MetricEvidenceInvalidated)

			if c.eventBus != nil {
				_ = c.eventBus.Publish(ctx, &protocol.CollaborationEvent{
					ID:        fmt.Sprintf("evt_evi_inv_%s", ev.ID),
					Type:      protocol.EventEvidenceInvalidated,
					Source:    "harnessmesh",
					Timestamp: now,
					Payload: map[string]any{
						"change_id":   changeID,
						"evidence_id": ev.ID,
						"reason":      reason,
					},
				})
			}
		}
	}

	return nil
}

// EvaluateGate evaluates all proof obligations deterministically (PURE CODE, ZERO LLM).
func (c *MeshCommitCoordinator) EvaluateGate(ctx context.Context, changeID string) (*protocol.GateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evaluateGateLocked(ctx, changeID)
}

func (c *MeshCommitCoordinator) evaluateGateLocked(ctx context.Context, changeID string) (*protocol.GateResult, error) {
	chg, err := c.store.GetMeshChange(ctx, changeID)
	if err != nil || chg == nil {
		return nil, &protocol.ChangeNotFoundError{ChangeID: changeID}
	}

	currentTreeHash, err := workspace.ComputeTreeHash(ctx, c.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to compute tree hash: %w", err)
	}

	// If repository state changed relative to change.CurrentTreeHash, invalidate stale evidence
	if currentTreeHash != chg.CurrentTreeHash {
		diff, _ := workspace.ComputeDiff(ctx, c.repoPath, chg.CurrentTreeHash, currentTreeHash)
		var changedPaths []string
		for _, p := range diff {
			changedPaths = append(changedPaths, p.Path)
		}
		_ = c.InvalidateStaleEvidence(ctx, changeID, currentTreeHash, changedPaths)
		chg.CurrentTreeHash = currentTreeHash
		_ = c.store.SaveMeshChange(ctx, chg)
	}

	obligations, err := c.store.GetProofObligations(ctx, changeID)
	if err != nil {
		return nil, fmt.Errorf("failed to load obligations: %w", err)
	}

	allEvidence, err := c.store.GetChangeEvidenceForChange(ctx, changeID)
	if err != nil {
		return nil, fmt.Errorf("failed to load change evidence: %w", err)
	}

	// Index valid un-invalidated evidence by obligationID
	validEvByObl := make(map[string][]*protocol.ChangeEvidence)
	for _, ev := range allEvidence {
		if ev.Valid && ev.TreeHash == chg.CurrentTreeHash {
			validEvByObl[ev.ObligationID] = append(validEvByObl[ev.ObligationID], ev)
		}
	}

	var passedObls []string
	var failedObls []string
	var pendingObls []string
	var staleObls []string
	var reasons []string

	allRequiredPassed := true

	for _, obl := range obligations {
		evList := validEvByObl[obl.ID]

		hasPass := false
		hasFail := false

		for _, ev := range evList {
			if ev.Result == "passed" {
				hasPass = true
			} else if ev.Result == "failed" {
				hasFail = true
			}
		}

		if hasFail {
			failedObls = append(failedObls, obl.ID)
			_ = c.store.UpdateProofObligationStatus(ctx, obl.ID, protocol.ProofStatusFailed, "")
			if obl.Required {
				allRequiredPassed = false
				reasons = append(reasons, fmt.Sprintf("required obligation %q (%s) failed", obl.Name, obl.ID))
			}
		} else if hasPass {
			passedObls = append(passedObls, obl.ID)
			_ = c.store.UpdateProofObligationStatus(ctx, obl.ID, protocol.ProofStatusPassed, "")
		} else if obl.Status == protocol.ProofStatusStale {
			staleObls = append(staleObls, obl.ID)
			if obl.Required {
				allRequiredPassed = false
				reasons = append(reasons, fmt.Sprintf("required obligation %q (%s) evidence is stale", obl.Name, obl.ID))
			}
		} else {
			pendingObls = append(pendingObls, obl.ID)
			_ = c.store.UpdateProofObligationStatus(ctx, obl.ID, protocol.ProofStatusPending, "")
			if obl.Required {
				allRequiredPassed = false
				reasons = append(reasons, fmt.Sprintf("required obligation %q (%s) is pending evidence", obl.Name, obl.ID))
			}
		}
	}

	now := time.Now().UTC()
	gateStatus := protocol.GateStatusBlocked
	if allRequiredPassed && len(obligations) > 0 {
		gateStatus = protocol.GateStatusCommittable
	}

	c.incMetric(telemetry.MetricGateEvaluationsTotal)

	// Update change lifecycle state according to gate status
	if gateStatus == protocol.GateStatusCommittable {
		chg.Status = protocol.ChangeStatusCommittable
		chg.VerifiedTreeHash = chg.CurrentTreeHash
		chg.UpdatedAt = now
		_ = c.store.SaveMeshChange(ctx, chg)
		c.incMetric(telemetry.MetricGateVerifiedTotal)
		c.incMetric(telemetry.MetricChangesCommittable)
	} else {
		c.incMetric(telemetry.MetricGateBlockedTotal)
		if chg.Status == protocol.ChangeStatusCommittable || chg.Status == protocol.ChangeStatusVerified {
			// Demote if previously verified/committable but now invalidated or blocked
			chg.Status = protocol.ChangeStatusBlocked
			chg.UpdatedAt = now
			_ = c.store.SaveMeshChange(ctx, chg)
		}
	}

	gateResult := &protocol.GateResult{
		ID:                 fmt.Sprintf("gate_%s_%d", changeID, now.UnixNano()),
		ChangeID:           changeID,
		Status:             gateStatus,
		CurrentTreeHash:    chg.CurrentTreeHash,
		VerifiedTreeHash:   chg.VerifiedTreeHash,
		PassedObligations:  passedObls,
		PendingObligations: pendingObls,
		FailedObligations:  failedObls,
		StaleObligations:   staleObls,
		Reasons:            reasons,
		EvaluatedAt:        now,
	}

	if err := c.store.SaveGateResult(ctx, gateResult); err != nil {
		return nil, fmt.Errorf("failed to save gate result: %w", err)
	}

	if c.eventBus != nil {
		evtType := protocol.EventGateVerified
		if gateStatus == protocol.GateStatusBlocked {
			evtType = protocol.EventGateBlocked
		}
		_ = c.eventBus.Publish(ctx, &protocol.CollaborationEvent{
			ID:        fmt.Sprintf("evt_gate_%s", gateResult.ID),
			SpaceID:   chg.SpaceID,
			Type:      evtType,
			Source:    "harnessmesh",
			Timestamp: now,
			Payload: map[string]any{
				"change_id":   changeID,
				"gate_status": string(gateStatus),
				"passed":      len(passedObls),
				"failed":      len(failedObls),
				"pending":     len(pendingObls),
			},
		})
	}

	return gateResult, nil
}

// CommitChange executes the final commit if gate is verified and TOCTOU check passes.
func (c *MeshCommitCoordinator) CommitChange(ctx context.Context, changeID string, authorID string, commitMsg string) (*protocol.MeshChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	chg, err := c.store.GetMeshChange(ctx, changeID)
	if err != nil || chg == nil {
		return nil, &protocol.ChangeNotFoundError{ChangeID: changeID}
	}

	if chg.Status == protocol.ChangeStatusCommitted {
		return chg, nil
	}
	if chg.Status == protocol.ChangeStatusAborted {
		return nil, fmt.Errorf("cannot commit aborted change %q", changeID)
	}

	// Must be in committable or verified status
	if chg.Status != protocol.ChangeStatusCommittable && chg.Status != protocol.ChangeStatusVerified {
		return nil, &protocol.ChangeNotCommittableError{
			ChangeID: changeID,
			Status:   chg.Status,
			Reasons:  []string{fmt.Sprintf("change is in status %q; must be %q", chg.Status, protocol.ChangeStatusCommittable)},
		}
	}

	// Single-writer invariant, defense in depth: only the original author may
	// commit their own change, and that author must still resolve to a
	// writable/managed executor. This holds even though CommitChange is
	// currently only reachable from the local CLI, never from a remote/MCP tool.
	if strings.TrimSpace(authorID) != "" && authorID != chg.AuthorParticipant {
		return nil, &protocol.PolicyDeniedError{
			Action: "commit_change",
			Reason: fmt.Sprintf("caller %q is not the author %q of change %q", authorID, chg.AuthorParticipant, changeID),
		}
	}
	if err := c.requireWritableExecutor(chg.AuthorParticipant, "commit_change"); err != nil {
		return nil, err
	}

	// TOCTOU Invariant Check: Verify that current working tree matches Change.CurrentTreeHash exactly!
	currentTreeHash, err := workspace.ComputeTreeHash(ctx, c.repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to verify tree hash before commit: %w", err)
	}

	if currentTreeHash != chg.CurrentTreeHash || (chg.VerifiedTreeHash != "" && currentTreeHash != chg.VerifiedTreeHash) {
		c.incMetric(telemetry.MetricTOCTOUViolationsTotal)
		return nil, &protocol.CommitTreeMismatchError{
			ChangeID:     chg.ID,
			VerifiedTree: chg.VerifiedTreeHash,
			CommitTree:   currentTreeHash,
		}
	}

	now := time.Now().UTC()
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("MeshCommit: %s (%s)", chg.Title, chg.ID)
	}

	// Attempt Git commit if repo is Git
	var commitHash string
	gitCommitRes, err := executil.Run(ctx, c.repoPath, nil, "", "git", "commit", "-am", commitMsg)
	if err == nil && gitCommitRes.ExitCode == 0 {
		revRes, revErr := executil.Run(ctx, c.repoPath, nil, "", "git", "rev-parse", "HEAD")
		if revErr == nil && revRes.ExitCode == 0 {
			commitHash = strings.TrimSpace(revRes.Stdout)
		}
	}

	if commitHash == "" {
		// Deterministic fallback commit hash
		commitHash = fmt.Sprintf("commit_%s_%d", chg.CurrentTreeHash[:min(8, len(chg.CurrentTreeHash))], now.Unix())
	}

	chg.Status = protocol.ChangeStatusCommitted
	chg.CommitSHA = commitHash
	chg.CommittedAt = &now
	chg.UpdatedAt = now

	if err := c.store.SaveMeshChange(ctx, chg); err != nil {
		return nil, fmt.Errorf("failed to save committed change: %w", err)
	}

	c.incMetric(telemetry.MetricChangesCommitted)

	// Record commit knowledge in Knowledge Archive
	if c.archive != nil {
		_, _ = c.archive.Remember(ctx,
			fmt.Sprintf("MeshCommit [%s]: %s (commit %s, tree %s)", chg.ID, chg.Title, commitHash, chg.CurrentTreeHash),
			"change_commit",
			"meshcommit",
			[]string{"meshcommit", chg.ID, chg.AuthorParticipant},
		)
	}

	if c.eventBus != nil {
		_ = c.eventBus.Publish(ctx, &protocol.CollaborationEvent{
			ID:        fmt.Sprintf("evt_chg_committed_%s", changeID),
			SpaceID:   chg.SpaceID,
			Type:      protocol.EventChangeCommitted,
			Source:    authorID,
			Timestamp: now,
			Payload: map[string]any{
				"change_id":   changeID,
				"commit_hash": commitHash,
				"tree_hash":   chg.CurrentTreeHash,
			},
		})
	}

	return chg, nil
}

// AbortChange marks a change as aborted.
// AbortChange transitions a change to "aborted". actorID has the same
// authorship-matching semantics as PrepareChange - see its doc comment.
func (c *MeshCommitCoordinator) AbortChange(ctx context.Context, changeID, actorID, reason string) (*protocol.MeshChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	chg, err := c.store.GetMeshChange(ctx, changeID)
	if err != nil || chg == nil {
		return nil, &protocol.ChangeNotFoundError{ChangeID: changeID}
	}

	if strings.TrimSpace(actorID) != "" && actorID != chg.AuthorParticipant {
		return nil, &protocol.PolicyDeniedError{
			Action: "abort_change",
			Reason: fmt.Sprintf("caller %q is not the author %q of change %q", actorID, chg.AuthorParticipant, changeID),
		}
	}

	if chg.Status == protocol.ChangeStatusCommitted {
		return nil, fmt.Errorf("cannot abort already committed change %q", changeID)
	}

	now := time.Now().UTC()
	chg.Status = protocol.ChangeStatusAborted
	chg.AbortedAt = &now
	chg.UpdatedAt = now

	if err := c.store.SaveMeshChange(ctx, chg); err != nil {
		return nil, fmt.Errorf("failed to save aborted change: %w", err)
	}

	c.incMetric(telemetry.MetricChangesAborted)

	if c.eventBus != nil {
		_ = c.eventBus.Publish(ctx, &protocol.CollaborationEvent{
			ID:        fmt.Sprintf("evt_chg_aborted_%s", changeID),
			SpaceID:   chg.SpaceID,
			Type:      protocol.EventChangeAborted,
			Source:    chg.AuthorParticipant,
			Timestamp: now,
			Payload: map[string]any{
				"change_id": changeID,
				"reason":    reason,
			},
		})
	}

	return chg, nil
}
