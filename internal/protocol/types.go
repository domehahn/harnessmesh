package protocol

import "time"

type ReviewVerdict string

const (
	VerdictApprove         ReviewVerdict = "approve"
	VerdictChangesRequired ReviewVerdict = "changes_required"
	VerdictBlock           ReviewVerdict = "block"
)

type Finding struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"`
	File           string `json:"file,omitempty"`
	Line           int    `json:"line,omitempty"`
	Claim          string `json:"claim"`
	Evidence       string `json:"evidence"`
	Recommendation string `json:"recommendation"`
}

type ReviewResult struct {
	Verdict   ReviewVerdict `json:"verdict"`
	Summary   string        `json:"summary"`
	Findings  []Finding     `json:"findings"`
	Questions []string      `json:"questions,omitempty"`
}

type PeerEnvelope struct {
	SchemaVersion int             `json:"schema_version"`
	SessionID     string          `json:"session_id"`
	MessageID     string          `json:"message_id"`
	From          string          `json:"from"`
	To            string          `json:"to"`
	Type          string          `json:"type"`
	CreatedAt     time.Time       `json:"created_at"`
	Task          string          `json:"task"`
	Round         int             `json:"round"`
	Context       ContextSnapshot `json:"context"`
	Review        *ReviewResult   `json:"review,omitempty"`
}

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
