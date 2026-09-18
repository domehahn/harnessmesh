package collaboration

import (
	"context"
	"fmt"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

// DefaultChannels returns the canonical default channels for any collaboration space.
func DefaultChannels(spaceID string) map[string]protocol.Channel {
	now := time.Now().UTC()
	return map[string]protocol.Channel{
		"general": {
			ID:          "general",
			SpaceID:     spaceID,
			Name:        "general",
			Description: "General collaboration, coordination and status updates",
			Visibility:  protocol.ChannelVisibilityAll,
			CreatedBy:   "system",
			CreatedAt:   now,
		},
		"architecture": {
			ID:          "architecture",
			SpaceID:     spaceID,
			Name:        "architecture",
			Description: "Architectural proposals, structural designs, and boundary discussions",
			Visibility:  protocol.ChannelVisibilityAll,
			CreatedBy:   "system",
			CreatedAt:   now,
		},
		"security": {
			ID:          "security",
			SpaceID:     spaceID,
			Name:        "security",
			Description: "Security findings, threat models, and vulnerability audits",
			Visibility:  protocol.ChannelVisibilityAll,
			CreatedBy:   "system",
			CreatedAt:   now,
		},
		"testing": {
			ID:          "testing",
			SpaceID:     spaceID,
			Name:        "testing",
			Description: "Test plans, test execution results, and quality verification",
			Visibility:  protocol.ChannelVisibilityAll,
			CreatedBy:   "system",
			CreatedAt:   now,
		},
		"findings": {
			ID:          "findings",
			SpaceID:     spaceID,
			Name:        "findings",
			Description: "Formal code findings, challenges, and dispute resolutions",
			Visibility:  protocol.ChannelVisibilityAll,
			CreatedBy:   "system",
			CreatedAt:   now,
		},
		"decisions": {
			ID:          "decisions",
			SpaceID:     spaceID,
			Name:        "decisions",
			Description: "Evidence-based decisions, consensus records, and architectural choices",
			Visibility:  protocol.ChannelVisibilityAll,
			CreatedBy:   "system",
			CreatedAt:   now,
		},
	}
}

// SpaceService manages the lifecycle and contents of collaboration spaces.
type SpaceService struct {
	store store.Store
}

func NewSpaceService(st store.Store) *SpaceService {
	return &SpaceService{store: st}
}

// CreateSpace initializes a new collaboration space with default channels and participants.
func (s *SpaceService) CreateSpace(ctx context.Context, id, workspaceID, title, purpose, writerParticipant string, participants map[string]protocol.SpaceParticipant) (*protocol.CollaborationSpace, error) {
	if id == "" {
		id = fmt.Sprintf("space_%d", time.Now().UnixNano())
	}
	if workspaceID == "" {
		workspaceID = "."
	}

	// Validate single-writer invariant
	writableCount := 0
	for _, p := range participants {
		if p.Writable {
			writableCount++
			if writerParticipant == "" {
				writerParticipant = p.ID
			}
		}
	}
	if writableCount > 1 {
		return nil, &protocol.WriterConflictError{
			Reason: fmt.Sprintf("collaboration space %q cannot have more than 1 writable participant (found %d)", id, writableCount),
		}
	}

	now := time.Now().UTC()
	space := &protocol.CollaborationSpace{
		ID:                id,
		WorkspaceID:       workspaceID,
		Title:             title,
		Purpose:           purpose,
		LifecycleState:    protocol.SpaceStateActive,
		WriterParticipant: writerParticipant,
		Participants:      participants,
		Channels:          DefaultChannels(id),
		Metadata:          make(map[string]any),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.store.SaveSpace(ctx, space); err != nil {
		return nil, fmt.Errorf("failed to save space: %w", err)
	}

	// Create default subscriptions for participants
	for _, p := range participants {
		sub := &protocol.Subscription{
			ID:            fmt.Sprintf("sub_%s_default", p.ID),
			SpaceID:       id,
			ParticipantID: p.ID,
			Channels:      []string{"general", "findings", "decisions"},
			EventTypes:    []string{protocol.EventFindingCreated, protocol.EventDecisionCreated, protocol.EventMessageCreated},
			Mode:          p.Mode,
			CreatedAt:     now,
		}
		_ = s.store.SaveSubscription(ctx, sub)
	}

	return space, nil
}

func (s *SpaceService) GetSpace(ctx context.Context, id string) (*protocol.CollaborationSpace, error) {
	return s.store.GetSpace(ctx, id)
}

func (s *SpaceService) ListSpaces(ctx context.Context) ([]*protocol.CollaborationSpace, error) {
	return s.store.ListSpaces(ctx)
}

func (s *SpaceService) PauseSpace(ctx context.Context, id string) error {
	return s.store.UpdateSpaceLifecycle(ctx, id, protocol.SpaceStatePaused)
}

func (s *SpaceService) ResumeSpace(ctx context.Context, id string) error {
	return s.store.UpdateSpaceLifecycle(ctx, id, protocol.SpaceStateActive)
}

func (s *SpaceService) StopSpace(ctx context.Context, id string) error {
	return s.store.UpdateSpaceLifecycle(ctx, id, protocol.SpaceStateStopped)
}

func (s *SpaceService) ArchiveSpace(ctx context.Context, id string) error {
	return s.store.UpdateSpaceLifecycle(ctx, id, protocol.SpaceStateArchived)
}

func (s *SpaceService) AddParticipant(ctx context.Context, spaceID string, p *protocol.SpaceParticipant) error {
	space, err := s.store.GetSpace(ctx, spaceID)
	if err != nil {
		return err
	}
	if p.Writable && space.WriterParticipant != "" && space.WriterParticipant != p.ID {
		return &protocol.WriterConflictError{
			Reason: fmt.Sprintf("space %q already has writer %q", spaceID, space.WriterParticipant),
		}
	}

	if err := s.store.AddSpaceParticipant(ctx, spaceID, p); err != nil {
		return err
	}

	// Create default subscription
	sub := &protocol.Subscription{
		ID:            fmt.Sprintf("sub_%s_%d", p.ID, time.Now().UnixNano()),
		SpaceID:       spaceID,
		ParticipantID: p.ID,
		Channels:      []string{"general", "findings", "decisions"},
		EventTypes:    []string{protocol.EventFindingCreated, protocol.EventDecisionCreated, protocol.EventMessageCreated},
		Mode:          p.Mode,
		CreatedAt:     time.Now().UTC(),
	}
	return s.store.SaveSubscription(ctx, sub)
}

func (s *SpaceService) UpdateParticipantMode(ctx context.Context, spaceID, participantID string, mode protocol.ParticipantActivityMode) error {
	return s.store.UpdateParticipantMode(ctx, spaceID, participantID, mode)
}

func (s *SpaceService) CreateChannel(ctx context.Context, ch *protocol.Channel) error {
	if ch.ID == "" {
		return fmt.Errorf("channel ID cannot be empty")
	}
	if ch.SpaceID == "" {
		return fmt.Errorf("space ID cannot be empty")
	}
	if ch.Visibility == "" {
		ch.Visibility = protocol.ChannelVisibilityAll
	}
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = time.Now().UTC()
	}
	return s.store.CreateChannel(ctx, ch)
}

func (s *SpaceService) GetChannel(ctx context.Context, spaceID, channelID string) (*protocol.Channel, error) {
	return s.store.GetChannel(ctx, spaceID, channelID)
}

func (s *SpaceService) ListChannels(ctx context.Context, spaceID string) ([]protocol.Channel, error) {
	return s.store.ListChannels(ctx, spaceID)
}

func (s *SpaceService) CreateThread(ctx context.Context, th *protocol.Thread) error {
	if th.ID == "" {
		th.ID = fmt.Sprintf("th_%d", time.Now().UnixNano())
	}
	if th.Status == "" {
		th.Status = protocol.ThreadStatusOpen
	}
	now := time.Now().UTC()
	if th.CreatedAt.IsZero() {
		th.CreatedAt = now
	}
	th.UpdatedAt = now
	return s.store.CreateThread(ctx, th)
}

func (s *SpaceService) GetThread(ctx context.Context, threadID string) (*protocol.Thread, error) {
	return s.store.GetThread(ctx, threadID)
}

func (s *SpaceService) ListThreads(ctx context.Context, spaceID, channelID string) ([]protocol.Thread, error) {
	return s.store.ListThreads(ctx, spaceID, channelID)
}
