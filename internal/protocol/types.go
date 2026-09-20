package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	PeerProtocolV1          = "harnessmesh.peer/v1"
	CollaborationProtocolV1 = "harnessmesh.collaboration/v1"
)

// Legacy / v0.1 Verdict compatibility
type ReviewVerdict string

const (
	VerdictApprove         ReviewVerdict = "approve"
	VerdictChangesRequired ReviewVerdict = "changes_required"
	VerdictBlock           ReviewVerdict = "block"
)

const ReviewSchema = `{
  "type": "object",
  "properties": {
    "verdict": {
      "type": "string",
      "enum": ["approve", "changes_required", "block"]
    },
    "summary": {
      "type": "string"
    },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "severity": {
            "type": "string",
            "enum": ["critical", "high", "medium", "low", "info"]
          },
          "file": {"type": "string"},
          "line": {"type": "integer"},
          "claim": {"type": "string"},
          "evidence": {"type": "string"},
          "recommendation": {"type": "string"}
        },
        "required": ["id", "severity", "file", "line", "claim", "evidence", "recommendation"],
        "additionalProperties": false
      }
    },
    "questions": {
      "type": "array",
      "items": {"type": "string"}
    }
  },
  "required": ["verdict", "summary", "findings", "questions"],
  "additionalProperties": false
}`

// Legacy / v0.1 Finding compatibility
type Finding struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"`
	File           string `json:"file,omitempty"`
	Line           int    `json:"line,omitempty"`
	Claim          string `json:"claim"`
	Evidence       string `json:"evidence"`
	Recommendation string `json:"recommendation"`
}

// Legacy / v0.1 ReviewResult compatibility
type ReviewResult struct {
	Verdict   ReviewVerdict `json:"verdict"`
	Summary   string        `json:"summary"`
	Findings  []Finding     `json:"findings"`
	Questions []string      `json:"questions,omitempty"`
}

// Legacy ContextSnapshot
type ContextSnapshot struct {
	RepoRoot       string   `json:"repo_root"`
	Head           string   `json:"head"`
	Branch         string   `json:"branch"`
	Status         string   `json:"status"`
	DiffStat       string   `json:"diff_stat"`
	Diff           string   `json:"diff"`
	UntrackedFiles []string `json:"untracked_files,omitempty"`
	TestCommand    []string `json:"test_command,omitempty"`
	TestExitCode   *int     `json:"test_exit_code,omitempty"`
	TestOutput     string   `json:"test_output,omitempty"`
	Truncated      bool     `json:"truncated"`
	OriginalChars  int      `json:"original_chars,omitempty"`
	IncludedChars  int      `json:"included_chars,omitempty"`
}

type AgentResult struct {
	AgentName  string         `json:"agent_name"`
	SessionID  string         `json:"session_id,omitempty"`
	Text       string         `json:"text"`
	RawOutput  string         `json:"-"`
	Usage      map[string]any `json:"usage,omitempty"`
	DurationMS int64          `json:"duration_ms"`
}

type Round struct {
	Number      int             `json:"number"`
	Executor    AgentResult     `json:"executor"`
	Context     ContextSnapshot `json:"context"`
	Reviewer    AgentResult     `json:"reviewer"`
	Review      ReviewResult    `json:"review"`
	FindingHash string          `json:"finding_hash"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt time.Time       `json:"completed_at"`
}

type RunResult struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	Task          string    `json:"task"`
	Repo          string    `json:"repo"`
	Status        string    `json:"status"`
	Executor      string    `json:"executor"`
	Reviewer      string    `json:"reviewer"`
	Rounds        []Round   `json:"rounds"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
	StopReason    string    `json:"stop_reason"`
}

// =========================================================================
// v0.2.0 Peer Protocol & v0.3.0 Collaboration Protocol
// =========================================================================

type MessageType string

const (
	MsgReviewRequest MessageType = "review_request"
	MsgReviewResult  MessageType = "review_result"
	MsgQuestion      MessageType = "question"
	MsgAnswer        MessageType = "answer"
	MsgFinding       MessageType = "finding"
	MsgEvidence      MessageType = "evidence"
	MsgChallenge     MessageType = "challenge"
	MsgResolution    MessageType = "resolution"
	MsgReply         MessageType = "reply"
	MsgStatus        MessageType = "status"
	MsgConverse      MessageType = "converse"
	MsgMessage       MessageType = "message"
	MsgDecision      MessageType = "decision"
	MsgHandoff       MessageType = "handoff"
	MsgNotification  MessageType = "notification"
	MsgSystemEvent   MessageType = "system_event"
)

type PeerEnvelope struct {
	Protocol          string          `json:"protocol"`
	ID                string          `json:"id"`
	SessionID         string          `json:"session_id,omitempty"`
	SpaceID           string          `json:"space_id,omitempty"`
	ChannelID         string          `json:"channel_id,omitempty"`
	ThreadID          string          `json:"thread_id,omitempty"`
	From              string          `json:"from"`
	To                string          `json:"to,omitempty"`
	Mentions          []string        `json:"mentions,omitempty"`
	Type              MessageType     `json:"type"`
	CreatedAt         time.Time       `json:"created_at"`
	CorrelationID     string          `json:"correlation_id,omitempty"`
	CausationID       string          `json:"causation_id,omitempty"`
	ReplyTo           string          `json:"reply_to,omitempty"`
	Depth             int             `json:"depth"`
	IdempotencyKey    string          `json:"idempotency_key,omitempty"`
	ExternalSessionID string          `json:"external_session_id,omitempty"`
	DurationMS        int64           `json:"duration_ms,omitempty"`
	Status            string          `json:"status,omitempty"`
	Scope             []string        `json:"scope,omitempty"`
	Metadata          map[string]any  `json:"metadata,omitempty"`
	Payload           json.RawMessage `json:"payload"`

	// Legacy backward compatibility fields
	SchemaVersion int              `json:"schema_version,omitempty"`
	MessageID     string           `json:"message_id,omitempty"`
	Task          string           `json:"task,omitempty"`
	Round         int              `json:"round,omitempty"`
	Context       *ContextSnapshot `json:"context,omitempty"`
	Review        *ReviewResult    `json:"review,omitempty"`
}

func (e *PeerEnvelope) Validate() error {
	if e.Protocol != "" && e.Protocol != PeerProtocolV1 && e.Protocol != CollaborationProtocolV1 {
		return fmt.Errorf("unsupported protocol version %q", e.Protocol)
	}
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("envelope id is required")
	}
	if strings.TrimSpace(e.SessionID) == "" && strings.TrimSpace(e.SpaceID) == "" {
		return errors.New("either session_id or space_id is required")
	}
	if strings.TrimSpace(e.From) == "" {
		return errors.New("from agent is required")
	}
	switch e.Type {
	case MsgReviewRequest, MsgReviewResult, MsgQuestion, MsgAnswer,
		MsgFinding, MsgEvidence, MsgChallenge, MsgResolution, MsgReply, MsgStatus,
		MsgConverse, MsgMessage, MsgDecision, MsgHandoff, MsgNotification, MsgSystemEvent:
	default:
		return fmt.Errorf("unknown message type %q", e.Type)
	}
	return nil
}

// -------------------------------------------------------------------------
// Peer Payloads
// -------------------------------------------------------------------------

type AskContextOptions struct {
	IncludeDiff      bool `json:"include_diff"`
	IncludeTests     bool `json:"include_tests"`
	IncludeGitStatus bool `json:"include_git_status"`
}

type AskRequest struct {
	Peer       string            `json:"peer,omitempty"`
	Capability string            `json:"capability,omitempty"`
	Question   string            `json:"question"`
	Scope      []string          `json:"scope,omitempty"`
	Context    AskContextOptions `json:"context"`
}

type AnswerPayload struct {
	Answer      string   `json:"answer"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	FindingIDs  []string `json:"finding_ids,omitempty"`
}

type ReviewerSpec struct {
	Peer       string   `json:"peer,omitempty"`
	Capability string   `json:"capability,omitempty"`
	Focus      []string `json:"focus,omitempty"`
}

type ReviewRequestPayload struct {
	Peer             string         `json:"peer,omitempty"`
	Capability       string         `json:"capability,omitempty"`
	Reviewers        []ReviewerSpec `json:"reviewers,omitempty"`
	Scope            []string       `json:"scope,omitempty"`
	Focus            []string       `json:"focus,omitempty"`
	ChangedFilesOnly bool           `json:"changed_files_only,omitempty"`
	IncludeDiff      bool           `json:"include_diff"`
	IncludeTests     bool           `json:"include_tests"`
	MinimumSeverity  string         `json:"minimum_severity,omitempty"`
	ReviewProfile    string         `json:"review_profile,omitempty"`
}

type ReviewResultPayload struct {
	Status   string           `json:"status"` // "approve", "changes_required", "block"
	Summary  string           `json:"summary"`
	Findings []FindingPayload `json:"findings"`
}

type FindingStatus string

const (
	FindingOpen         FindingStatus = "open"
	FindingAcknowledged FindingStatus = "acknowledged"
	FindingDisputed     FindingStatus = "disputed"
	FindingResolved     FindingStatus = "resolved"
	FindingDismissed    FindingStatus = "dismissed"
	FindingSuperseded   FindingStatus = "superseded"
)

type FindingPayload struct {
	ID                string        `json:"id"`
	SessionID         string        `json:"session_id,omitempty"`
	SourceAgent       string        `json:"source_agent,omitempty"`
	SourceParticipant string        `json:"source_participant,omitempty"`
	SourceAdapter     string        `json:"source_adapter,omitempty"`
	Timestamp         time.Time     `json:"timestamp"`
	Severity          string        `json:"severity"` // "info", "low", "medium", "high", "critical"
	Category          string        `json:"category"` // "correctness", "concurrency", "security", "style", etc.
	Claim             string        `json:"claim"`
	Evidence          string        `json:"evidence,omitempty"`
	EvidenceRefs      []string      `json:"evidence_refs,omitempty"`
	Recommendation    string        `json:"recommendation"`
	File              string        `json:"file,omitempty"`
	Line              int           `json:"line,omitempty"`
	LineStart         int           `json:"line_start,omitempty"`
	EndLine           int           `json:"end_line,omitempty"`
	LineEnd           int           `json:"line_end,omitempty"`
	Status            FindingStatus `json:"status"`
	CreatedAt         time.Time     `json:"created_at,omitempty"`
	UpdatedAt         time.Time     `json:"updated_at,omitempty"`
	DuplicateOf       string        `json:"duplicate_of,omitempty"`
	RelatedFindings   []string      `json:"related_findings,omitempty"`
}

type ParticipantStatus struct {
	ID       string   `json:"id"`
	Adapter  string   `json:"adapter"`
	Roles    []string `json:"roles"`
	Writable bool     `json:"writable"`
	Status   string   `json:"status"` // "ready", "busy", "offline"
}

type PeerListRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

type PeerListResponse struct {
	Participants []ParticipantStatus `json:"participants"`
}

type PeerCapabilitiesRequest struct {
	Capability string `json:"capability"`
}

type PeerCapabilitiesMatch struct {
	Participant string   `json:"participant"`
	Roles       []string `json:"roles"`
	Writable    bool     `json:"writable"`
}

type PeerCapabilitiesResponse struct {
	Matches []PeerCapabilitiesMatch `json:"matches"`
}

type Workspace struct {
	Root                 string      `json:"root"`
	RepositoryIdentity   string      `json:"repository_identity"`
	Branch               string      `json:"branch"`
	Head                 string      `json:"head"`
	WriterParticipant    string      `json:"writer_participant"`
	ReadOnlyParticipants []string    `json:"read_only_participants"`
	PathPolicy           *PathPolicy `json:"path_policy,omitempty"`
}

type PathPolicy struct {
	AllowedPaths []string `json:"allowed_paths,omitempty"`
	DeniedPaths  []string `json:"denied_paths,omitempty"`
}

type EvidenceType string

const (
	EvidenceCodeLocation    EvidenceType = "code_location"
	EvidenceGitDiff         EvidenceType = "git_diff"
	EvidenceTestResult      EvidenceType = "test_result"
	EvidenceCommandResult   EvidenceType = "command_result"
	EvidenceStaticAnalysis  EvidenceType = "static_analysis"
	EvidenceBuildResult     EvidenceType = "build_result"
	EvidenceLogExcerpt      EvidenceType = "log_excerpt"
	EvidenceUserRequirement EvidenceType = "user_requirement"
	EvidenceBenchmarkResult EvidenceType = "benchmark_result"
	EvidenceReasoningNote   EvidenceType = "reasoning_note"
)

type EvidencePayload struct {
	ID          string         `json:"id"`
	FindingID   string         `json:"finding_id,omitempty"`
	MessageID   string         `json:"message_id,omitempty"`
	SourceAgent string         `json:"source_agent"`
	Type        EvidenceType   `json:"type"`
	Command     string         `json:"command,omitempty"`
	Result      string         `json:"result,omitempty"`
	Excerpt     string         `json:"excerpt,omitempty"`
	ExitCode    *int           `json:"exit_code,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type ChallengePayload struct {
	ID                    string    `json:"id"`
	FindingID             string    `json:"finding_id"`
	Challenger            string    `json:"challenger"`
	Claim                 string    `json:"claim"`
	Evidence              string    `json:"evidence"`
	RequestedVerification string    `json:"requested_verification,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

type ResolutionStatus string

const (
	ResolutionConfirmed          ResolutionStatus = "confirmed"
	ResolutionRejected           ResolutionStatus = "rejected"
	ResolutionPartiallyConfirmed ResolutionStatus = "partially_confirmed"
	ResolutionSuperseded         ResolutionStatus = "superseded"
	ResolutionRequiresHuman      ResolutionStatus = "requires_human"
)

type ResolutionPayload struct {
	ID                 string           `json:"id"`
	FindingID          string           `json:"finding_id"`
	Status             ResolutionStatus `json:"status"`
	Rationale          string           `json:"rationale"`
	EvidenceReferences []string         `json:"evidence_references,omitempty"`
	ResolvingAgent     string           `json:"resolving_agent"`
	Timestamp          time.Time        `json:"timestamp"`
}

type ReplyPayload struct {
	ParentMessageID      string   `json:"parent_message_id"`
	TargetPeer           string   `json:"target_peer"`
	Message              string   `json:"message"`
	EvidenceReferences   []string `json:"evidence_references,omitempty"`
	FindingReferences    []string `json:"finding_references,omitempty"`
	ExpectedResponseType string   `json:"expected_response_type,omitempty"`
}

type BudgetStatus struct {
	Known              bool    `json:"known"`
	StrictTokenCeiling bool    `json:"strict_token_ceiling,omitempty"`
	MaxInputTokens     int64   `json:"max_input_tokens,omitempty"`
	UsedInputTokens    int64   `json:"used_input_tokens,omitempty"`
	MaxOutputTokens    int64   `json:"max_output_tokens,omitempty"`
	UsedOutputTokens   int64   `json:"used_output_tokens,omitempty"`
	MaxTotalTokens     int64   `json:"max_total_tokens,omitempty"`
	UsedTotalTokens    int64   `json:"used_total_tokens,omitempty"`
	MaxCostUSD         float64 `json:"max_cost_usd,omitempty"`
	UsedCostUSD        float64 `json:"used_cost_usd,omitempty"`
}

type StatusPayload struct {
	SessionID           string       `json:"session_id"`
	Participants        []string     `json:"participants"`
	OpenFindings        int          `json:"open_findings"`
	DisputedFindings    int          `json:"disputed_findings"`
	ResolvedFindings    int          `json:"resolved_findings"`
	PeerCalls           int          `json:"peer_calls"`
	RemainingPeerRounds int          `json:"remaining_peer_rounds"`
	RemainingBudget     BudgetStatus `json:"remaining_budget"`
}

type ConverseContextOptions struct {
	IncludeDiff      bool     `json:"include_diff"`
	IncludeTests     bool     `json:"include_tests"`
	IncludeGitStatus bool     `json:"include_git_status"`
	Files            []string `json:"files,omitempty"`
}

type ConverseRequest struct {
	Peer                 string                 `json:"peer,omitempty"`
	Capability           string                 `json:"capability,omitempty"`
	Message              string                 `json:"message"`
	ConversationID       string                 `json:"conversation_id,omitempty"`
	ParentMessageID      string                 `json:"parent_message_id,omitempty"`
	CorrelationID        string                 `json:"correlation_id,omitempty"`
	CausationID          string                 `json:"causation_id,omitempty"`
	Scope                []string               `json:"scope,omitempty"`
	Context              ConverseContextOptions `json:"context,omitempty"`
	ExpectedResponseType string                 `json:"expected_response_type,omitempty"` // "any", "answer", "review", "question"
	IdempotencyKey       string                 `json:"idempotency_key,omitempty"`
	ApprovalID           string                 `json:"approval_id,omitempty"`
}

type ConverseResponse struct {
	Type           string           `json:"type"` // "answer", "review", "question", "challenge", "approval", "requires_human"
	Peer           string           `json:"peer"`
	ConversationID string           `json:"conversation_id"`
	MessageID      string           `json:"message_id"`
	Response       string           `json:"response"`
	Status         string           `json:"status,omitempty"` // e.g. "changes_required", "approved" if review
	Findings       []FindingPayload `json:"findings,omitempty"`
	Evidence       []string         `json:"evidence,omitempty"`
	RequiresReply  bool             `json:"requires_reply"`
}

// =========================================================================
// v0.3.0 Collaboration Space Domain Types
// =========================================================================

type SpaceLifecycleState string

const (
	SpaceStateActive   SpaceLifecycleState = "active"
	SpaceStatePaused   SpaceLifecycleState = "paused"
	SpaceStateStopped  SpaceLifecycleState = "stopped"
	SpaceStateArchived SpaceLifecycleState = "archived"
)

type ParticipantActivityMode string

const (
	ParticipantModeActive   ParticipantActivityMode = "active"
	ParticipantModePassive  ParticipantActivityMode = "passive"
	ParticipantModeOnDemand ParticipantActivityMode = "on_demand"
	ParticipantModePaused   ParticipantActivityMode = "paused"
)

type ChannelVisibility string

const (
	ChannelVisibilityAll                  ChannelVisibility = "all_participants"
	ChannelVisibilitySelectedParticipants ChannelVisibility = "selected_participants"
	ChannelVisibilitySelectedCapabilities ChannelVisibility = "selected_capabilities"
)

type Channel struct {
	ID                  string            `json:"id"`
	SpaceID             string            `json:"space_id"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	Visibility          ChannelVisibility `json:"visibility"`
	AllowedParticipants []string          `json:"allowed_participants,omitempty"`
	AllowedCapabilities []string          `json:"allowed_capabilities,omitempty"`
	CreatedBy           string            `json:"created_by"`
	CreatedAt           time.Time         `json:"created_at"`
	ArchivedAt          *time.Time        `json:"archived_at,omitempty"`
}

type ThreadStatus string

const (
	ThreadStatusOpen     ThreadStatus = "open"
	ThreadStatusResolved ThreadStatus = "resolved"
	ThreadStatusArchived ThreadStatus = "archived"
)

type Thread struct {
	ID            string       `json:"id"`
	SpaceID       string       `json:"space_id"`
	ChannelID     string       `json:"channel_id"`
	RootMessageID string       `json:"root_message_id"`
	Title         string       `json:"title"`
	Status        ThreadStatus `json:"status"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type SpaceParticipant struct {
	ID           string                  `json:"id"`
	Adapter      string                  `json:"adapter"`
	Roles        []string                `json:"roles"`
	Capabilities []string                `json:"capabilities"`
	Mode         ParticipantActivityMode `json:"mode"`
	Writable     bool                    `json:"writable"`
	JoinedAt     time.Time               `json:"joined_at"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

type CollaborationSpace struct {
	ID                string                      `json:"id"`
	WorkspaceID       string                      `json:"workspace_id"`
	Title             string                      `json:"title"`
	Purpose           string                      `json:"purpose"`
	LifecycleState    SpaceLifecycleState         `json:"lifecycle_state"`
	WriterParticipant string                      `json:"writer_participant"`
	Budget            BudgetStatus                `json:"budget"`
	Participants      map[string]SpaceParticipant `json:"participants"`
	Channels          map[string]Channel          `json:"channels"`
	Metadata          map[string]any              `json:"metadata,omitempty"`
	CreatedAt         time.Time                   `json:"created_at"`
	UpdatedAt         time.Time                   `json:"updated_at"`
}

type Subscription struct {
	ID            string                  `json:"id"`
	SpaceID       string                  `json:"space_id"`
	ParticipantID string                  `json:"participant_id"`
	Channels      []string                `json:"channels"`
	EventTypes    []string                `json:"event_types"`
	ScopePatterns []string                `json:"scope_patterns,omitempty"`
	Mode          ParticipantActivityMode `json:"mode"`
	CreatedAt     time.Time               `json:"created_at"`
}

// Strongly-typed Collaboration Event constants
const (
	EventRepoChanged       = "repository.changed"
	EventCommitCreated     = "repository.commit_created"
	EventTestsStarted      = "tests.started"
	EventTestsFailed       = "tests.failed"
	EventTestsPassed       = "tests.passed"
	EventBuildStarted      = "build.started"
	EventBuildFailed       = "build.failed"
	EventBuildPassed       = "build.passed"
	EventFindingCreated    = "finding.created"
	EventFindingUpdated    = "finding.updated"
	EventFindingResolved   = "finding.resolved"
	EventEvidenceCreated   = "evidence.created"
	EventMessageCreated    = "message.created"
	EventThreadCreated     = "thread.created"
	EventParticipantJoined = "participant.joined"
	EventParticipantLeft   = "participant.left"
	EventParticipantFailed = "participant.failed"
	EventReviewRequested   = "review.requested"
	EventReviewCompleted   = "review.completed"
	EventDecisionCreated   = "decision.created"
	EventBudgetWarning     = "budget.warning"
	EventBudgetExhausted   = "budget.exhausted"
	EventHumanPause        = "human.pause"
	EventHumanResume       = "human.resume"
	EventHumanStop         = "human.stop"
	EventWorkspaceChanged  = "workspace.changed"
)

type CollaborationEvent struct {
	ID        string         `json:"id"`
	SpaceID   string         `json:"space_id"`
	Type      string         `json:"type"`
	Source    string         `json:"source"`
	Timestamp time.Time      `json:"timestamp"`
	Scope     []string       `json:"scope,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type DeliveryStatus string

const (
	DeliveryPending    DeliveryStatus = "pending"
	DeliveryProcessing DeliveryStatus = "processing"
	DeliveryDelivered  DeliveryStatus = "delivered"
	DeliverySkipped    DeliveryStatus = "skipped"
	DeliveryFailed     DeliveryStatus = "failed"
)

type EventDelivery struct {
	ID              string         `json:"id"`
	EventID         string         `json:"event_id"`
	SpaceID         string         `json:"space_id"`
	ParticipantID   string         `json:"participant_id"`
	Status          DeliveryStatus `json:"status"`
	Attempt         int            `json:"attempt"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	ResultMessageID string         `json:"result_message_id,omitempty"`
	SkipReason      string         `json:"skip_reason,omitempty"`
}

type DecisionStatus string

const (
	DecisionProposed      DecisionStatus = "proposed"
	DecisionAccepted      DecisionStatus = "accepted"
	DecisionRejected      DecisionStatus = "rejected"
	DecisionSuperseded    DecisionStatus = "superseded"
	DecisionRequiresHuman DecisionStatus = "requires_human"
)

type Decision struct {
	ID           string         `json:"id"`
	SpaceID      string         `json:"space_id"`
	Title        string         `json:"title"`
	Statement    string         `json:"statement"`
	Rationale    string         `json:"rationale"`
	EvidenceRefs []string       `json:"evidence_refs,omitempty"`
	ProposedBy   string         `json:"proposed_by"`
	AcceptedBy   []string       `json:"accepted_by,omitempty"`
	Status       DecisionStatus `json:"status"`
	Supersedes   string         `json:"supersedes,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type ParticipantCursor struct {
	SpaceID           string                  `json:"space_id"`
	ParticipantID     string                  `json:"participant_id"`
	Mode              ParticipantActivityMode `json:"mode"`
	LastSeenMessageID string                  `json:"last_seen_message_id"`
	LastSeenEventID   string                  `json:"last_seen_event_id"`
	UpdatedAt         time.Time               `json:"updated_at"`
}

type InboxItem struct {
	ID               string      `json:"id"`
	SpaceID          string      `json:"space_id"`
	ChannelID        string      `json:"channel_id"`
	ThreadID         string      `json:"thread_id,omitempty"`
	From             string      `json:"from"`
	Type             MessageType `json:"type"`
	Summary          string      `json:"summary"`
	CreatedAt        time.Time   `json:"created_at"`
	RequiresResponse bool        `json:"requires_response"`
	MentionsMe       bool        `json:"mentions_me"`
	IsDirect         bool        `json:"is_direct"`
	Payload          any         `json:"payload,omitempty"`
}

type AgentInbox struct {
	ParticipantID string      `json:"participant_id"`
	SpaceID       string      `json:"space_id"`
	UnreadCount   int         `json:"unread_count"`
	Items         []InboxItem `json:"items"`
}

type PublishRequest struct {
	SpaceID        string          `json:"space_id,omitempty"`
	ChannelID      string          `json:"channel_id,omitempty"`
	Channel        string          `json:"channel,omitempty"`
	ThreadID       string          `json:"thread_id,omitempty"`
	From           string          `json:"from,omitempty"`
	To             string          `json:"to,omitempty"`
	Type           MessageType     `json:"type,omitempty"`
	Subject        string          `json:"subject,omitempty"`
	Message        string          `json:"message,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Mentions       []string        `json:"mentions,omitempty"`
	ReplyTo        string          `json:"reply_to,omitempty"`
	Scope          []string        `json:"scope,omitempty"`
	CausationID    string          `json:"causation_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Metadata       map[string]any  `json:"metadata,omitempty"`
	Priority       int             `json:"priority,omitempty"`
}

type PublishResponse struct {
	MessageID    string   `json:"message_id"`
	ThreadID     string   `json:"thread_id,omitempty"`
	ChannelID    string   `json:"channel_id,omitempty"`
	Status       string   `json:"status"`
	DeliveredTo  []string `json:"delivered_to,omitempty"`
	PendingInbox []string `json:"pending_inbox,omitempty"`
	ActiveRuns   []string `json:"active_runs,omitempty"`
}

type CollaborationStatusResponse struct {
	SpaceID             string                      `json:"space_id"`
	Title               string                      `json:"title"`
	LifecycleState      SpaceLifecycleState         `json:"lifecycle_state"`
	WriterParticipant   string                      `json:"writer_participant"`
	Participants        map[string]SpaceParticipant `json:"participants"`
	Channels            []string                    `json:"channels"`
	OpenFindings        int                         `json:"open_findings"`
	DisputedFindings    int                         `json:"disputed_findings"`
	ResolvedFindings    int                         `json:"resolved_findings"`
	DecisionsCount      int                         `json:"decisions_count"`
	TotalMessages       int                         `json:"total_messages"`
	RemainingPeerRounds int                         `json:"remaining_peer_rounds"`
}

// -------------------------------------------------------------------------
// Typed Errors
// -------------------------------------------------------------------------

type PeerUnavailableError struct {
	Peer   string
	Reason string
}

func (e *PeerUnavailableError) Error() string {
	return fmt.Sprintf("peer %q unavailable: %s", e.Peer, e.Reason)
}

type PeerTimeoutError struct {
	Peer    string
	Timeout time.Duration
}

func (e *PeerTimeoutError) Error() string {
	return fmt.Sprintf("peer %q timed out after %s", e.Peer, e.Timeout)
}

type PeerDepthExceededError struct {
	CurrentDepth int
	MaxDepth     int
}

func (e *PeerDepthExceededError) Error() string {
	return fmt.Sprintf("peer invocation depth %d exceeded maximum configured depth %d", e.CurrentDepth, e.MaxDepth)
}

type BudgetExceededError struct {
	Metric string
	Limit  any
	Used   any
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("collaboration budget exceeded for %s: used %v, limit %v", e.Metric, e.Used, e.Limit)
}

type ContextRejectedError struct {
	Path   string
	Reason string
}

func (e *ContextRejectedError) Error() string {
	return fmt.Sprintf("context rejected for path %q: %s", e.Path, e.Reason)
}

type CapabilityUnsupportedError struct {
	Agent      string
	Capability string
}

func (e *CapabilityUnsupportedError) Error() string {
	return fmt.Sprintf("agent %q does not support capability %q", e.Agent, e.Capability)
}

type SessionNotFoundError struct {
	SessionID string
}

func (e *SessionNotFoundError) Error() string {
	return fmt.Sprintf("collaboration session %q not found", e.SessionID)
}

type SessionClosedError struct {
	SessionID string
	Status    string
}

func (e *SessionClosedError) Error() string {
	return fmt.Sprintf("collaboration session %q is closed (status: %s)", e.SessionID, e.Status)
}

type HarnessInvocationFailedError struct {
	Agent  string
	Err    error
	Stderr string
}

// QuotaExceededError indicates that an agent cannot accept more work until a
// known (or fallback) point in time.  RetryAt is deliberately optional because
// providers do not all include a reset timestamp in their error response.
type QuotaExceededError struct {
	Agent   string
	RetryAt time.Time
	Reason  string
}

type AgentHealthStatus string

const (
	AgentHealthUnknown     AgentHealthStatus = "unknown"
	AgentHealthHealthy     AgentHealthStatus = "healthy"
	AgentHealthDegraded    AgentHealthStatus = "degraded"
	AgentHealthUnavailable AgentHealthStatus = "unavailable"
	AgentHealthQuotaWait   AgentHealthStatus = "quota_wait"
	AgentHealthCircuitOpen AgentHealthStatus = "circuit_open"
)

type AgentHealth struct {
	Agent            string            `json:"agent"`
	Adapter          string            `json:"adapter,omitempty"`
	Status           AgentHealthStatus `json:"status"`
	ConsecutiveFails int               `json:"consecutive_failures"`
	TotalInvocations int64             `json:"total_invocations"`
	Successful       int64             `json:"successful"`
	Failed           int64             `json:"failed"`
	LastError        string            `json:"last_error,omitempty"`
	LastSuccess      time.Time         `json:"last_success,omitempty"`
	LastFailure      time.Time         `json:"last_failure,omitempty"`
	QuotaResetAt     time.Time         `json:"quota_reset_at,omitempty"`
	CircuitOpenUntil time.Time         `json:"circuit_open_until,omitempty"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
	ApprovalExpired  ApprovalStatus = "expired"
)

type ApprovalRequest struct {
	ID               string         `json:"id"`
	SessionID        string         `json:"session_id,omitempty"`
	Agent            string         `json:"agent"`
	Reason           string         `json:"reason"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd,omitempty"`
	EstimatedTokens  int64          `json:"estimated_tokens,omitempty"`
	Status           ApprovalStatus `json:"status"`
	RequestedBy      string         `json:"requested_by,omitempty"`
	DecidedBy        string         `json:"decided_by,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	DecidedAt        *time.Time     `json:"decided_at,omitempty"`
}

type ApprovalRequiredError struct {
	ApprovalID string
	Agent      string
	Reason     string
}

func (e *ApprovalRequiredError) Error() string {
	return fmt.Sprintf("human approval required for agent %q: %s (approval_id=%s)", e.Agent, e.Reason, e.ApprovalID)
}

func (e *QuotaExceededError) Error() string {
	if e.RetryAt.IsZero() {
		return fmt.Sprintf("agent %q quota exceeded: %s", e.Agent, e.Reason)
	}
	return fmt.Sprintf("agent %q quota exceeded until %s: %s", e.Agent, e.RetryAt.UTC().Format(time.RFC3339), e.Reason)
}

func (e *HarnessInvocationFailedError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("harness invocation failed for %q: %v (stderr: %s)", e.Agent, e.Err, e.Stderr)
	}
	return fmt.Sprintf("harness invocation failed for %q: %v", e.Agent, e.Err)
}

func (e *HarnessInvocationFailedError) Unwrap() error {
	return e.Err
}

type MalformedPeerResponseError struct {
	Agent  string
	Reason string
	Output string
}

func (e *MalformedPeerResponseError) Error() string {
	return fmt.Sprintf("malformed peer response from %q: %s", e.Agent, e.Reason)
}

type PolicyDeniedError struct {
	Agent  string
	Action string
	Reason string
}

func (e *PolicyDeniedError) Error() string {
	return fmt.Sprintf("policy denied action %q for agent %q: %s", e.Action, e.Agent, e.Reason)
}

type StalledError struct {
	Reason      string
	FindingHash string
	Rounds      int
}

func (e *StalledError) Error() string {
	return fmt.Sprintf("collaboration stalled: %s (rounds: %d)", e.Reason, e.Rounds)
}

type PeerCallLimitExceededError struct {
	Calls    int
	MaxCalls int
}

func (e *PeerCallLimitExceededError) Error() string {
	return fmt.Sprintf("peer call limit exceeded: made %d calls, maximum allowed is %d", e.Calls, e.MaxCalls)
}

type WorkspaceInvalidError struct {
	Path   string
	Reason string
}

func (e *WorkspaceInvalidError) Error() string {
	return fmt.Sprintf("invalid workspace %q: %s", e.Path, e.Reason)
}

type WriterConflictError struct {
	Reason  string
	Writers []string
}

func (e *WriterConflictError) Error() string {
	if len(e.Writers) > 0 {
		return fmt.Sprintf("single writer invariant violated: multiple writers configured (%s): %s", strings.Join(e.Writers, ", "), e.Reason)
	}
	return fmt.Sprintf("writer conflict: %s", e.Reason)
}

type HarnessAuthenticationRequiredError struct {
	Agent  string
	Reason string
}

func (e *HarnessAuthenticationRequiredError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("harness %q requires authentication: %s", e.Agent, e.Reason)
	}
	return fmt.Sprintf("harness %q requires authentication", e.Agent)
}

type ProtocolUnsupportedError struct {
	Protocol string
	Reason   string
}

func (e *ProtocolUnsupportedError) Error() string {
	return fmt.Sprintf("unsupported protocol %q: %s", e.Protocol, e.Reason)
}

type OperationDuplicateError struct {
	Key string
}

func (e *OperationDuplicateError) Error() string {
	return fmt.Sprintf("operation with idempotency key %q is duplicate", e.Key)
}

type OperationInProgressError struct {
	Key string
}

func (e *OperationInProgressError) Error() string {
	return fmt.Sprintf("operation with idempotency key %q is currently in progress", e.Key)
}

type SwitchyardUnavailableError struct {
	URL    string
	Reason string
}

func (e *SwitchyardUnavailableError) Error() string {
	return fmt.Sprintf("switchyard unavailable at %q: %s", e.URL, e.Reason)
}

type SwitchyardRouteUnavailableError struct {
	Route  string
	Reason string
}

func (e *SwitchyardRouteUnavailableError) Error() string {
	return fmt.Sprintf("switchyard route %q unavailable: %s", e.Route, e.Reason)
}

type ModelRoutingUnavailableError struct {
	Backend string
	Reason  string
}

func (e *ModelRoutingUnavailableError) Error() string {
	return fmt.Sprintf("model routing backend %q unavailable: %s", e.Backend, e.Reason)
}

type CollaborationCycleDetectedError struct {
	CycleLength  int
	Depth        int
	Participants []string
	Reason       string
}

func (e *CollaborationCycleDetectedError) Error() string {
	if len(e.Participants) > 0 {
		return fmt.Sprintf("collaboration cycle detected among participants (%s): %s", strings.Join(e.Participants, " -> "), e.Reason)
	}
	return fmt.Sprintf("collaboration cycle detected: %s", e.Reason)
}

type ParticipantPausedError struct {
	Participant string
	SpaceID     string
}

func (e *ParticipantPausedError) Error() string {
	return fmt.Sprintf("participant %q in space %q is paused", e.Participant, e.SpaceID)
}

type ChannelAccessDeniedError struct {
	Participant string
	Channel     string
	Reason      string
}

func (e *ChannelAccessDeniedError) Error() string {
	return fmt.Sprintf("channel access denied for participant %q to channel %q: %s", e.Participant, e.Channel, e.Reason)
}

type HumanInterruptedError struct {
	Action string // "pause", "stop"
	Reason string
}

func (e *HumanInterruptedError) Error() string {
	return fmt.Sprintf("collaboration interrupted by human operator (%s): %s", e.Action, e.Reason)
}

type SpaceNotFoundError struct {
	SpaceID string
}

func (e *SpaceNotFoundError) Error() string {
	return fmt.Sprintf("collaboration space %q not found", e.SpaceID)
}

type ChannelNotFoundError struct {
	Channel string
	SpaceID string
}

func (e *ChannelNotFoundError) Error() string {
	return fmt.Sprintf("channel %q in space %q not found", e.Channel, e.SpaceID)
}

type ThreadNotFoundError struct {
	ThreadID string
}

func (e *ThreadNotFoundError) Error() string {
	return fmt.Sprintf("thread %q not found", e.ThreadID)
}

type HardTokenLimitUnsupportedError struct {
	Peer   string `json:"peer"`
	Reason string `json:"reason"`
}

func (e *HardTokenLimitUnsupportedError) Error() string {
	return fmt.Sprintf("hard token limit unsupported for peer %q: %s", e.Peer, e.Reason)
}

// -------------------------------------------------------------------------
// Retry Classification
// -------------------------------------------------------------------------

type RetryCategory string

const (
	RetryCategoryTransientTransport RetryCategory = "transient_transport"
	RetryCategoryRateLimit          RetryCategory = "rate_limit"
	RetryCategoryQuota              RetryCategory = "quota"
	RetryCategoryTimeout            RetryCategory = "timeout"
	RetryCategoryPermanentAuth      RetryCategory = "permanent_auth"
	RetryCategoryInvalidRequest     RetryCategory = "invalid_request"
	RetryCategoryMalformedResponse  RetryCategory = "malformed_response"
	RetryCategoryNonRetryable       RetryCategory = "non_retryable"
)

func ClassifyError(err error) RetryCategory {
	if err == nil {
		return RetryCategoryNonRetryable
	}
	var (
		timeoutErr *PeerTimeoutError
		unavailErr *PeerUnavailableError
		policyErr  *PolicyDeniedError
		capErr     *CapabilityUnsupportedError
		depthErr   *PeerDepthExceededError
		budgetErr  *BudgetExceededError
		malformed  *MalformedPeerResponseError
		quota      *QuotaExceededError
	)
	if errors.As(err, &quota) {
		return RetryCategoryQuota
	}
	if errors.As(err, &timeoutErr) {
		return RetryCategoryTimeout
	}
	if errors.As(err, &unavailErr) {
		return RetryCategoryTransientTransport
	}
	if errors.As(err, &policyErr) || errors.As(err, &capErr) || errors.As(err, &depthErr) || errors.As(err, &budgetErr) {
		return RetryCategoryNonRetryable
	}
	if errors.As(err, &malformed) {
		return RetryCategoryMalformedResponse
	}

	msg := strings.ToLower(err.Error())
	if looksLikeQuotaLimit(msg) {
		return RetryCategoryQuota
	}
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many requests") {
		return RetryCategoryRateLimit
	}
	if strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "authentication failed") || strings.Contains(msg, "invalid api key") {
		return RetryCategoryPermanentAuth
	}
	if strings.Contains(msg, "502") || strings.Contains(msg, "503") || strings.Contains(msg, "504") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "reset by peer") {
		return RetryCategoryTransientTransport
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return RetryCategoryTimeout
	}
	return RetryCategoryNonRetryable
}

func IsTransient(err error) bool {
	cat := ClassifyError(err)
	return cat == RetryCategoryTransientTransport || cat == RetryCategoryRateLimit || cat == RetryCategoryQuota || cat == RetryCategoryTimeout
}

var (
	quotaMarkerPattern = regexp.MustCompile(`(?i)(usage[ _-]?limit|credit[ _-]?limit|credits? exhausted|quota(?:[ _-]?exceeded|[ _-]?limit)?|monthly limit|session limit|capacity limit|resource exhausted|resource_exhausted|rate_limit_error|insufficient_quota|too_many_requests)`)
	rfc3339Pattern     = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})\b`)
	dateTimePattern    = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?::\d{2})?\s*(?:UTC|GMT|[+-]\d{2}:?\d{2})?\b`)
	retryAfterPattern  = regexp.MustCompile(`(?i)(?:retry[- ]after|try again in|retry in|resumes? in)\s*[: ]\s*(\d+(?:\.\d+)?)\s*(seconds?|secs?|minutes?|mins?|hours?|hrs?|days?)`)
)

func looksLikeQuotaLimit(message string) bool {
	return quotaMarkerPattern.MatchString(message) &&
		(strings.Contains(message, "reset") || strings.Contains(message, "resume") || strings.Contains(message, "retry") || strings.Contains(message, "limit") || strings.Contains(message, "exhausted"))
}

// IsQuotaLimited reports whether an agent error means that work should wait
// for a provider-side credit, usage, session, or quota reset.
func IsQuotaLimited(err error) bool {
	return ClassifyError(err) == RetryCategoryQuota
}

// QuotaRetryAt extracts a provider reset time or retry-after duration.  It
// returns false when the provider only reports the limit without a timestamp.
func QuotaRetryAt(err error) (time.Time, bool) {
	if err == nil || !IsQuotaLimited(err) {
		return time.Time{}, false
	}
	var quota *QuotaExceededError
	if errors.As(err, &quota) && !quota.RetryAt.IsZero() {
		return quota.RetryAt, true
	}
	message := err.Error()
	for _, candidate := range []string{rfc3339Pattern.FindString(message), dateTimePattern.FindString(message)} {
		if candidate == "" {
			continue
		}
		layouts := []string{time.RFC3339, "2006-01-02 15:04 MST", "2006-01-02 15:04:05 MST", "2006-01-02 15:04 -0700", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04", "2006-01-02 15:04:05"}
		for _, layout := range layouts {
			if parsed, parseErr := time.Parse(layout, candidate); parseErr == nil && parsed.After(time.Now()) {
				return parsed, true
			}
		}
	}
	if match := retryAfterPattern.FindStringSubmatch(message); len(match) == 3 {
		value, parseErr := strconv.ParseFloat(match[1], 64)
		if parseErr == nil {
			multiplier := time.Second
			switch strings.ToLower(match[2][:1]) {
			case "m":
				multiplier = time.Minute
			case "h":
				multiplier = time.Hour
			case "d":
				multiplier = 24 * time.Hour
			}
			return time.Now().Add(time.Duration(value * float64(multiplier))), true
		}
	}
	return time.Time{}, false
}

func IsAuthPermanent(err error) bool {
	return ClassifyError(err) == RetryCategoryPermanentAuth
}
