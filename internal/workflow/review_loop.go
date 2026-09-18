package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

type Projector interface {
	Capture(context.Context, []string) (protocol.ContextSnapshot, error)
}

type Runner struct {
	Config    *config.Config
	Repo      string
	RunDir    string
	Executor  agent.Agent
	Reviewer  agent.Agent
	Projector Projector
	Store     store.Store
}

func (r *Runner) Run(ctx context.Context, task string) (*protocol.RunResult, error) {
	result := &protocol.RunResult{
		SchemaVersion: 1,
		RunID:         runID(),
		Task:          task,
		Repo:          r.Repo,
		Status:        "running",
		Executor:      r.Config.Workflow.Executor,
		Reviewer:      r.Config.Workflow.Reviewer,
		StartedAt:     time.Now().UTC(),
	}

	if r.Store != nil {
		_ = r.Store.SaveSession(ctx, &store.Session{
			ID:        result.RunID,
			RepoRoot:  r.Repo,
			Task:      task,
			Status:    "active",
			CreatedAt: result.StartedAt,
			UpdatedAt: result.StartedAt,
			Participants: map[string]store.ParticipantInfo{
				r.Config.Workflow.Executor: {AgentName: r.Config.Workflow.Executor, Role: "executor", Writable: true},
				r.Config.Workflow.Reviewer: {AgentName: r.Config.Workflow.Reviewer, Role: "reviewer", Writable: false},
			},
		})
	}

	var executorSession string
	var reviewerSession string
	var previousHash string

	execResult, err := r.Executor.Run(ctx, agent.Request{
		Name:       r.Config.Workflow.Executor,
		Repo:       r.Repo,
		Prompt:     executorInitialPrompt(task),
		SessionID:  executorSession,
		ReviewMode: false,
	})
	if err != nil {
		result.Status = "executor_failed"
		result.StopReason = err.Error()
		result.CompletedAt = time.Now().UTC()
		result.Rounds = append(result.Rounds, protocol.Round{
			Number:      0,
			Executor:    execResult,
			StartedAt:   result.StartedAt,
			CompletedAt: time.Now().UTC(),
		})
		if r.Store != nil {
			_ = r.Store.UpdateSessionStatus(ctx, result.RunID, result.Status, result.StopReason)
		}
		return result, err
	}
	executorSession = execResult.SessionID

	for roundNum := 1; roundNum <= r.Config.Workflow.MaxRounds; roundNum++ {
		started := time.Now().UTC()
		snap, err := r.Projector.Capture(ctx, r.Config.Workflow.TestCommand)
		if err != nil {
			result.Status = "context_failed"
			result.StopReason = err.Error()
			result.CompletedAt = time.Now().UTC()
			return result, err
		}

		reviewerResult, err := r.Reviewer.Run(ctx, agent.Request{
			Name:         r.Config.Workflow.Reviewer,
			Repo:         r.Repo,
			Prompt:       reviewerPrompt(task, roundNum, execResult, snap),
			SessionID:    reviewerSession,
			ReviewMode:   true,
			ReviewSchema: ReviewSchema,
		})
		if err != nil {
			result.Status = "reviewer_failed"
			result.StopReason = err.Error()
			result.CompletedAt = time.Now().UTC()
			return result, err
		}
		reviewerSession = reviewerResult.SessionID

		var review protocol.ReviewResult
		if err := json.Unmarshal([]byte(normalizeReviewJSON(reviewerResult.Text)), &review); err != nil {
			result.Status = "review_parse_failed"
			result.StopReason = err.Error()
			result.CompletedAt = time.Now().UTC()
			return result, fmt.Errorf("parse reviewer JSON: %w; reviewer text=%s", err, reviewerResult.Text)
		}
		if err := validateReview(review); err != nil {
			result.Status = "invalid_review"
			result.StopReason = err.Error()
			result.CompletedAt = time.Now().UTC()
			return result, err
		}

		hash := findingHash(review)
		result.Rounds = append(result.Rounds, protocol.Round{
			Number:      roundNum,
			Executor:    execResult,
			Context:     snap,
			Reviewer:    reviewerResult,
			Review:      review,
			FindingHash: hash,
			StartedAt:   started,
			CompletedAt: time.Now().UTC(),
		})

		if r.Store != nil {
			for _, f := range review.Findings {
				_ = r.Store.SaveFinding(ctx, result.RunID, &protocol.FindingPayload{
					ID:             f.ID,
					SourceAgent:    r.Config.Workflow.Reviewer,
					Severity:       f.Severity,
					Claim:          f.Claim,
					Evidence:       f.Evidence,
					Recommendation: f.Recommendation,
					File:           f.File,
					Line:           f.Line,
					Status:         protocol.FindingOpen,
					Timestamp:      time.Now().UTC(),
				})
			}
		}

		switch review.Verdict {
		case protocol.VerdictApprove:
			result.Status = "approved"
			result.StopReason = "reviewer approved repository state"
			result.CompletedAt = time.Now().UTC()
			if r.Store != nil {
				_ = r.Store.UpdateSessionStatus(ctx, result.RunID, result.Status, result.StopReason)
			}
			return result, nil

		case protocol.VerdictBlock:
			result.Status = "blocked"
			result.StopReason = review.Summary
			result.CompletedAt = time.Now().UTC()
			return result, fmt.Errorf("reviewer blocked automatic continuation: %s", review.Summary)

		case protocol.VerdictChangesRequired:
			if r.Config.Workflow.StopOnRepeat && hash != "" && hash == previousHash {
				result.Status = "stalled"
				result.StopReason = "same material findings repeated in consecutive rounds"
				result.CompletedAt = time.Now().UTC()
				return result, fmt.Errorf(result.StopReason)
			}
			previousHash = hash
		default:
			result.Status = "invalid_review"
			result.StopReason = "unknown verdict"
			result.CompletedAt = time.Now().UTC()
			return result, fmt.Errorf("unknown review verdict %q", review.Verdict)
		}

		if roundNum == r.Config.Workflow.MaxRounds {
			break
		}

		execResult, err = r.Executor.Run(ctx, agent.Request{
			Name:       r.Config.Workflow.Executor,
			Repo:       r.Repo,
			Prompt:     executorFeedbackPrompt(task, review),
			SessionID:  executorSession,
			ReviewMode: false,
		})
		if err != nil {
			result.Status = "executor_failed"
			result.StopReason = err.Error()
			result.CompletedAt = time.Now().UTC()
			result.Rounds = append(result.Rounds, protocol.Round{
				Number:      roundNum + 1,
				Executor:    execResult,
				StartedAt:   time.Now().UTC(),
				CompletedAt: time.Now().UTC(),
			})
			if r.Store != nil {
				_ = r.Store.UpdateSessionStatus(ctx, result.RunID, result.Status, result.StopReason)
			}
			return result, err
		}
		executorSession = execResult.SessionID
	}

	result.Status = "max_rounds"
	result.StopReason = fmt.Sprintf("reached max_rounds=%d without approval", r.Config.Workflow.MaxRounds)
	result.CompletedAt = time.Now().UTC()
	return result, fmt.Errorf(result.StopReason)
}

func validateReview(r protocol.ReviewResult) error {
	switch r.Verdict {
	case protocol.VerdictApprove:
		if len(r.Findings) != 0 {
			return fmt.Errorf("approve verdict must not contain findings")
		}
	case protocol.VerdictChangesRequired:
		if len(r.Findings) == 0 {
			return fmt.Errorf("changes_required verdict must contain findings")
		}
	case protocol.VerdictBlock:
	default:
		return fmt.Errorf("invalid verdict %q", r.Verdict)
	}
	for i, f := range r.Findings {
		if strings.TrimSpace(f.ID) == "" ||
			strings.TrimSpace(f.Claim) == "" ||
			strings.TrimSpace(f.Evidence) == "" ||
			strings.TrimSpace(f.Recommendation) == "" {
			return fmt.Errorf("finding %d is missing required fields", i)
		}
	}
	return nil
}

func findingHash(review protocol.ReviewResult) string {
	if len(review.Findings) == 0 {
		return ""
	}
	keys := make([]string, 0, len(review.Findings))
	for _, f := range review.Findings {
		keys = append(keys, strings.ToLower(strings.TrimSpace(f.ID))+"|"+
			strings.ToLower(strings.TrimSpace(f.Claim))+"|"+
			strings.ToLower(strings.TrimSpace(f.File)))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

func runID() string {
	now := time.Now().UTC().Format("20060102T150405")
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return now + "-" + hex.EncodeToString(sum[:4])
}
