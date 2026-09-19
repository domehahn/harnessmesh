package collaboration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/economy"
	"github.com/domehahn/harnessmesh/internal/knowledge"
	"github.com/domehahn/harnessmesh/internal/modelrouting"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

type EngineConfig struct {
	Config               *config.Config
	Store                store.Store
	Repo                 string
	Projector            *contextpack.Projector
	Harnesses            map[string]agent.Harness
	ModelRoutingBackends map[string]modelrouting.ModelRoutingBackend
}

type Engine struct {
	cfg            *config.Config
	store          store.Store
	repo           string
	projector      *contextpack.Projector
	harnesses      map[string]agent.Harness
	modelBackends  map[string]modelrouting.ModelRoutingBackend
	economyCtrl    *economy.Controller
	spaceService   *SpaceService
	eventBus       *EventBus
	activationCtrl *ActivationController
	mu             sync.Mutex
	budgetMu       sync.Mutex
	inflightMu     sync.Mutex
	inflight       map[string]chan struct{}

	// In-memory state tracking per session
	peerCalls    map[string]int
	peerRounds   map[string]int
	peerTurns    map[string]int
	lastHash     map[string]string
	repeatCounts map[string]int

	rrIndexMu sync.Mutex
	rrIndices map[string]int
}

func NewEngine(ec EngineConfig) *Engine {
	backends := ec.ModelRoutingBackends
	if backends == nil && ec.Config != nil {
		backends = modelrouting.BuildRegistry(ec.Config)
	}

	economyCtrl := economy.NewController()
	var spaceCfg config.Config
	if ec.Config != nil {
		spaceCfg = *ec.Config
	}
	spaceSvc := NewSpaceService(ec.Store, spaceCfg)
	eventBus := NewEventBus(ec.Store, 150*time.Millisecond)
	actCtrl := NewActivationController(ActivationControllerConfig{
		Store:       ec.Store,
		Config:      ec.Config,
		Projector:   ec.Projector,
		Harnesses:   ec.Harnesses,
		EconomyCtrl: economyCtrl,
	})

	eng := &Engine{
		cfg:            ec.Config,
		store:          ec.Store,
		repo:           ec.Repo,
		projector:      ec.Projector,
		harnesses:      ec.Harnesses,
		modelBackends:  backends,
		economyCtrl:    economyCtrl,
		spaceService:   spaceSvc,
		eventBus:       eventBus,
		activationCtrl: actCtrl,
		peerCalls:      make(map[string]int),
		peerRounds:     make(map[string]int),
		peerTurns:      make(map[string]int),
		lastHash:       make(map[string]string),
		repeatCounts:   make(map[string]int),
		rrIndices:      make(map[string]int),
		inflight:       make(map[string]chan struct{}),
	}

	// Connect event bus to activation controller
	eventBus.AddListener("*", func(ctx context.Context, evt *protocol.CollaborationEvent) {
		eng.handleCollaborationEvent(ctx, evt)
	})

	return eng
}

func (e *Engine) Store() store.Store {
	return e.store
}

type knowledgeStore interface {
	KnowledgeArchive() *knowledge.Archive
}

func (e *Engine) KnowledgeSearch(ctx context.Context, query string, opts knowledge.SearchOptions) ([]knowledge.Record, error) {
	ks, ok := e.store.(knowledgeStore)
	if !ok || ks.KnowledgeArchive() == nil {
		return nil, errors.New("knowledge archive is not available")
	}
	return ks.KnowledgeArchive().Search(ctx, query, opts)
}

func (e *Engine) KnowledgeStats(ctx context.Context) (knowledge.Stats, error) {
	ks, ok := e.store.(knowledgeStore)
	if !ok || ks.KnowledgeArchive() == nil {
		return knowledge.Stats{}, errors.New("knowledge archive is not available")
	}
	return ks.KnowledgeArchive().Stats(ctx)
}

func (e *Engine) SpaceService() *SpaceService {
	return e.spaceService
}

func (e *Engine) EventBus() *EventBus {
	return e.eventBus
}

func (e *Engine) ActivationController() *ActivationController {
	return e.activationCtrl
}

func (e *Engine) EconomyController() *economy.Controller {
	return e.economyCtrl
}

func (e *Engine) ModelRoutingBackends() map[string]modelrouting.ModelRoutingBackend {
	return e.modelBackends
}

func (e *Engine) Escalate(sessionID, capability string) {
	e.economyCtrl.Escalate(sessionID, capability)
	if e.store != nil && sessionID != "" {
		_ = e.store.EmitEvent(context.Background(), sessionID, "routing.escalated", map[string]any{"capability": capability})
	}
}

func (e *Engine) Deescalate(sessionID, capability string) {
	e.economyCtrl.Deescalate(sessionID, capability)
	if e.store != nil && sessionID != "" {
		_ = e.store.EmitEvent(context.Background(), sessionID, "routing.deescalated", map[string]any{"capability": capability})
	}
}

// Close gracefully releases Engine resources including EventBus coalesce timers.
func (e *Engine) Close() {
	if e.eventBus != nil {
		e.eventBus.Close()
	}
}

func (e *Engine) CreateSession(ctx context.Context, sessionID, task string) (*store.Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if sessionID == "" {
		sessionID = fmt.Sprintf("hm_%d", time.Now().UnixNano())
	}

	writerParticipant := ""
	participants := make(map[string]store.ParticipantInfo)
	for name, a := range e.cfg.Agents {
		if a.Writable {
			if writerParticipant != "" {
				return nil, &protocol.WriterConflictError{
					Reason:  "multiple writable participants configured for session",
					Writers: []string{writerParticipant, name},
				}
			}
			writerParticipant = name
		}
		participants[name] = store.ParticipantInfo{
			AgentName: name,
			Adapter:   a.Kind,
			Role:      a.Role,
			Roles:     a.Roles,
			Writable:  a.Writable,
		}
	}

	sess := &store.Session{
		ID:                sessionID,
		RepoRoot:          e.repo,
		Task:              task,
		Status:            "active",
		WriterParticipant: writerParticipant,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		Participants:      participants,
		Budget: protocol.BudgetStatus{
			Known:              e.cfg.Collaboration.MaxCostUSD > 0 || e.cfg.Collaboration.MaxTotalTokens > 0 || e.cfg.Collaboration.MaxInputTokens > 0 || e.cfg.Collaboration.MaxOutputTokens > 0 || e.cfg.Collaboration.StrictTokenCeiling,
			StrictTokenCeiling: e.cfg.Collaboration.StrictTokenCeiling,
			MaxInputTokens:     e.cfg.Collaboration.MaxInputTokens,
			MaxOutputTokens:    e.cfg.Collaboration.MaxOutputTokens,
			MaxTotalTokens:     e.cfg.Collaboration.MaxTotalTokens,
			MaxCostUSD:         e.cfg.Collaboration.MaxCostUSD,
		},
	}

	if err := e.store.SaveSession(ctx, sess); err != nil {
		return nil, err
	}
	_ = e.store.EmitEvent(ctx, sessionID, "session.started", map[string]any{"task": task, "writer": writerParticipant})
	return sess, nil
}

type budgetReservation struct {
	sessionID          string
	estInput           int64
	estOutput          int64
	estCost            float64
	maxTokens          int64
	maxCostUSD         float64
	strictTokenCeiling bool
}

func checkPeerTokenLimit(h agent.Harness, targetPeer string, res *budgetReservation) (int64, error) {
	if res == nil || res.maxTokens <= 0 {
		return 0, nil
	}
	if h != nil && h.Capabilities().HardTokenLimitEnforced {
		return res.maxTokens, nil
	}
	if res.strictTokenCeiling {
		return 0, &protocol.HardTokenLimitUnsupportedError{
			Peer:   targetPeer,
			Reason: fmt.Sprintf("peer %q does not support hard output token enforcement under strict token ceiling", targetPeer),
		}
	}
	return 0, nil
}

func (e *Engine) checkBudget(sess *store.Session, estInputTokens int64) error {
	if sess == nil || !sess.Budget.Known {
		return nil
	}
	b := sess.Budget
	estOutput := int64(1000)
	if b.MaxOutputTokens > 0 && b.MaxOutputTokens < estOutput {
		estOutput = b.MaxOutputTokens
	}
	estCost := float64(estInputTokens)*0.000003 + float64(estOutput)*0.000015
	if b.MaxInputTokens > 0 && b.UsedInputTokens+estInputTokens > b.MaxInputTokens {
		return &protocol.BudgetExceededError{Metric: "input_tokens", Limit: b.MaxInputTokens, Used: b.UsedInputTokens + estInputTokens}
	}
	if b.MaxOutputTokens > 0 && b.UsedOutputTokens+estOutput > b.MaxOutputTokens {
		return &protocol.BudgetExceededError{Metric: "output_tokens", Limit: b.MaxOutputTokens, Used: b.UsedOutputTokens + estOutput}
	}
	if b.MaxTotalTokens > 0 && b.UsedTotalTokens+estInputTokens+estOutput > b.MaxTotalTokens {
		return &protocol.BudgetExceededError{Metric: "total_tokens", Limit: b.MaxTotalTokens, Used: b.UsedTotalTokens + estInputTokens + estOutput}
	}
	if b.MaxCostUSD > 0 && b.UsedCostUSD+estCost > b.MaxCostUSD {
		return &protocol.BudgetExceededError{Metric: "cost_usd", Limit: b.MaxCostUSD, Used: b.UsedCostUSD + estCost}
	}
	return nil
}

func (e *Engine) reserveBudget(ctx context.Context, sessionID string, estInputTokens, estOutputTokens int64) (*budgetReservation, error) {
	if sessionID == "" {
		return &budgetReservation{}, nil
	}
	if estOutputTokens <= 0 {
		estOutputTokens = 1000
	}

	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()

	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		var notFound *protocol.SessionNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, sql.ErrNoRows) {
			// Check if space exists with this ID
			sp, spErr := e.store.GetSpace(ctx, sessionID)
			if spErr != nil {
				var spNotFound *protocol.SpaceNotFoundError
				if errors.As(spErr, &spNotFound) || errors.Is(spErr, sql.ErrNoRows) {
					return &budgetReservation{sessionID: sessionID, maxTokens: estOutputTokens}, nil
				}
				return nil, fmt.Errorf("failed to lookup space budget: %w", spErr)
			}
			if sp != nil {
				sess = &store.Session{
					ID:     sp.ID,
					Budget: sp.Budget,
				}
			}
		} else {
			return nil, fmt.Errorf("failed to lookup session budget: %w", err)
		}
	}

	if sess == nil {
		return &budgetReservation{sessionID: sessionID, maxTokens: estOutputTokens}, nil
	}

	// Apply default engine budget if session/space budget is not yet initialized
	if !sess.Budget.Known && (e.cfg.Collaboration.MaxCostUSD > 0 || e.cfg.Collaboration.MaxTotalTokens > 0 || e.cfg.Collaboration.MaxInputTokens > 0 || e.cfg.Collaboration.MaxOutputTokens > 0 || e.cfg.Collaboration.StrictTokenCeiling) {
		sess.Budget.Known = true
		sess.Budget.StrictTokenCeiling = e.cfg.Collaboration.StrictTokenCeiling
		sess.Budget.MaxInputTokens = e.cfg.Collaboration.MaxInputTokens
		sess.Budget.MaxOutputTokens = e.cfg.Collaboration.MaxOutputTokens
		sess.Budget.MaxTotalTokens = e.cfg.Collaboration.MaxTotalTokens
		sess.Budget.MaxCostUSD = e.cfg.Collaboration.MaxCostUSD
	}

	if !sess.Budget.Known {
		return &budgetReservation{sessionID: sessionID, maxTokens: estOutputTokens}, nil
	}

	b := sess.Budget
	if b.MaxInputTokens > 0 && b.UsedInputTokens+estInputTokens > b.MaxInputTokens {
		return nil, &protocol.BudgetExceededError{Metric: "input_tokens", Limit: b.MaxInputTokens, Used: b.UsedInputTokens + estInputTokens}
	}

	// Dynamic worst-case output bound calculation
	minViableOutput := int64(100)
	targetOutput := estOutputTokens

	// Bound by remaining output budget if configured
	if b.MaxOutputTokens > 0 {
		remOutput := b.MaxOutputTokens - b.UsedOutputTokens
		if remOutput < minViableOutput {
			return nil, &protocol.BudgetExceededError{Metric: "output_tokens", Limit: b.MaxOutputTokens, Used: b.UsedOutputTokens + targetOutput}
		}
		if remOutput < targetOutput {
			targetOutput = remOutput
		}
	}

	// Bound by remaining total budget if configured
	if b.MaxTotalTokens > 0 {
		remTotal := b.MaxTotalTokens - b.UsedTotalTokens - estInputTokens
		if remTotal < minViableOutput {
			return nil, &protocol.BudgetExceededError{Metric: "total_tokens", Limit: b.MaxTotalTokens, Used: b.UsedTotalTokens + estInputTokens + targetOutput}
		}
		if remTotal < targetOutput {
			targetOutput = remTotal
		}
	}

	estCost := float64(estInputTokens)*0.000003 + float64(targetOutput)*0.000015
	if b.MaxCostUSD > 0 && b.UsedCostUSD+estCost > b.MaxCostUSD {
		return nil, &protocol.BudgetExceededError{Metric: "cost_usd", Limit: b.MaxCostUSD, Used: b.UsedCostUSD + estCost}
	}

	// Atomically record reservation in store across sessions and spaces
	sess.Budget.UsedInputTokens += estInputTokens
	sess.Budget.UsedOutputTokens += targetOutput
	sess.Budget.UsedTotalTokens += (estInputTokens + targetOutput)
	sess.Budget.UsedCostUSD += estCost
	if err := e.store.UpdateBudget(ctx, sessionID, sess.Budget); err != nil {
		return nil, fmt.Errorf("failed to atomically reserve budget: %w", err)
	}

	remCost := float64(0)
	if b.MaxCostUSD > 0 {
		remCost = b.MaxCostUSD - b.UsedCostUSD
	}

	return &budgetReservation{
		sessionID:          sessionID,
		estInput:           estInputTokens,
		estOutput:          targetOutput,
		estCost:            estCost,
		maxTokens:          targetOutput,
		maxCostUSD:         remCost,
		strictTokenCeiling: b.StrictTokenCeiling,
	}, nil
}

func (e *Engine) commitBudget(ctx context.Context, res *budgetReservation, actualIn, actualOut int64, actualCost float64) error {
	if res == nil || res.sessionID == "" {
		return nil
	}
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()

	sessionID := res.sessionID
	estInput := res.estInput
	estOutput := res.estOutput
	estCost := res.estCost

	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		var notFound *protocol.SessionNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, sql.ErrNoRows) {
			sp, spErr := e.store.GetSpace(ctx, sessionID)
			if spErr != nil {
				var spNotFound *protocol.SpaceNotFoundError
				if errors.As(spErr, &spNotFound) || errors.Is(spErr, sql.ErrNoRows) {
					return fmt.Errorf("session or space not found: %s", sessionID)
				}
				return fmt.Errorf("failed to lookup space for budget commit: %w", spErr)
			}
			if sp != nil {
				sess = &store.Session{ID: sp.ID, Budget: sp.Budget}
			}
		} else {
			return fmt.Errorf("failed to lookup session for budget commit: %w", err)
		}
	}
	if sess == nil {
		return fmt.Errorf("session or space %q not found for budget commit", sessionID)
	}

	if !sess.Budget.Known && (e.cfg.Collaboration.MaxCostUSD > 0 || e.cfg.Collaboration.MaxTotalTokens > 0 || e.cfg.Collaboration.MaxInputTokens > 0 || e.cfg.Collaboration.MaxOutputTokens > 0) {
		sess.Budget.Known = true
		sess.Budget.MaxInputTokens = e.cfg.Collaboration.MaxInputTokens
		sess.Budget.MaxOutputTokens = e.cfg.Collaboration.MaxOutputTokens
		sess.Budget.MaxTotalTokens = e.cfg.Collaboration.MaxTotalTokens
		sess.Budget.MaxCostUSD = e.cfg.Collaboration.MaxCostUSD
	}
	if !sess.Budget.Known {
		// Discharge reservation now that we know no budget is tracked
		res.sessionID = ""
		res.estInput = 0
		res.estOutput = 0
		res.estCost = 0
		res.maxTokens = 0
		res.maxCostUSD = 0
		return nil
	}

	b := sess.Budget
	newIn := b.UsedInputTokens - estInput + actualIn
	newOut := b.UsedOutputTokens - estOutput + actualOut
	newTotal := b.UsedTotalTokens - (estInput + estOutput) + (actualIn + actualOut)
	newCost := b.UsedCostUSD - estCost + actualCost

	sess.Budget.UsedInputTokens = newIn
	sess.Budget.UsedOutputTokens = newOut
	sess.Budget.UsedTotalTokens = newTotal
	sess.Budget.UsedCostUSD = newCost

	// Atomically persist budget update across sessions and spaces
	if err := e.store.UpdateBudget(ctx, sessionID, sess.Budget); err != nil {
		return fmt.Errorf("failed to atomically commit budget: %w", err)
	}

	// DISCHARGE RESERVATION ONLY AFTER ATOMIC PERSISTENCE SUCCEEDS!
	res.sessionID = ""
	res.estInput = 0
	res.estOutput = 0
	res.estCost = 0
	res.maxTokens = 0
	res.maxCostUSD = 0

	// Enforce hard budget limits on committed usage
	if b.MaxInputTokens > 0 && newIn > b.MaxInputTokens {
		return &protocol.BudgetExceededError{Metric: "input_tokens", Limit: b.MaxInputTokens, Used: newIn}
	}
	if b.MaxOutputTokens > 0 && newOut > b.MaxOutputTokens {
		return &protocol.BudgetExceededError{Metric: "output_tokens", Limit: b.MaxOutputTokens, Used: newOut}
	}
	if b.MaxTotalTokens > 0 && newTotal > b.MaxTotalTokens {
		return &protocol.BudgetExceededError{Metric: "total_tokens", Limit: b.MaxTotalTokens, Used: newTotal}
	}
	if b.MaxCostUSD > 0 && newCost > b.MaxCostUSD {
		return &protocol.BudgetExceededError{Metric: "cost_usd", Limit: b.MaxCostUSD, Used: newCost}
	}

	return nil
}

func (e *Engine) rollbackBudget(ctx context.Context, res *budgetReservation) error {
	if res == nil || res.sessionID == "" {
		return nil
	}
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()

	sessionID := res.sessionID
	estInput := res.estInput
	estOutput := res.estOutput
	estCost := res.estCost

	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		var notFound *protocol.SessionNotFoundError
		if errors.As(err, &notFound) || errors.Is(err, sql.ErrNoRows) {
			sp, spErr := e.store.GetSpace(ctx, sessionID)
			if spErr != nil {
				var spNotFound *protocol.SpaceNotFoundError
				if errors.As(spErr, &spNotFound) || errors.Is(spErr, sql.ErrNoRows) {
					return fmt.Errorf("session or space not found for budget rollback: %s", sessionID)
				}
				return fmt.Errorf("failed to lookup space budget for rollback: %w", spErr)
			}
			if sp != nil {
				sess = &store.Session{ID: sp.ID, Budget: sp.Budget}
			}
		} else {
			return fmt.Errorf("failed to lookup session budget for rollback: %w", err)
		}
	}
	if sess == nil {
		return fmt.Errorf("session or space %q not found for budget rollback", sessionID)
	}
	if !sess.Budget.Known {
		res.sessionID = ""
		res.estInput = 0
		res.estOutput = 0
		res.estCost = 0
		res.maxTokens = 0
		res.maxCostUSD = 0
		res.strictTokenCeiling = false
		return nil
	}

	sess.Budget.UsedInputTokens -= estInput
	sess.Budget.UsedOutputTokens -= estOutput
	sess.Budget.UsedTotalTokens -= (estInput + estOutput)
	sess.Budget.UsedCostUSD -= estCost

	if err := e.store.UpdateBudget(ctx, sessionID, sess.Budget); err != nil {
		_ = e.store.EmitEvent(ctx, sessionID, "budget.rollback_failed", map[string]any{
			"session_id": sessionID,
			"error":      err.Error(),
		})
		return fmt.Errorf("failed to atomically rollback budget for %s: %w", sessionID, err)
	}

	res.sessionID = ""
	res.estInput = 0
	res.estOutput = 0
	res.estCost = 0
	res.maxTokens = 0
	res.maxCostUSD = 0
	return nil
}

func (e *Engine) recordBudgetUsage(ctx context.Context, sessionID string, inputTokens, outputTokens int64, costUSD float64) error {
	if sessionID == "" {
		return nil
	}
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()

	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return err
	}
	sess.Budget.UsedInputTokens += inputTokens
	sess.Budget.UsedOutputTokens += outputTokens
	sess.Budget.UsedTotalTokens += (inputTokens + outputTokens)
	sess.Budget.UsedCostUSD += costUSD
	return e.store.SaveSession(ctx, sess)
}

func (e *Engine) acquireIdempotency(ctx context.Context, sessionID, key string) (cached string, release func(), err error) {
	if key == "" {
		return "", func() {}, nil
	}

	inflightKey := fmt.Sprintf("%s:%s", sessionID, key)

	for {
		e.inflightMu.Lock()
		val, getErr := e.store.GetIdempotency(ctx, sessionID, key)
		if getErr == nil && val != "" {
			e.inflightMu.Unlock()
			return val, func() {}, nil
		}

		ch, inFlight := e.inflight[inflightKey]
		if !inFlight {
			doneCh := make(chan struct{})
			e.inflight[inflightKey] = doneCh
			e.inflightMu.Unlock()

			release = func() {
				e.inflightMu.Lock()
				delete(e.inflight, inflightKey)
				close(doneCh)
				e.inflightMu.Unlock()
			}
			return "", release, nil
		}

		e.inflightMu.Unlock()

		select {
		case <-ctx.Done():
			return "", func() {}, ctx.Err()
		case <-ch:
		}
	}
}

func extractTokensAndCost(invReq agent.InvokeRequest, invRes agent.InvokeResult) (int64, int64, float64) {
	var inTokens, outTokens int64
	var cost float64
	if invRes.Usage != nil {
		if v, ok := invRes.Usage["input_tokens"].(float64); ok {
			inTokens = int64(v)
		} else if v, ok := invRes.Usage["input_tokens"].(int64); ok {
			inTokens = v
		} else if v, ok := invRes.Usage["input_tokens"].(int); ok {
			inTokens = int64(v)
		}

		if v, ok := invRes.Usage["output_tokens"].(float64); ok {
			outTokens = int64(v)
		} else if v, ok := invRes.Usage["output_tokens"].(int64); ok {
			outTokens = v
		} else if v, ok := invRes.Usage["output_tokens"].(int); ok {
			outTokens = int64(v)
		}

		if v, ok := invRes.Usage["cost_usd"].(float64); ok {
			cost = v
		}
	}
	if inTokens == 0 && len(invReq.Prompt) > 0 {
		inTokens = int64(len(invReq.Prompt) / 4)
		if inTokens < 1 {
			inTokens = 1
		}
	}
	if outTokens == 0 && len(invRes.Text) > 0 {
		outTokens = int64(len(invRes.Text) / 4)
		if outTokens < 1 {
			outTokens = 1
		}
	}
	if cost == 0 {
		cost = float64(inTokens)*0.000003 + float64(outTokens)*0.000015
	}
	return inTokens, outTokens, cost
}

func (e *Engine) checkChannelAccess(space *protocol.CollaborationSpace, ch *protocol.Channel, participantID string) error {
	if participantID == "" {
		return fmt.Errorf("participant ID cannot be empty")
	}
	if participantID == "user" || participantID == "admin" {
		if space.WriterParticipant == participantID && space.WriterParticipant != "" {
			return nil
		}
		if _, ok := space.Participants[participantID]; ok {
			return nil
		}
		return fmt.Errorf("caller %q is not authorized as user/admin for space %q", participantID, space.ID)
	}
	var participant *protocol.SpaceParticipant
	for _, p := range space.Participants {
		if p.ID == participantID {
			participant = &p
			break
		}
	}
	if participant == nil {
		return fmt.Errorf("participant %q is not a member of space %q", participantID, space.ID)
	}

	if ch == nil || ch.Visibility == protocol.ChannelVisibilityAll || ch.Visibility == "" {
		return nil
	}

	if ch.Visibility == protocol.ChannelVisibilitySelectedParticipants {
		for _, ap := range ch.AllowedParticipants {
			if ap == participantID {
				return nil
			}
		}
		return fmt.Errorf("participant %q is not authorized to access channel %q", participantID, ch.ID)
	}

	if ch.Visibility == protocol.ChannelVisibilitySelectedCapabilities {
		for _, ac := range ch.AllowedCapabilities {
			for _, pc := range participant.Capabilities {
				if pc == ac {
					return nil
				}
			}
		}
		return fmt.Errorf("participant %q lacks required capabilities to access channel %q", participantID, ch.ID)
	}

	return nil
}

func (e *Engine) GetSession(ctx context.Context, sessionID string) (*store.Session, error) {
	return e.store.GetSession(ctx, sessionID)
}

func (e *Engine) StopSession(ctx context.Context, sessionID, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	err := e.store.UpdateSessionStatus(ctx, sessionID, "completed", reason)
	if err != nil {
		return err
	}
	_ = e.store.EmitEvent(ctx, sessionID, "session.completed", map[string]any{"reason": reason})
	return nil
}

// ResolveParticipant resolves a participant given capability or explicit peer name.
func (e *Engine) ResolveParticipant(capability, explicitPeer string) (string, error) {
	return e.ResolveParticipantContext(context.Background(), "", capability, explicitPeer, "", economy.DifficultySignals{})
}

// ResolveParticipantContext resolves a participant with full task context, difficulty signals, and economic selection.
func (e *Engine) ResolveParticipantContext(ctx context.Context, sessionID, capability, explicitPeer, task string, signals economy.DifficultySignals) (string, error) {
	if explicitPeer != "" {
		if _, ok := e.harnesses[explicitPeer]; ok {
			return explicitPeer, nil
		}
		if _, ok := e.cfg.Agents[explicitPeer]; ok {
			return explicitPeer, nil
		}
		return "", &protocol.PeerUnavailableError{Peer: explicitPeer, Reason: "peer harness not registered"}
	}

	if capability == "" {
		if e.cfg.Workflow.Reviewer != "" {
			return e.cfg.Workflow.Reviewer, nil
		}
		return "", errors.New("neither peer nor capability specified")
	}

	var candidates []economy.ParticipantCandidate
	for name, a := range e.cfg.Agents {
		roles := a.Roles
		if len(roles) == 0 && a.Role != "" {
			roles = []string{a.Role}
		}
		if e.cfg.CapabilityRouting != nil {
			if routed, ok := e.cfg.CapabilityRouting[capability]; ok {
				for _, r := range routed {
					if r == name {
						roles = append(roles, capability)
						break
					}
				}
			}
		}

		cand := economy.ParticipantCandidate{
			ID:           name,
			Adapter:      a.Kind,
			Roles:        roles,
			Writable:     a.Writable,
			IsLocal:      a.IsLocal || a.Economy.Class == "local" || a.Kind == "fake" || a.Kind == "mock",
			Economy:      a.Economy,
			Capabilities: a.Capabilities,
			CallsCount:   e.peerCalls[name],
			AllowedPaths: a.AllowedPaths,
			DeniedPaths:  a.DeniedPaths,
		}
		candidates = append(candidates, cand)
	}

	policy := e.cfg.Selection.Policy
	if policy == "" {
		policy = e.cfg.Collaboration.PeerSelectionPolicy
	}
	if policy == "" {
		policy = "cheapest_suitable"
	}

	cand, dec, err := e.economyCtrl.SelectParticipant(sessionID, capability, explicitPeer, task, signals, candidates, policy)
	if err != nil {
		return "", err
	}

	if sessionID != "" && e.store != nil {
		rec := &store.RoutingDecisionRecord{
			ID:                  dec.ID,
			SessionID:           sessionID,
			RequestedCapability: dec.RequestedCapability,
			TaskTier:            string(dec.TaskTier),
			Eligible:            dec.Eligible,
			Selected:            dec.Selected,
			Reason:              dec.Reason,
			Timestamp:           dec.Timestamp,
		}
		_ = e.store.SaveRoutingDecision(ctx, rec)
		_ = e.store.EmitEvent(ctx, sessionID, "routing.participant_selected", map[string]any{
			"capability": dec.RequestedCapability,
			"selected":   dec.Selected,
			"tier":       dec.TaskTier,
			"reasons":    dec.Reason,
		})
	}

	return cand.ID, nil
}

func (e *Engine) ListParticipants(ctx context.Context, sessionID string) (*protocol.PeerListResponse, error) {
	var resp protocol.PeerListResponse
	for name, a := range e.cfg.Agents {
		status := "ready"
		resp.Participants = append(resp.Participants, protocol.ParticipantStatus{
			ID:       name,
			Adapter:  a.Kind,
			Roles:    a.Roles,
			Writable: a.Writable,
			Status:   status,
		})
	}
	sort.Slice(resp.Participants, func(i, j int) bool {
		return resp.Participants[i].ID < resp.Participants[j].ID
	})
	return &resp, nil
}

func (e *Engine) DiscoverCapabilities(ctx context.Context, req protocol.PeerCapabilitiesRequest) (*protocol.PeerCapabilitiesResponse, error) {
	var matches []protocol.PeerCapabilitiesMatch

	for name, a := range e.cfg.Agents {
		hasCap := false
		if e.cfg.CapabilityRouting != nil {
			if routed, ok := e.cfg.CapabilityRouting[req.Capability]; ok {
				for _, r := range routed {
					if r == name {
						hasCap = true
						break
					}
				}
			}
		}
		if !hasCap && a.HasRole(req.Capability) {
			hasCap = true
		}

		if hasCap {
			matches = append(matches, protocol.PeerCapabilitiesMatch{
				Participant: name,
				Roles:       a.Roles,
				Writable:    a.Writable,
			})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Participant < matches[j].Participant
	})

	return &protocol.PeerCapabilitiesResponse{Matches: matches}, nil
}

func DeduplicateFindings(findings []protocol.FindingPayload) []protocol.FindingPayload {
	if len(findings) <= 1 {
		return findings
	}

	for i := 0; i < len(findings); i++ {
		if findings[i].DuplicateOf != "" {
			continue
		}
		for j := i + 1; j < len(findings); j++ {
			if findings[j].DuplicateOf != "" {
				continue
			}

			fileI := filepath.Clean(findings[i].File)
			fileJ := filepath.Clean(findings[j].File)
			sameFile := fileI != "" && fileI != "." && fileI == fileJ

			lineDiff := findings[i].Line - findings[j].Line
			if lineDiff < 0 {
				lineDiff = -lineDiff
			}
			proximity := sameFile && lineDiff <= 5

			sameCat := strings.EqualFold(findings[i].Category, findings[j].Category) && findings[i].Category != ""
			claimSim := strings.EqualFold(strings.TrimSpace(findings[i].Claim), strings.TrimSpace(findings[j].Claim))

			if proximity && (sameCat || claimSim) {
				findings[j].DuplicateOf = findings[i].ID
				findings[i].RelatedFindings = append(findings[i].RelatedFindings, findings[j].ID)
			}
		}
	}
	return findings
}

// Ask handles peer.ask
func (e *Engine) Ask(ctx context.Context, sessionID, caller string, req protocol.AskRequest, depth int, idempotencyKey string) (retAns *protocol.AnswerPayload, retErr error) {
	if idempotencyKey != "" {
		cached, release, err := e.acquireIdempotency(ctx, sessionID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if cached != "" {
			var ans protocol.AnswerPayload
			if err := json.Unmarshal([]byte(cached), &ans); err == nil {
				return &ans, nil
			}
		}
		defer release()
	}

	e.mu.Lock()
	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if sess.Status != "active" {
		e.mu.Unlock()
		return nil, &protocol.SessionClosedError{SessionID: sessionID, Status: sess.Status}
	}

	// Reentrancy / Deadlock protection
	maxDepth := e.cfg.Collaboration.MaxPeerDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if depth >= maxDepth {
		e.mu.Unlock()
		return nil, &protocol.PeerDepthExceededError{CurrentDepth: depth, MaxDepth: maxDepth}
	}

	// Loop / call limits
	maxCalls := e.cfg.Collaboration.MaxPeerCalls
	if maxCalls <= 0 {
		maxCalls = 10
	}
	e.peerCalls[sessionID]++
	if e.peerCalls[sessionID] > maxCalls {
		e.mu.Unlock()
		return nil, &protocol.BudgetExceededError{Metric: "peer_calls", Limit: maxCalls, Used: e.peerCalls[sessionID]}
	}

	// Resolve peer
	targetPeer := req.Peer
	if targetPeer == "" && req.Capability != "" {
		var resolveErr error
		targetPeer, resolveErr = e.ResolveParticipant(req.Capability, "")
		if resolveErr != nil {
			e.mu.Unlock()
			return nil, resolveErr
		}
	} else if targetPeer != "" {
		var resolveErr error
		targetPeer, resolveErr = e.ResolveParticipant("", targetPeer)
		if resolveErr != nil {
			e.mu.Unlock()
			return nil, resolveErr
		}
	} else {
		e.mu.Unlock()
		return nil, errors.New("neither peer nor capability specified in ask request")
	}

	peerHarness, ok := e.harnesses[targetPeer]
	if !ok {
		e.mu.Unlock()
		return nil, &protocol.PeerUnavailableError{Peer: targetPeer, Reason: "peer harness not registered"}
	}

	// Capability check
	if !peerHarness.Capabilities().AnswerQuestions {
		e.mu.Unlock()
		return nil, &protocol.CapabilityUnsupportedError{Agent: targetPeer, Capability: "answer_questions"}
	}

	peerSessionID := ""
	if pInfo, ok := sess.Participants[targetPeer]; ok {
		peerSessionID = pInfo.HarnessSessionID
	}
	e.mu.Unlock()

	// Context projection
	projReq := contextpack.ProjectRequest{
		Task:             sess.Task,
		Question:         req.Question,
		Scope:            req.Scope,
		IncludeDiff:      req.Context.IncludeDiff,
		IncludeTests:     req.Context.IncludeTests,
		IncludeGitStatus: req.Context.IncludeGitStatus,
		TestCommand:      e.cfg.Workflow.TestCommand,
	}
	projected, err := e.projector.Project(ctx, projReq)
	if err != nil {
		return nil, err
	}

	questionText := req.Question
	if projected != nil && projected.Question != "" {
		questionText = projected.Question
	}
	prompt := formatAskPrompt(caller, questionText, projected)

	invReq := agent.InvokeRequest{
		Name:       targetPeer,
		Repo:       e.repo,
		Prompt:     prompt,
		SessionID:  peerSessionID,
		ReviewMode: false,
	}

	_ = e.store.EmitEvent(ctx, sessionID, "peer.started", map[string]any{
		"peer":      targetPeer,
		"caller":    caller,
		"operation": "ask",
		"depth":     depth,
	})

	estTokens := int64(len(invReq.Prompt) / 4)
	budgetRes, err := e.reserveBudget(ctx, sessionID, estTokens, 1000)
	if err != nil {
		return nil, err
	}
	defer func() {
		if budgetRes != nil {
			if rErr := e.rollbackBudget(ctx, budgetRes); rErr != nil {
				if retErr != nil {
					retErr = fmt.Errorf("%w (rollback error: %v)", retErr, rErr)
				} else {
					retErr = rErr
				}
			}
		}
	}()

	maxTok, tokErr := checkPeerTokenLimit(peerHarness, targetPeer, budgetRes)
	if tokErr != nil {
		return nil, tokErr
	}
	invReq.MaxTokens = maxTok
	invReq.MaxCostUSD = budgetRes.maxCostUSD

	invRes, err := peerHarness.Invoke(ctx, invReq)
	if err != nil {
		_ = e.store.EmitEvent(ctx, sessionID, "peer.failed", map[string]any{
			"peer":  targetPeer,
			"error": err.Error(),
		})
		return nil, err
	}

	inTok, outTok, cost := extractTokensAndCost(invReq, invRes)
	if err := e.commitBudget(ctx, budgetRes, inTok, outTok, cost); err != nil {
		return nil, fmt.Errorf("failed to commit budget usage: %w", err)
	}
	budgetRes = nil // successfully committed

	if invRes.SessionID != "" && invRes.SessionID != peerSessionID {
		_ = e.store.UpdateParticipantHarnessSession(ctx, sessionID, targetPeer, invRes.SessionID)
	}

	answer := &protocol.AnswerPayload{
		Answer: invRes.Text,
	}

	answerBytes, _ := json.Marshal(answer)
	msg := &protocol.PeerEnvelope{
		Protocol:       protocol.PeerProtocolV1,
		ID:             fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		SessionID:      sessionID,
		From:           targetPeer,
		To:             caller,
		Type:           protocol.MsgAnswer,
		CreatedAt:      time.Now().UTC(),
		Depth:          depth,
		IdempotencyKey: idempotencyKey,
		Payload:        answerBytes,
	}
	if err := e.store.SaveMessage(ctx, msg); err != nil {
		return nil, fmt.Errorf("failed to save message: %w", err)
	}

	if idempotencyKey != "" {
		if err := e.store.SaveIdempotency(ctx, idempotencyKey, sessionID, string(answerBytes)); err != nil {
			return nil, fmt.Errorf("failed to save idempotency: %w", err)
		}
	}

	_ = e.store.EmitEvent(ctx, sessionID, "peer.completed", map[string]any{
		"peer":      targetPeer,
		"operation": "ask",
	})

	return answer, nil
}

func (e *Engine) executeSingleReview(ctx context.Context, sessionID, caller, targetPeer string, req protocol.ReviewRequestPayload, depth int) (retResult *protocol.ReviewResultPayload, retErr error) {
	e.mu.Lock()
	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	peerHarness, ok := e.harnesses[targetPeer]
	if !ok {
		e.mu.Unlock()
		return nil, &protocol.PeerUnavailableError{Peer: targetPeer, Reason: "peer harness not registered"}
	}
	if !peerHarness.Capabilities().Review {
		e.mu.Unlock()
		return nil, &protocol.CapabilityUnsupportedError{Agent: targetPeer, Capability: "review"}
	}

	peerSessionID := ""
	if pInfo, ok := sess.Participants[targetPeer]; ok {
		peerSessionID = pInfo.HarnessSessionID
	}
	roundNum := e.peerRounds[sessionID]
	e.mu.Unlock()

	snap, err := e.projector.Capture(ctx, e.cfg.Workflow.TestCommand)
	if err != nil {
		return nil, err
	}

	taskText := sess.Task
	if e.projector != nil {
		taskText = e.projector.RedactSecrets(taskText)
	}
	prompt := formatReviewPrompt(taskText, roundNum, snap, req.Focus)

	invReq := agent.InvokeRequest{
		Name:         targetPeer,
		Repo:         e.repo,
		Prompt:       prompt,
		SessionID:    peerSessionID,
		ReviewMode:   true,
		ReviewSchema: protocol.ReviewSchema,
	}

	_ = e.store.EmitEvent(ctx, sessionID, "peer.started", map[string]any{
		"peer":      targetPeer,
		"caller":    caller,
		"operation": "request_review",
		"round":     roundNum,
	})

	estTokens := int64(len(invReq.Prompt) / 4)
	budgetRes, err := e.reserveBudget(ctx, sessionID, estTokens, 1500)
	if err != nil {
		return nil, err
	}
	defer func() {
		if budgetRes != nil {
			if rErr := e.rollbackBudget(ctx, budgetRes); rErr != nil {
				if retErr != nil {
					retErr = fmt.Errorf("%w (rollback error: %v)", retErr, rErr)
				} else {
					retErr = rErr
				}
			}
		}
	}()

	maxTok, tokErr := checkPeerTokenLimit(peerHarness, targetPeer, budgetRes)
	if tokErr != nil {
		return nil, tokErr
	}
	invReq.MaxTokens = maxTok
	invReq.MaxCostUSD = budgetRes.maxCostUSD

	invRes, err := peerHarness.Invoke(ctx, invReq)
	if err != nil {
		_ = e.store.EmitEvent(ctx, sessionID, "peer.failed", map[string]any{
			"peer":  targetPeer,
			"error": err.Error(),
		})
		return nil, err
	}

	inTok, outTok, cost := extractTokensAndCost(invReq, invRes)
	if err := e.commitBudget(ctx, budgetRes, inTok, outTok, cost); err != nil {
		return nil, fmt.Errorf("failed to commit review budget usage: %w", err)
	}
	budgetRes = nil

	if invRes.SessionID != "" && invRes.SessionID != peerSessionID {
		_ = e.store.UpdateParticipantHarnessSession(ctx, sessionID, targetPeer, invRes.SessionID)
	}

	var parsed protocol.ReviewResult
	cleanJSON := normalizeReviewJSON(invRes.Text)
	if err := json.Unmarshal([]byte(cleanJSON), &parsed); err != nil {
		return nil, &protocol.MalformedPeerResponseError{
			Agent:  targetPeer,
			Reason: fmt.Sprintf("unmarshal review JSON: %v", err),
			Output: invRes.Text,
		}
	}

	result := &protocol.ReviewResultPayload{
		Status:   string(parsed.Verdict),
		Summary:  parsed.Summary,
		Findings: make([]protocol.FindingPayload, 0, len(parsed.Findings)),
	}

	for _, f := range parsed.Findings {
		fp := protocol.FindingPayload{
			ID:                f.ID,
			SessionID:         sessionID,
			SourceAgent:       targetPeer,
			SourceParticipant: targetPeer,
			SourceAdapter:     peerHarness.AdapterType(),
			Timestamp:         time.Now().UTC(),
			Severity:          f.Severity,
			Claim:             f.Claim,
			Evidence:          f.Evidence,
			Recommendation:    f.Recommendation,
			File:              f.File,
			Line:              f.Line,
			LineStart:         f.Line,
			Status:            protocol.FindingOpen,
		}
		if fp.ID == "" {
			fp.ID = fmt.Sprintf("HM-%03d", len(result.Findings)+1)
		}
		result.Findings = append(result.Findings, fp)
		if err := e.store.SaveFinding(ctx, sessionID, &fp); err != nil {
			return nil, fmt.Errorf("failed to save finding %s: %w", fp.ID, err)
		}
		_ = e.store.EmitEvent(ctx, sessionID, "finding.created", fp)
	}

	return result, nil
}

// RequestReview handles peer.request_review (single reviewer or parallel multi-review)
func (e *Engine) RequestReview(ctx context.Context, sessionID, caller string, req protocol.ReviewRequestPayload, depth int, idempotencyKey string) (*protocol.ReviewResultPayload, error) {
	if idempotencyKey != "" {
		cached, release, err := e.acquireIdempotency(ctx, sessionID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if cached != "" {
			var rev protocol.ReviewResultPayload
			if err := json.Unmarshal([]byte(cached), &rev); err == nil {
				return &rev, nil
			}
		}
		defer release()
	}

	e.mu.Lock()
	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if sess.Status != "active" {
		e.mu.Unlock()
		return nil, &protocol.SessionClosedError{SessionID: sessionID, Status: sess.Status}
	}

	// Reentrancy / Deadlock protection
	maxDepth := e.cfg.Collaboration.MaxPeerDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if depth >= maxDepth {
		e.mu.Unlock()
		return nil, &protocol.PeerDepthExceededError{CurrentDepth: depth, MaxDepth: maxDepth}
	}

	// Call & Round limits
	maxCalls := e.cfg.Collaboration.MaxPeerCalls
	if maxCalls <= 0 {
		maxCalls = 10
	}
	e.peerCalls[sessionID]++
	if e.peerCalls[sessionID] > maxCalls {
		e.mu.Unlock()
		return nil, &protocol.BudgetExceededError{Metric: "peer_calls", Limit: maxCalls, Used: e.peerCalls[sessionID]}
	}

	maxRounds := e.cfg.Collaboration.MaxPeerRounds
	if maxRounds <= 0 {
		maxRounds = 3
	}
	e.peerRounds[sessionID]++
	if e.peerRounds[sessionID] > maxRounds {
		e.mu.Unlock()
		_ = e.store.UpdateSessionStatus(ctx, sessionID, "max_rounds", fmt.Sprintf("reached max_peer_rounds=%d", maxRounds))
		return nil, fmt.Errorf("reached max_peer_rounds=%d without approval", maxRounds)
	}
	e.mu.Unlock()

	var reviewPayload protocol.ReviewResultPayload

	// Branch: Multi-review vs Single-review
	if len(req.Reviewers) > 0 {
		maxParallel := e.cfg.Collaboration.MaxParallelPeers
		if maxParallel <= 0 {
			maxParallel = 3
		}
		sem := make(chan struct{}, maxParallel)
		var wg sync.WaitGroup

		type reviewEntry struct {
			peer string
			res  *protocol.ReviewResultPayload
			err  error
		}
		entries := make([]reviewEntry, len(req.Reviewers))

		for idx, spec := range req.Reviewers {
			wg.Add(1)
			go func(i int, s protocol.ReviewerSpec) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				targetPeer, err := e.ResolveParticipant(s.Capability, s.Peer)
				if err != nil {
					entries[i] = reviewEntry{peer: targetPeer, err: err}
					return
				}

				singleReq := req
				singleReq.Reviewers = nil
				singleReq.Peer = targetPeer
				singleReq.Capability = ""
				if len(s.Focus) > 0 {
					singleReq.Focus = s.Focus
				}

				res, err := e.executeSingleReview(ctx, sessionID, caller, targetPeer, singleReq, depth)
				entries[i] = reviewEntry{peer: targetPeer, res: res, err: err}
			}(idx, spec)
		}
		wg.Wait()

		var allFindings []protocol.FindingPayload
		hasChangesRequired := false
		hasBlock := false
		successCount := 0
		var summaries []string

		for _, entry := range entries {
			if entry.err == nil && entry.res != nil {
				successCount++
				allFindings = append(allFindings, entry.res.Findings...)
				if entry.res.Status == "block" {
					hasBlock = true
				} else if entry.res.Status == "changes_required" {
					hasChangesRequired = true
				}
				summaries = append(summaries, fmt.Sprintf("[%s]: %s", entry.peer, entry.res.Summary))
			}
		}

		if successCount == 0 && len(entries) > 0 {
			return nil, fmt.Errorf("all parallel reviewers failed; first error: %v", entries[0].err)
		}

		allFindings = DeduplicateFindings(allFindings)

		aggregateStatus := "approve"
		if hasBlock {
			aggregateStatus = "block"
		} else if hasChangesRequired || len(allFindings) > 0 {
			aggregateStatus = "changes_required"
		}

		reviewPayload = protocol.ReviewResultPayload{
			Status:   aggregateStatus,
			Summary:  strings.Join(summaries, "\n"),
			Findings: allFindings,
		}
	} else {
		targetPeer, err := e.ResolveParticipant(req.Capability, req.Peer)
		if err != nil {
			return nil, err
		}
		singleReq := req
		singleReq.Peer = targetPeer
		res, err := e.executeSingleReview(ctx, sessionID, caller, targetPeer, singleReq, depth)
		if err != nil {
			return nil, err
		}
		reviewPayload = *res
		reviewPayload.Findings = DeduplicateFindings(reviewPayload.Findings)
	}

	// Stall detection: check identical finding hashes across consecutive rounds
	hash := computeFindingsHash(reviewPayload.Findings)
	e.mu.Lock()
	if e.cfg.Workflow.StopOnRepeat && hash != "" && hash == e.lastHash[sessionID] {
		e.repeatCounts[sessionID]++
		if e.repeatCounts[sessionID] >= 1 {
			e.mu.Unlock()
			_ = e.store.UpdateSessionStatus(ctx, sessionID, "stalled", "same material findings repeated in consecutive rounds")
			return nil, &protocol.StalledError{
				Reason:      "same material findings repeated in consecutive rounds",
				FindingHash: hash,
				Rounds:      e.peerRounds[sessionID],
			}
		}
	} else {
		e.repeatCounts[sessionID] = 0
		e.lastHash[sessionID] = hash
	}
	e.mu.Unlock()

	// Persist message
	revBytes, _ := json.Marshal(reviewPayload)
	msg := &protocol.PeerEnvelope{
		Protocol:       protocol.PeerProtocolV1,
		ID:             fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		SessionID:      sessionID,
		From:           caller,
		To:             "reviewers",
		Type:           protocol.MsgReviewResult,
		CreatedAt:      time.Now().UTC(),
		Depth:          depth,
		IdempotencyKey: idempotencyKey,
		Payload:        revBytes,
	}
	if err := e.store.SaveMessage(ctx, msg); err != nil {
		return nil, fmt.Errorf("failed to save review message: %w", err)
	}

	if idempotencyKey != "" {
		if err := e.store.SaveIdempotency(ctx, idempotencyKey, sessionID, string(revBytes)); err != nil {
			return nil, fmt.Errorf("failed to save idempotency: %w", err)
		}
	}

	_ = e.store.EmitEvent(ctx, sessionID, "peer.completed", map[string]any{
		"operation": "request_review",
		"verdict":   reviewPayload.Status,
	})

	return &reviewPayload, nil
}

// SubmitFinding handles peer.submit_finding
func (e *Engine) SubmitFinding(ctx context.Context, sessionID, caller string, finding protocol.FindingPayload) (*protocol.FindingPayload, error) {
	if finding.ID == "" {
		finding.ID = fmt.Sprintf("HM-%d", time.Now().UnixNano())
	}
	if finding.SourceAgent == "" {
		finding.SourceAgent = caller
	}
	if finding.Timestamp.IsZero() {
		finding.Timestamp = time.Now().UTC()
	}
	if finding.Status == "" {
		finding.Status = protocol.FindingOpen
	}

	// Check for duplicate finding in session
	existing, _ := e.store.GetFinding(ctx, sessionID, finding.ID)
	if existing != nil {
		// Duplicate ID; update existing or return
		finding.Status = existing.Status
	}

	if err := e.store.SaveFinding(ctx, sessionID, &finding); err != nil {
		return nil, err
	}
	_ = e.store.EmitEvent(ctx, sessionID, "finding.created", finding)
	return &finding, nil
}

// SubmitEvidence handles peer.submit_evidence
func (e *Engine) SubmitEvidence(ctx context.Context, sessionID, caller string, ev protocol.EvidencePayload) (*protocol.EvidencePayload, error) {
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("ev_%d", time.Now().UnixNano())
	}
	if ev.SourceAgent == "" {
		ev.SourceAgent = caller
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}

	if err := e.store.SaveEvidence(ctx, sessionID, &ev); err != nil {
		return nil, err
	}
	_ = e.store.EmitEvent(ctx, sessionID, "evidence.created", ev)
	return &ev, nil
}

// Challenge handles peer.challenge
func (e *Engine) Challenge(ctx context.Context, sessionID, caller string, ch protocol.ChallengePayload) (*protocol.ChallengePayload, error) {
	if ch.ID == "" {
		ch.ID = fmt.Sprintf("ch_%d", time.Now().UnixNano())
	}
	if ch.Challenger == "" {
		ch.Challenger = caller
	}
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = time.Now().UTC()
	}

	if err := e.store.SaveChallenge(ctx, sessionID, &ch); err != nil {
		return nil, err
	}
	_ = e.store.EmitEvent(ctx, sessionID, "challenge.created", ch)
	_ = e.store.EmitEvent(ctx, sessionID, "finding.updated", map[string]any{
		"finding_id": ch.FindingID,
		"status":     protocol.FindingDisputed,
	})
	return &ch, nil
}

// Resolve handles peer.resolve
func (e *Engine) Resolve(ctx context.Context, sessionID, caller string, res protocol.ResolutionPayload) (*protocol.ResolutionPayload, error) {
	if res.ID == "" {
		res.ID = fmt.Sprintf("res_%d", time.Now().UnixNano())
	}
	if res.ResolvingAgent == "" {
		res.ResolvingAgent = caller
	}
	if res.Timestamp.IsZero() {
		res.Timestamp = time.Now().UTC()
	}

	if err := e.store.SaveResolution(ctx, sessionID, &res); err != nil {
		return nil, err
	}
	_ = e.store.EmitEvent(ctx, sessionID, "resolution.created", res)
	return &res, nil
}

// Reply handles peer.reply
func (e *Engine) Reply(ctx context.Context, sessionID, caller string, rep protocol.ReplyPayload, depth int, idempotencyKey string) (*protocol.ReplyPayload, error) {
	if idempotencyKey != "" {
		cached, release, err := e.acquireIdempotency(ctx, sessionID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if cached != "" {
			var cachedRep protocol.ReplyPayload
			if err := json.Unmarshal([]byte(cached), &cachedRep); err == nil {
				return &cachedRep, nil
			}
		}
		defer release()
	}
	repBytes, _ := json.Marshal(rep)
	msg := &protocol.PeerEnvelope{
		Protocol:       protocol.PeerProtocolV1,
		ID:             fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		SessionID:      sessionID,
		From:           caller,
		To:             rep.TargetPeer,
		Type:           protocol.MsgReply,
		CreatedAt:      time.Now().UTC(),
		CorrelationID:  rep.ParentMessageID,
		Depth:          depth,
		IdempotencyKey: idempotencyKey,
		Payload:        repBytes,
	}
	if err := e.store.SaveMessage(ctx, msg); err != nil {
		return nil, err
	}
	if idempotencyKey != "" {
		if err := e.store.SaveIdempotency(ctx, idempotencyKey, sessionID, string(repBytes)); err != nil {
			return nil, fmt.Errorf("failed to save idempotency: %w", err)
		}
	}
	_ = e.store.EmitEvent(ctx, sessionID, "message.created", msg)
	return &rep, nil
}

// Status handles peer.status
func (e *Engine) Status(ctx context.Context, sessionID string) (*protocol.StatusPayload, error) {
	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	findings, err := e.store.GetFindings(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	open := 0
	disputed := 0
	resolved := 0
	for _, f := range findings {
		switch f.Status {
		case protocol.FindingOpen:
			open++
		case protocol.FindingDisputed:
			disputed++
		case protocol.FindingResolved, protocol.FindingDismissed:
			resolved++
		}
	}

	participants := make([]string, 0, len(sess.Participants))
	for name := range sess.Participants {
		participants = append(participants, name)
	}
	sort.Strings(participants)

	e.mu.Lock()
	calls := e.peerCalls[sessionID]
	rounds := e.peerRounds[sessionID]
	e.mu.Unlock()

	remainingRounds := e.cfg.Collaboration.MaxPeerRounds - rounds
	if remainingRounds < 0 {
		remainingRounds = 0
	}

	return &protocol.StatusPayload{
		SessionID:           sessionID,
		Participants:        participants,
		OpenFindings:        open,
		DisputedFindings:    disputed,
		ResolvedFindings:    resolved,
		PeerCalls:           calls,
		RemainingPeerRounds: remainingRounds,
		RemainingBudget:     sess.Budget,
	}, nil
}

func computeFindingsHash(findings []protocol.FindingPayload) string {
	if len(findings) == 0 {
		return ""
	}
	keys := make([]string, 0, len(findings))
	for _, f := range findings {
		keys = append(keys, strings.ToLower(strings.TrimSpace(f.ID))+"|"+
			strings.ToLower(strings.TrimSpace(f.Claim))+"|"+
			strings.ToLower(strings.TrimSpace(f.File)))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

func formatAskPrompt(caller, question string, proj *contextpack.ProjectedContext) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Agent %q is requesting your assistance on a task.\n\n", caller))
	task := ""
	if proj != nil {
		task = proj.Task
	}
	sb.WriteString(fmt.Sprintf("Task: %s\n\n", contextpack.RedactSecrets(task)))
	sb.WriteString(fmt.Sprintf("Question:\n%s\n\n", contextpack.RedactSecrets(question)))

	if proj.Status != "" {
		sb.WriteString("Repository status:\n" + proj.Status + "\n\n")
	}
	if proj.DiffStat != "" {
		sb.WriteString("Diff stat:\n" + proj.DiffStat + "\n\n")
	}
	if proj.Diff != "" {
		sb.WriteString("Diff:\n```diff\n" + proj.Diff + "\n```\n\n")
	}
	if len(proj.Files) > 0 {
		sb.WriteString("Scoped files:\n")
		for path, content := range proj.Files {
			sb.WriteString(fmt.Sprintf("--- File: %s ---\n%s\n\n", path, content))
		}
	}
	sb.WriteString("Provide a concise, evidence-driven, direct answer to the question.")
	return sb.String()
}

func formatReviewPrompt(task string, round int, snap protocol.ContextSnapshot, focus []string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Review round %d for repository changes.\n\n", round))
	sb.WriteString(fmt.Sprintf("Task: %s\n\n", task))
	if len(focus) > 0 {
		sb.WriteString(fmt.Sprintf("Focus areas: %s\n\n", strings.Join(focus, ", ")))
	}
	sb.WriteString("Repository state:\n")
	sb.WriteString(fmt.Sprintf("HEAD: %s\nBranch: %s\nStatus:\n%s\n\n", snap.Head, snap.Branch, snap.Status))
	if snap.DiffStat != "" {
		sb.WriteString("Diff stat:\n" + snap.DiffStat + "\n\n")
	}
	if snap.Diff != "" {
		sb.WriteString("Diff:\n```diff\n" + snap.Diff + "\n```\n\n")
	}
	if snap.TestOutput != "" {
		sb.WriteString(fmt.Sprintf("Test output (exit code %v):\n```\n%s\n```\n\n", snap.TestExitCode, snap.TestOutput))
	}
	sb.WriteString("Respond with valid JSON matching the review schema. If changes are required, provide concrete, verifiable findings with claims, evidence, and recommendations.")
	return sb.String()
}

func normalizeReviewJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

func (e *Engine) formatConversePrompt(caller, message string, proj *contextpack.ProjectedContext, priorMessages []*protocol.PeerEnvelope) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Peer collaboration request from agent %q:\n\n", caller))
	if proj != nil && proj.Task != "" {
		sb.WriteString(fmt.Sprintf("Active Task: %s\n\n", proj.Task))
	}

	if len(priorMessages) > 0 {
		start := 0
		if len(priorMessages) > 4 {
			start = len(priorMessages) - 4
		}
		sb.WriteString("Recent peer conversation history:\n")
		for _, m := range priorMessages[start:] {
			text := string(m.Payload)
			var ans protocol.AnswerPayload
			if err := json.Unmarshal(m.Payload, &ans); err == nil && ans.Answer != "" {
				text = ans.Answer
			}
			var convReq protocol.ConverseRequest
			if err := json.Unmarshal(m.Payload, &convReq); err == nil && convReq.Message != "" {
				text = convReq.Message
			}
			var convResp protocol.ConverseResponse
			if err := json.Unmarshal(m.Payload, &convResp); err == nil && convResp.Response != "" {
				text = convResp.Response
			}
			if len(text) > 400 {
				text = text[:397] + "..."
			}
			if e.projector != nil {
				text = e.projector.RedactSecrets(text)
			}
			sb.WriteString(fmt.Sprintf("[%s -> %s]: %s\n", m.From, m.To, text))
		}
		sb.WriteString("\n")
	}

	msg := message
	if proj != nil && proj.Question != "" {
		msg = proj.Question
	}
	if e.projector != nil {
		msg = e.projector.RedactSecrets(msg)
	} else {
		msg = contextpack.RedactSecrets(msg)
	}
	sb.WriteString("Message:\n" + msg + "\n\n")

	if proj != nil {
		if proj.Status != "" {
			sb.WriteString("Repository status:\n" + proj.Status + "\n\n")
		}
		if proj.DiffStat != "" {
			sb.WriteString("Diff stat:\n" + proj.DiffStat + "\n\n")
		}
		if proj.Diff != "" {
			sb.WriteString("Diff:\n```diff\n" + proj.Diff + "\n```\n\n")
		}
		if proj.TestOutput != "" {
			sb.WriteString(fmt.Sprintf("Test Output:\n```\n%s\n```\n\n", proj.TestOutput))
		}
		if len(proj.Files) > 0 {
			sb.WriteString("Scoped files:\n")
			for path, content := range proj.Files {
				sb.WriteString(fmt.Sprintf("--- File: %s ---\n%s\n\n", path, content))
			}
		}
	}

	sb.WriteString("Respond directly and constructively. If giving architectural or debugging advice, be direct. If reviewing, provide concrete findings with file:line pointers. If you need clarification from the calling agent to proceed, state your question clearly.")
	return sb.String()
}

// Converse handles peer.converse for ongoing multi-turn autonomous peer collaboration.
func (e *Engine) Converse(ctx context.Context, sessionID, caller string, req protocol.ConverseRequest, depth int, idempotencyKey string) (retResp *protocol.ConverseResponse, retErr error) {
	if sessionID == "" {
		sessionID = req.ConversationID
	}
	if sessionID == "" {
		sess, err := e.CreateSession(ctx, "", "Peer conversation")
		if err != nil {
			return nil, err
		}
		sessionID = sess.ID
	}

	if idempotencyKey != "" {
		cached, release, err := e.acquireIdempotency(ctx, sessionID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if cached != "" {
			var cachedResp protocol.ConverseResponse
			if err := json.Unmarshal([]byte(cached), &cachedResp); err == nil {
				return &cachedResp, nil
			}
		}
		defer release()
	}

	e.mu.Lock()
	sess, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if sess.Status != "active" {
		e.mu.Unlock()
		return nil, &protocol.SessionClosedError{SessionID: sessionID, Status: sess.Status}
	}

	// Check reentrancy / depth limits
	maxDepth := e.cfg.Collaboration.MaxPeerDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if depth >= maxDepth {
		e.mu.Unlock()
		return nil, &protocol.PeerDepthExceededError{CurrentDepth: depth, MaxDepth: maxDepth}
	}

	// Check call & turn limits
	maxCalls := e.cfg.Collaboration.MaxPeerCalls
	if maxCalls <= 0 {
		maxCalls = 12
	}
	e.peerCalls[sessionID]++
	if e.peerCalls[sessionID] > maxCalls {
		e.mu.Unlock()
		return nil, &protocol.BudgetExceededError{Metric: "peer_calls", Limit: maxCalls, Used: e.peerCalls[sessionID]}
	}

	maxTurns := e.cfg.Collaboration.MaxConversationTurns
	if maxTurns <= 0 {
		maxTurns = 6
	}
	e.peerTurns[sessionID]++
	if e.peerTurns[sessionID] > maxTurns {
		e.mu.Unlock()
		return nil, &protocol.BudgetExceededError{Metric: "conversation_turns", Limit: maxTurns, Used: e.peerTurns[sessionID]}
	}

	// Target peer resolution
	targetPeer := req.Peer
	if targetPeer == "" && req.Capability != "" {
		var resolveErr error
		targetPeer, resolveErr = e.ResolveParticipant(req.Capability, "")
		if resolveErr != nil {
			e.mu.Unlock()
			return nil, resolveErr
		}
	} else if targetPeer != "" {
		var resolveErr error
		targetPeer, resolveErr = e.ResolveParticipant("", targetPeer)
		if resolveErr != nil {
			e.mu.Unlock()
			return nil, resolveErr
		}
	} else {
		// Default to reviewer if configured
		if e.cfg.Workflow.Reviewer != "" {
			targetPeer = e.cfg.Workflow.Reviewer
		} else {
			e.mu.Unlock()
			return nil, errors.New("neither peer nor capability specified in converse request")
		}
	}

	peerHarness, ok := e.harnesses[targetPeer]
	if !ok {
		e.mu.Unlock()
		return nil, &protocol.PeerUnavailableError{
			Peer:   targetPeer,
			Reason: fmt.Sprintf("peer %q not registered or available. Run 'harnessmesh doctor'", targetPeer),
		}
	}

	// Retrieve persistent harness session for this peer in this session
	peerSessionID := ""
	if pInfo, ok := sess.Participants[targetPeer]; ok {
		peerSessionID = pInfo.HarnessSessionID
	}
	e.mu.Unlock()

	// Context projection
	projReq := contextpack.ProjectRequest{
		Task:             sess.Task,
		Question:         req.Message,
		Scope:            req.Scope,
		IncludeDiff:      req.Context.IncludeDiff,
		IncludeTests:     req.Context.IncludeTests,
		IncludeGitStatus: req.Context.IncludeGitStatus,
		TestCommand:      e.cfg.Workflow.TestCommand,
	}
	projected, err := e.projector.Project(ctx, projReq)
	if err != nil {
		return nil, err
	}

	// Fetch prior messages
	priorMessages, _ := e.store.GetMessages(ctx, sessionID)

	prompt := e.formatConversePrompt(caller, req.Message, projected, priorMessages)

	// Determine if structured review is explicitly requested
	isReviewExpected := strings.EqualFold(req.ExpectedResponseType, "review") ||
		(req.ExpectedResponseType == "" && strings.Contains(strings.ToLower(req.Message), "review") && req.Context.IncludeDiff)

	invReq := agent.InvokeRequest{
		Name:         targetPeer,
		Repo:         e.repo,
		Prompt:       prompt,
		SessionID:    peerSessionID,
		ReviewMode:   isReviewExpected,
		ReviewSchema: protocol.ReviewSchema,
	}

	reqMsgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	corrID := req.CorrelationID
	if corrID == "" {
		corrID = reqMsgID
	}
	causationID := req.ParentMessageID
	if causationID == "" {
		causationID = req.CausationID
	}

	// Record outgoing caller request in store
	reqPayloadBytes, _ := json.Marshal(req)
	inEnvelope := &protocol.PeerEnvelope{
		Protocol:          protocol.PeerProtocolV1,
		ID:                reqMsgID,
		SessionID:         sessionID,
		From:              caller,
		To:                targetPeer,
		Type:              protocol.MsgConverse,
		CreatedAt:         time.Now().UTC(),
		CorrelationID:     corrID,
		CausationID:       causationID,
		Depth:             depth,
		IdempotencyKey:    idempotencyKey,
		ExternalSessionID: peerSessionID,
		Status:            "sent",
		Payload:           reqPayloadBytes,
	}
	if err := e.store.SaveMessage(ctx, inEnvelope); err != nil {
		return nil, fmt.Errorf("failed to save converse message: %w", err)
	}

	_ = e.store.EmitEvent(ctx, sessionID, "peer.started", map[string]any{
		"peer":      targetPeer,
		"caller":    caller,
		"operation": "converse",
		"depth":     depth,
	})

	estTokens := int64(len(invReq.Prompt) / 4)
	budgetRes, err := e.reserveBudget(ctx, sessionID, estTokens, 1000)
	if err != nil {
		return nil, err
	}
	defer func() {
		if budgetRes != nil {
			if rErr := e.rollbackBudget(ctx, budgetRes); rErr != nil {
				if retErr != nil {
					retErr = fmt.Errorf("%w (rollback error: %v)", retErr, rErr)
				} else {
					retErr = rErr
				}
			}
		}
	}()

	maxTok, tokErr := checkPeerTokenLimit(peerHarness, targetPeer, budgetRes)
	if tokErr != nil {
		return nil, tokErr
	}
	invReq.MaxTokens = maxTok
	invReq.MaxCostUSD = budgetRes.maxCostUSD

	startTime := time.Now()
	invRes, err := peerHarness.Invoke(ctx, invReq)
	durationMS := time.Since(startTime).Milliseconds()

	if err != nil {
		_ = e.store.EmitEvent(ctx, sessionID, "peer.failed", map[string]any{
			"peer":  targetPeer,
			"error": err.Error(),
		})
		return nil, err
	}

	inTok, outTok, cost := extractTokensAndCost(invReq, invRes)
	if err := e.commitBudget(ctx, budgetRes, inTok, outTok, cost); err != nil {
		return nil, fmt.Errorf("failed to commit converse budget usage: %w", err)
	}
	budgetRes = nil

	// Update persistent peer session mapping if a new session ID was returned
	if invRes.SessionID != "" && invRes.SessionID != peerSessionID {
		_ = e.store.UpdateParticipantHarnessSession(ctx, sessionID, targetPeer, invRes.SessionID)
	}

	// Analyze response type
	respText := strings.TrimSpace(invRes.Text)
	respType := "answer"
	status := ""
	var findings []protocol.FindingPayload
	requiresReply := false

	// Check if the response contains review JSON
	cleanJSON := normalizeReviewJSON(respText)
	var parsedReview protocol.ReviewResult
	if err := json.Unmarshal([]byte(cleanJSON), &parsedReview); err == nil && parsedReview.Verdict != "" {
		respType = "review"
		status = string(parsedReview.Verdict)
		if parsedReview.Verdict == protocol.VerdictApprove {
			respType = "approval"
		}
		for _, f := range parsedReview.Findings {
			fp := protocol.FindingPayload{
				ID:                f.ID,
				SessionID:         sessionID,
				SourceAgent:       targetPeer,
				SourceParticipant: targetPeer,
				SourceAdapter:     peerHarness.AdapterType(),
				Timestamp:         time.Now().UTC(),
				Severity:          f.Severity,
				Claim:             f.Claim,
				Evidence:          f.Evidence,
				Recommendation:    f.Recommendation,
				File:              f.File,
				Line:              f.Line,
				LineStart:         f.Line,
				Status:            protocol.FindingOpen,
			}
			if fp.ID == "" {
				fp.ID = fmt.Sprintf("HM-%03d", len(findings)+1)
			}
			findings = append(findings, fp)
			if err := e.store.SaveFinding(ctx, sessionID, &fp); err != nil {
				return nil, fmt.Errorf("failed to save finding %s: %w", fp.ID, err)
			}
			_ = e.store.EmitEvent(ctx, sessionID, "finding.created", fp)
		}
	} else {
		// Non-JSON conversational text: check question vs approval vs answer
		trimmed := strings.TrimSpace(respText)
		trimmedLower := strings.ToLower(trimmed)
		if strings.HasSuffix(trimmed, "?") ||
			strings.Contains(trimmedLower, "could you clarify") ||
			strings.Contains(trimmedLower, "do you want me to") ||
			strings.Contains(trimmedLower, "what is") ||
			strings.Contains(trimmedLower, "is this") {
			respType = "question"
			requiresReply = true
		} else if strings.Contains(trimmedLower, "approved") ||
			strings.Contains(trimmedLower, "lgtm") ||
			strings.Contains(trimmedLower, "looks good to merge") {
			respType = "approval"
		}
	}

	replyMsgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	convResp := &protocol.ConverseResponse{
		Type:           respType,
		Peer:           targetPeer,
		ConversationID: sessionID,
		MessageID:      replyMsgID,
		Response:       respText,
		Status:         status,
		Findings:       findings,
		RequiresReply:  requiresReply,
	}

	// Persist response envelope
	respPayloadBytes, _ := json.Marshal(convResp)
	outEnvelope := &protocol.PeerEnvelope{
		Protocol:          protocol.PeerProtocolV1,
		ID:                replyMsgID,
		SessionID:         sessionID,
		From:              targetPeer,
		To:                caller,
		Type:              protocol.MsgConverse,
		CreatedAt:         time.Now().UTC(),
		CorrelationID:     corrID,
		CausationID:       reqMsgID,
		Depth:             depth,
		IdempotencyKey:    idempotencyKey,
		ExternalSessionID: invRes.SessionID,
		DurationMS:        durationMS,
		Status:            "delivered",
		Payload:           respPayloadBytes,
	}
	if err := e.store.SaveMessage(ctx, outEnvelope); err != nil {
		return nil, fmt.Errorf("failed to save converse response message: %w", err)
	}

	if idempotencyKey != "" {
		if err := e.store.SaveIdempotency(ctx, idempotencyKey, sessionID, string(respPayloadBytes)); err != nil {
			return nil, fmt.Errorf("failed to save idempotency: %w", err)
		}
	}

	_ = e.store.EmitEvent(ctx, sessionID, "peer.completed", map[string]any{
		"peer":      targetPeer,
		"operation": "converse",
		"type":      respType,
	})

	return convResp, nil
}

func (e *Engine) findHarness(id, adapter string) agent.Harness {
	if h, ok := e.harnesses[id]; ok && h != nil {
		return h
	}
	if h, ok := e.harnesses[adapter]; ok && h != nil {
		return h
	}
	return nil
}

func (e *Engine) dispatchParticipantEvent(ctx context.Context, space *protocol.CollaborationSpace, p *protocol.SpaceParticipant, evt *protocol.CollaborationEvent) {
	if ctx.Err() != nil {
		return
	}
	if evt.Type == protocol.EventMessageCreated {
		// EventMessageCreated is delivered directly by Publish with channel authorization and activation controls.
		// Skipping here avoids duplicate invocation and double message processing.
		return
	}

	channelID := ""
	if chVal, ok := evt.Payload["channel_id"].(string); ok {
		channelID = chVal
	}
	if channelID != "" {
		ch, err := e.store.GetChannel(ctx, space.ID, channelID)
		if err != nil || ch == nil {
			return
		}
		if err := e.checkChannelAccess(space, ch, p.ID); err != nil {
			return
		}
	}

	subs, _ := e.store.GetParticipantSubscriptions(ctx, space.ID, p.ID)
	matches := false
	for _, s := range subs {
		if MatchesSubscription(&s, evt, channelID) {
			matches = true
			break
		}
	}
	if !matches {
		return
	}

	shouldAct, reason, err := e.activationCtrl.ShouldActivate(ctx, space, p, nil, evt)
	if err != nil || !shouldAct {
		delID := fmt.Sprintf("del_%s_%s", evt.ID, p.ID)
		status := protocol.DeliveryPending
		if reason == "cooldown_active" || reason == "space_paused" {
			status = protocol.DeliverySkipped
		}
		_ = e.store.RecordEventDelivery(ctx, &protocol.EventDelivery{
			ID:            delID,
			EventID:       evt.ID,
			SpaceID:       space.ID,
			ParticipantID: p.ID,
			Status:        status,
			SkipReason:    reason,
		})
		return
	}

	if allowed, policyReason := e.activationCtrl.CheckPrivacyPolicy(p, evt.Scope); !allowed {
		delID := fmt.Sprintf("del_%s_%s", evt.ID, p.ID)
		_ = e.store.RecordEventDelivery(ctx, &protocol.EventDelivery{
			ID:            delID,
			EventID:       evt.ID,
			SpaceID:       space.ID,
			ParticipantID: p.ID,
			Status:        protocol.DeliverySkipped,
			SkipReason:    policyReason,
		})
		return
	}

	harness := e.findHarness(p.ID, p.Adapter)
	if harness == nil {
		return
	}

	delID := fmt.Sprintf("del_%s_%s", evt.ID, p.ID)
	started := time.Now().UTC()
	_ = e.store.RecordEventDelivery(ctx, &protocol.EventDelivery{
		ID:            delID,
		EventID:       evt.ID,
		SpaceID:       space.ID,
		ParticipantID: p.ID,
		Status:        protocol.DeliveryProcessing,
		Attempt:       1,
		StartedAt:     &started,
	})

	invCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	prompt := fmt.Sprintf("Collaboration Event in space %s:\nType: %s\nSource: %s\nFiles: %s\nAnalyze and provide your findings or response.", space.ID, evt.Type, evt.Source, strings.Join(evt.Scope, ", "))
	invReq := agent.InvokeRequest{
		Name:       p.ID,
		Repo:       space.WorkspaceID,
		Prompt:     prompt,
		ReviewMode: true,
	}

	estTokens := int64(len(invReq.Prompt) / 4)
	budgetRes, err := e.reserveBudget(ctx, space.ID, estTokens, 1000)
	if err != nil {
		_ = e.store.RecordEventDelivery(ctx, &protocol.EventDelivery{
			ID:            delID,
			EventID:       evt.ID,
			SpaceID:       space.ID,
			ParticipantID: p.ID,
			Status:        protocol.DeliverySkipped,
			SkipReason:    "budget_exceeded",
		})
		return
	}
	maxTok, tokErr := checkPeerTokenLimit(harness, p.ID, budgetRes)
	if tokErr != nil {
		skipReason := "hard_token_limit_unsupported"
		if rErr := e.rollbackBudget(ctx, budgetRes); rErr != nil {
			skipReason = fmt.Sprintf("hard_token_limit_unsupported (rollback error: %v)", rErr)
		}
		_ = e.store.RecordEventDelivery(ctx, &protocol.EventDelivery{
			ID:            delID,
			EventID:       evt.ID,
			SpaceID:       space.ID,
			ParticipantID: p.ID,
			Status:        protocol.DeliverySkipped,
			SkipReason:    skipReason,
		})
		return
	}
	invReq.MaxTokens = maxTok
	invReq.MaxCostUSD = budgetRes.maxCostUSD

	invRes, err := harness.Invoke(invCtx, invReq)
	if err != nil {
		statusMsg := err.Error()
		if rErr := e.rollbackBudget(ctx, budgetRes); rErr != nil {
			statusMsg = fmt.Sprintf("invocation error: %v; rollback error: %v", err, rErr)
		}
		_ = e.store.UpdateEventDeliveryStatus(ctx, delID, protocol.DeliveryFailed, "", statusMsg)
		return
	}

	inTok, outTok, cost := extractTokensAndCost(invReq, invRes)
	if err := e.commitBudget(ctx, budgetRes, inTok, outTok, cost); err != nil {
		var bErr *protocol.BudgetExceededError
		if errors.As(err, &bErr) {
			_ = e.store.UpdateEventDeliveryStatus(ctx, delID, protocol.DeliverySkipped, "", "budget_exceeded")
		} else {
			_ = e.store.UpdateEventDeliveryStatus(ctx, delID, protocol.DeliveryFailed, "", err.Error())
		}
		return
	}

	resultMsgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	resEnv := &protocol.PeerEnvelope{
		Protocol:  protocol.CollaborationProtocolV1,
		ID:        resultMsgID,
		SpaceID:   space.ID,
		ChannelID: "findings",
		From:      p.ID,
		To:        "channel:findings",
		Type:      protocol.MsgMessage,
		Scope:     evt.Scope,
		Payload:   []byte(fmt.Sprintf(`{"text":%q}`, invRes.Text)),
		CreatedAt: time.Now().UTC(),
	}
	if err := e.store.SaveMessage(ctx, resEnv); err != nil {
		_ = e.store.UpdateEventDeliveryStatus(ctx, delID, protocol.DeliveryFailed, "", err.Error())
		return
	}

	var parsed protocol.ReviewResult
	cleanJSON := normalizeReviewJSON(invRes.Text)
	if err := json.Unmarshal([]byte(cleanJSON), &parsed); err == nil && len(parsed.Findings) > 0 {
		for _, f := range parsed.Findings {
			fp := protocol.FindingPayload{
				ID:                f.ID,
				SessionID:         space.ID,
				SourceAgent:       p.ID,
				SourceParticipant: p.ID,
				SourceAdapter:     p.Adapter,
				Timestamp:         time.Now().UTC(),
				Severity:          f.Severity,
				Claim:             f.Claim,
				Evidence:          f.Evidence,
				Recommendation:    f.Recommendation,
				File:              f.File,
				Line:              f.Line,
				LineStart:         f.Line,
				Status:            protocol.FindingOpen,
			}
			_ = e.store.SaveFinding(ctx, space.ID, &fp)
		}
	}

	e.activationCtrl.MarkActivated(space.ID, p.ID)
	_ = e.store.UpdateEventDeliveryStatus(ctx, delID, protocol.DeliveryDelivered, resultMsgID, "")
}

func (e *Engine) handleCollaborationEvent(ctx context.Context, evt *protocol.CollaborationEvent) {
	if evt == nil || evt.SpaceID == "" || ctx.Err() != nil {
		return
	}

	space, err := e.store.GetSpace(ctx, evt.SpaceID)
	if err != nil || space.LifecycleState == protocol.SpaceStatePaused || space.LifecycleState == protocol.SpaceStateStopped {
		return
	}

	var wg sync.WaitGroup
	for _, p := range space.Participants {
		if p.ID == evt.Source {
			continue
		}
		wg.Add(1)
		go func(part protocol.SpaceParticipant) {
			defer wg.Done()
			e.dispatchParticipantEvent(ctx, space, &part, evt)
		}(p)
	}
	wg.Wait()
}

func (e *Engine) Publish(ctx context.Context, req *protocol.PublishRequest) (*protocol.PublishResponse, error) {
	if req.SpaceID == "" {
		return nil, errors.New("space_id is required")
	}

	space, err := e.store.GetSpace(ctx, req.SpaceID)
	if err != nil {
		return nil, err
	}

	if space.LifecycleState == protocol.SpaceStatePaused {
		return nil, &protocol.ParticipantPausedError{Participant: req.From, SpaceID: req.SpaceID}
	}
	if space.LifecycleState == protocol.SpaceStateStopped {
		return nil, &protocol.HumanInterruptedError{Reason: "space is stopped"}
	}

	if req.ChannelID == "" {
		if req.Channel != "" {
			req.ChannelID = req.Channel
		} else {
			req.ChannelID = "general"
		}
	}

	// Channel access authorization check
	ch, err := e.store.GetChannel(ctx, req.SpaceID, req.ChannelID)
	if err != nil {
		// New channel creation: verify sender is authorized in this space
		if err := e.checkChannelAccess(space, nil, req.From); err != nil {
			return nil, err
		}
		ch = &protocol.Channel{
			ID:         req.ChannelID,
			SpaceID:    req.SpaceID,
			Name:       req.ChannelID,
			Visibility: protocol.ChannelVisibilityAll,
			CreatedBy:  req.From,
			CreatedAt:  time.Now().UTC(),
		}
		if err := e.store.CreateChannel(ctx, ch); err != nil {
			return nil, fmt.Errorf("failed to create channel: %w", err)
		}
	} else {
		// Existing channel: verify authorization
		if err := e.checkChannelAccess(space, ch, req.From); err != nil {
			return nil, err
		}
	}

	// Ensure thread exists
	if req.ThreadID == "" {
		threadID := fmt.Sprintf("th_%d", time.Now().UnixNano())
		title := req.Subject
		if title == "" {
			title = fmt.Sprintf("Discussion by %s", req.From)
		}
		th := &protocol.Thread{
			ID:        threadID,
			SpaceID:   req.SpaceID,
			ChannelID: req.ChannelID,
			Title:     title,
			Status:    protocol.ThreadStatusOpen,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := e.store.CreateThread(ctx, th); err != nil {
			return nil, fmt.Errorf("failed to create thread: %w", err)
		}
		req.ThreadID = threadID
	}

	msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	payloadBytes := req.Payload
	if len(payloadBytes) == 0 || string(payloadBytes) == "{}" {
		if req.Message != "" {
			payloadBytes, _ = json.Marshal(map[string]string{"text": req.Message})
		} else {
			payloadBytes = []byte("{}")
		}
	}

	env := &protocol.PeerEnvelope{
		Protocol:  protocol.CollaborationProtocolV1,
		ID:        msgID,
		SpaceID:   req.SpaceID,
		ChannelID: req.ChannelID,
		ThreadID:  req.ThreadID,
		From:      req.From,
		To:        fmt.Sprintf("channel:%s", req.ChannelID),
		Type:      protocol.MsgMessage,
		Mentions:  req.Mentions,
		ReplyTo:   req.ReplyTo,
		Scope:     req.Scope,
		Metadata:  req.Metadata,
		Payload:   payloadBytes,
		CreatedAt: time.Now().UTC(),
	}

	if err := e.store.SaveMessage(ctx, env); err != nil {
		return nil, err
	}

	// Emit message created event
	if err := e.eventBus.Publish(ctx, &protocol.CollaborationEvent{
		ID:        fmt.Sprintf("evt_msg_%s", msgID),
		SpaceID:   req.SpaceID,
		Type:      protocol.EventMessageCreated,
		Source:    req.From,
		Timestamp: time.Now().UTC(),
		Scope:     req.Scope,
		Payload: map[string]any{
			"message_id": msgID,
			"channel_id": req.ChannelID,
			"thread_id":  req.ThreadID,
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to publish message event: %w", err)
	}

	var deliveredTo []string
	var pendingInbox []string
	var activeRuns []string
	var dispatchWG sync.WaitGroup
	var dispatchMu sync.Mutex
	var dispatchErr error
	setDispatchErr := func(err error) {
		if err == nil {
			return
		}
		dispatchMu.Lock()
		if dispatchErr == nil {
			dispatchErr = err
		}
		dispatchMu.Unlock()
	}
	recordDelivery := func(d *protocol.EventDelivery) bool {
		if err := e.store.RecordEventDelivery(ctx, d); err != nil {
			setDispatchErr(fmt.Errorf("record delivery %s: %w", d.ID, err))
			return false
		}
		return true
	}
	updateDelivery := func(id string, status protocol.DeliveryStatus, resultMsgID, reason string) {
		if err := e.store.UpdateEventDeliveryStatus(ctx, id, status, resultMsgID, reason); err != nil {
			setDispatchErr(fmt.Errorf("update delivery %s: %w", id, err))
		}
	}

	for _, p := range space.Participants {
		if p.ID == req.From {
			continue
		}
		participant := p
		dispatchWG.Add(1)
		go func() {
			defer dispatchWG.Done()
			p := participant

			isMentioned := false
			for _, m := range req.Mentions {
				if m == p.ID || m == "@"+p.ID {
					isMentioned = true
					break
				}
			}

			subs, _ := e.store.GetParticipantSubscriptions(ctx, space.ID, p.ID)
			isSubscribed := false
			for _, s := range subs {
				for _, ch := range s.Channels {
					if ch == "*" || ch == req.ChannelID {
						isSubscribed = true
						break
					}
				}
				if isSubscribed {
					break
				}
			}

			if !isMentioned && !isSubscribed {
				return
			}

			// Verify participant has authorization to access this channel
			if err := e.checkChannelAccess(space, ch, p.ID); err != nil {
				return
			}

			shouldAct, reason, err := e.activationCtrl.ShouldActivate(ctx, space, &p, env, nil)
			if err != nil {
				setDispatchErr(err)
				return
			}

			delID := fmt.Sprintf("del_%s_%s", msgID, p.ID)
			if !shouldAct {
				status := protocol.DeliveryPending
				if reason == "cooldown_active" || reason == "space_paused" {
					status = protocol.DeliverySkipped
				}
				if !recordDelivery(&protocol.EventDelivery{
					ID:            delID,
					EventID:       msgID,
					SpaceID:       space.ID,
					ParticipantID: p.ID,
					Status:        status,
					SkipReason:    reason,
				}) {
					return
				}
				dispatchMu.Lock()
				pendingInbox = append(pendingInbox, p.ID)
				dispatchMu.Unlock()
				return
			}

			if allowed, policyReason := e.activationCtrl.CheckPrivacyPolicy(&p, req.Scope); !allowed {
				if !recordDelivery(&protocol.EventDelivery{
					ID:            delID,
					EventID:       msgID,
					SpaceID:       space.ID,
					ParticipantID: p.ID,
					Status:        protocol.DeliverySkipped,
					SkipReason:    policyReason,
				}) {
					return
				}
				return
			}

			harness := e.findHarness(p.ID, p.Adapter)
			if harness == nil {
				dispatchMu.Lock()
				pendingInbox = append(pendingInbox, p.ID)
				dispatchMu.Unlock()
				return
			}

			started := time.Now().UTC()
			if !recordDelivery(&protocol.EventDelivery{
				ID:            delID,
				EventID:       msgID,
				SpaceID:       space.ID,
				ParticipantID: p.ID,
				Status:        protocol.DeliveryProcessing,
				Attempt:       1,
				StartedAt:     &started,
			}) {
				return
			}
			dispatchMu.Lock()
			activeRuns = append(activeRuns, p.ID)
			dispatchMu.Unlock()

			prompt := fmt.Sprintf("Message from %s in channel #%s (thread %s):\n\n%s", req.From, req.ChannelID, req.ThreadID, string(payloadBytes))
			invReq := agent.InvokeRequest{
				Name:       p.ID,
				Repo:       space.WorkspaceID,
				Prompt:     prompt,
				ReviewMode: true,
			}

			estTokens := int64(len(invReq.Prompt) / 4)
			budgetRes, err := e.reserveBudget(ctx, space.ID, estTokens, 1000)
			if err != nil {
				if !recordDelivery(&protocol.EventDelivery{
					ID:            delID,
					EventID:       msgID,
					SpaceID:       space.ID,
					ParticipantID: p.ID,
					Status:        protocol.DeliverySkipped,
					SkipReason:    "budget_exceeded",
				}) {
					return
				}
				return
			}
			maxTok, tokErr := checkPeerTokenLimit(harness, p.ID, budgetRes)
			if tokErr != nil {
				skipReason := "hard_token_limit_unsupported"
				if rErr := e.rollbackBudget(ctx, budgetRes); rErr != nil {
					skipReason = fmt.Sprintf("hard_token_limit_unsupported (rollback error: %v)", rErr)
				}
				if !recordDelivery(&protocol.EventDelivery{
					ID:            delID,
					EventID:       msgID,
					SpaceID:       space.ID,
					ParticipantID: p.ID,
					Status:        protocol.DeliverySkipped,
					SkipReason:    skipReason,
				}) {
					return
				}
				return
			}
			invReq.MaxTokens = maxTok
			invReq.MaxCostUSD = budgetRes.maxCostUSD

			invRes, err := harness.Invoke(ctx, invReq)
			if err != nil {
				statusMsg := err.Error()
				if rErr := e.rollbackBudget(ctx, budgetRes); rErr != nil {
					statusMsg = fmt.Sprintf("invocation error: %v; rollback error: %v", err, rErr)
				}
				updateDelivery(delID, protocol.DeliveryFailed, "", statusMsg)
				return
			}

			inTok, outTok, cost := extractTokensAndCost(invReq, invRes)
			if err := e.commitBudget(ctx, budgetRes, inTok, outTok, cost); err != nil {
				var bErr *protocol.BudgetExceededError
				if errors.As(err, &bErr) {
					updateDelivery(delID, protocol.DeliverySkipped, "", "budget_exceeded")
				} else {
					updateDelivery(delID, protocol.DeliveryFailed, "", err.Error())
				}
				return
			}

			respMsgID := fmt.Sprintf("msg_resp_%d", time.Now().UnixNano())
			respEnv := &protocol.PeerEnvelope{
				Protocol:  protocol.CollaborationProtocolV1,
				ID:        respMsgID,
				SpaceID:   req.SpaceID,
				ChannelID: req.ChannelID,
				ThreadID:  req.ThreadID,
				From:      p.ID,
				To:        req.From,
				Type:      protocol.MsgMessage,
				ReplyTo:   msgID,
				Scope:     req.Scope,
				Payload:   []byte(fmt.Sprintf(`{"text":%q}`, invRes.Text)),
				CreatedAt: time.Now().UTC(),
			}
			if err := e.store.SaveMessage(ctx, respEnv); err != nil {
				updateDelivery(delID, protocol.DeliveryFailed, "", err.Error())
				return
			}

			var parsedResp protocol.ReviewResult
			cleanJSONResp := normalizeReviewJSON(invRes.Text)
			if err := json.Unmarshal([]byte(cleanJSONResp), &parsedResp); err == nil && len(parsedResp.Findings) > 0 {
				for _, f := range parsedResp.Findings {
					fp := protocol.FindingPayload{
						ID:                f.ID,
						SessionID:         space.ID,
						SourceAgent:       p.ID,
						SourceParticipant: p.ID,
						SourceAdapter:     p.Adapter,
						Timestamp:         time.Now().UTC(),
						Severity:          f.Severity,
						Claim:             f.Claim,
						Evidence:          f.Evidence,
						Recommendation:    f.Recommendation,
						File:              f.File,
						Line:              f.Line,
						LineStart:         f.Line,
						Status:            protocol.FindingOpen,
					}
					if err := e.store.SaveFinding(ctx, space.ID, &fp); err != nil {
						updateDelivery(delID, protocol.DeliveryFailed, "", fmt.Sprintf("save finding: %v", err))
						return
					}
				}
			}

			e.activationCtrl.MarkActivated(space.ID, p.ID)
			updateDelivery(delID, protocol.DeliveryDelivered, respMsgID, "")
			dispatchMu.Lock()
			deliveredTo = append(deliveredTo, p.ID)
			dispatchMu.Unlock()
		}()
	}
	dispatchWG.Wait()
	if dispatchErr != nil {
		return nil, dispatchErr
	}

	return &protocol.PublishResponse{
		MessageID:    msgID,
		ThreadID:     req.ThreadID,
		ChannelID:    req.ChannelID,
		Status:       "delivered",
		DeliveredTo:  deliveredTo,
		PendingInbox: pendingInbox,
		ActiveRuns:   activeRuns,
	}, nil
}

func (e *Engine) PublishReply(ctx context.Context, spaceID, channelID, threadID, from, text string, mentions, scope []string) (*protocol.PublishResponse, error) {
	rawPayload, _ := json.Marshal(map[string]string{"text": text})
	req := &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: channelID,
		ThreadID:  threadID,
		From:      from,
		Mentions:  mentions,
		Scope:     scope,
		Payload:   rawPayload,
	}
	return e.Publish(ctx, req)
}

func (e *Engine) GetInbox(ctx context.Context, spaceID, participantID string, since *time.Time, unreadOnly bool) (*protocol.AgentInbox, error) {
	cursor, err := e.store.GetParticipantCursor(ctx, spaceID, participantID)
	if err != nil {
		return nil, err
	}

	msgs, err := e.store.GetSpaceMessages(ctx, spaceID, "", "", 200)
	if err != nil {
		return nil, err
	}

	var items []protocol.InboxItem
	for _, m := range msgs {
		if since != nil && m.CreatedAt.Before(*since) {
			continue
		}

		isDirect := m.To == participantID || m.To == "participant:"+participantID
		mentionsMe := false
		for _, men := range m.Mentions {
			if men == participantID || men == "@"+participantID {
				mentionsMe = true
				break
			}
		}

		if !isDirect && !mentionsMe && m.From == participantID {
			continue
		}

		summary := string(m.Payload)
		if len(summary) > 120 {
			summary = summary[:120] + "..."
		}

		items = append(items, protocol.InboxItem{
			ID:               m.ID,
			SpaceID:          m.SpaceID,
			ChannelID:        m.ChannelID,
			ThreadID:         m.ThreadID,
			From:             m.From,
			Type:             m.Type,
			Summary:          summary,
			CreatedAt:        m.CreatedAt,
			RequiresResponse: mentionsMe || isDirect,
			MentionsMe:       mentionsMe,
			IsDirect:         isDirect,
			Payload:          m.Payload,
		})
	}

	if len(msgs) > 0 {
		cursor.LastSeenMessageID = msgs[len(msgs)-1].ID
		_ = e.store.UpdateParticipantCursor(ctx, cursor)
	}

	return &protocol.AgentInbox{
		ParticipantID: participantID,
		SpaceID:       spaceID,
		Items:         items,
		UnreadCount:   len(items),
	}, nil
}

func (e *Engine) CreateDecision(ctx context.Context, spaceID, title, statement, rationale, proposedBy string, evidenceRefs []string) (*protocol.Decision, error) {
	decID := fmt.Sprintf("dec_%d", time.Now().UnixNano())
	now := time.Now().UTC()
	d := &protocol.Decision{
		ID:           decID,
		SpaceID:      spaceID,
		Title:        title,
		Statement:    statement,
		Rationale:    rationale,
		EvidenceRefs: evidenceRefs,
		ProposedBy:   proposedBy,
		Status:       protocol.DecisionProposed,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := e.store.SaveDecision(ctx, d); err != nil {
		return nil, err
	}

	announcement := fmt.Sprintf("PROPOSED DECISION: %s\nStatement: %s\nRationale: %s", title, statement, rationale)
	rawPayload, _ := json.Marshal(map[string]string{"text": announcement})
	_, _ = e.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "decisions",
		Subject:   fmt.Sprintf("Decision: %s", title),
		From:      proposedBy,
		Payload:   rawPayload,
	})

	_ = e.eventBus.Publish(ctx, &protocol.CollaborationEvent{
		ID:        fmt.Sprintf("evt_dec_%s", decID),
		SpaceID:   spaceID,
		Type:      protocol.EventDecisionCreated,
		Source:    proposedBy,
		Timestamp: now,
		Payload: map[string]any{
			"decision_id": decID,
			"title":       title,
		},
	})

	return d, nil
}

func (e *Engine) AcceptDecision(ctx context.Context, spaceID, decisionID, participantID string) (*protocol.Decision, error) {
	d, err := e.store.GetDecision(ctx, spaceID, decisionID)
	if err != nil {
		return nil, err
	}

	alreadyAccepted := false
	for _, a := range d.AcceptedBy {
		if a == participantID {
			alreadyAccepted = true
			break
		}
	}
	if !alreadyAccepted {
		d.AcceptedBy = append(d.AcceptedBy, participantID)
	}

	d.Status = protocol.DecisionAccepted
	d.UpdatedAt = time.Now().UTC()

	if err := e.store.SaveDecision(ctx, d); err != nil {
		return nil, err
	}

	announcement := fmt.Sprintf("DECISION ACCEPTED: %s by %s", d.Title, participantID)
	rawPayload, _ := json.Marshal(map[string]string{"text": announcement})
	_, _ = e.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "decisions",
		Subject:   fmt.Sprintf("Decision Accepted: %s", d.Title),
		From:      participantID,
		Payload:   rawPayload,
	})

	return d, nil
}

func (e *Engine) SpaceStatus(ctx context.Context, spaceID string) (*protocol.CollaborationStatusResponse, error) {
	space, err := e.store.GetSpace(ctx, spaceID)
	if err != nil {
		return nil, err
	}

	channels, _ := e.store.ListChannels(ctx, spaceID)
	_, _ = e.store.ListThreads(ctx, spaceID, "")
	decisions, _ := e.store.ListDecisions(ctx, spaceID)
	findings, _ := e.store.GetFindings(ctx, spaceID)
	msgs, _ := e.store.GetSpaceMessages(ctx, spaceID, "", "", 1000)

	var chNames []string
	for _, ch := range channels {
		chNames = append(chNames, ch.Name)
	}

	openCount := 0
	disputedCount := 0
	resolvedCount := 0
	for _, f := range findings {
		switch f.Status {
		case protocol.FindingOpen:
			openCount++
		case protocol.FindingDisputed:
			disputedCount++
		case protocol.FindingResolved, protocol.FindingAcknowledged, protocol.FindingDismissed:
			resolvedCount++
		}
	}

	return &protocol.CollaborationStatusResponse{
		SpaceID:             space.ID,
		Title:               space.Title,
		LifecycleState:      space.LifecycleState,
		WriterParticipant:   space.WriterParticipant,
		Participants:        space.Participants,
		Channels:            chNames,
		OpenFindings:        openCount,
		DisputedFindings:    disputedCount,
		ResolvedFindings:    resolvedCount,
		DecisionsCount:      len(decisions),
		TotalMessages:       len(msgs),
		RemainingPeerRounds: 10,
	}, nil
}

func (e *Engine) Subscribe(ctx context.Context, sub *protocol.Subscription) error {
	if sub.ID == "" {
		sub.ID = fmt.Sprintf("sub_%d", time.Now().UnixNano())
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = time.Now().UTC()
	}
	if sub.SpaceID != "" && sub.SpaceID != "*" {
		space, err := e.store.GetSpace(ctx, sub.SpaceID)
		if err != nil {
			return err
		}
		if len(sub.Channels) > 0 {
			for _, chID := range sub.Channels {
				ch, err := e.store.GetChannel(ctx, sub.SpaceID, chID)
				if err == nil && ch != nil {
					if err := e.checkChannelAccess(space, ch, sub.ParticipantID); err != nil {
						return err
					}
				}
			}
		}
	}
	return e.store.SaveSubscription(ctx, sub)
}

func (e *Engine) Unsubscribe(ctx context.Context, spaceID, id string) error {
	return e.store.DeleteSubscription(ctx, spaceID, id)
}
