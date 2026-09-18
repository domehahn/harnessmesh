package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type fakeAgent struct {
	name    string
	results []protocol.AgentResult
	calls   int
}

func (f *fakeAgent) Name() string { return f.name }

func (f *fakeAgent) Run(_ context.Context, req agent.Request) (protocol.AgentResult, error) {
	if f.calls >= len(f.results) {
		return protocol.AgentResult{}, nil
	}
	out := f.results[f.calls]
	f.calls++
	if out.SessionID == "" {
		out.SessionID = f.name + "-session"
	}
	return out, nil
}

type fakeProjector struct{}

func (fakeProjector) Capture(context.Context, []string) (protocol.ContextSnapshot, error) {
	return protocol.ContextSnapshot{
		RepoRoot: "/tmp/repo",
		Head:     "abc",
		Branch:   "main",
		Diff:     "diff --git a/a.go b/a.go",
	}, nil
}

func reviewJSON(v protocol.ReviewVerdict, findings []protocol.Finding) string {
	raw, _ := json.Marshal(protocol.ReviewResult{
		Verdict:   v,
		Summary:   string(v),
		Findings:  findings,
		Questions: []string{},
	})
	return string(raw)
}

func TestRunnerFeedbackThenApprove(t *testing.T) {
	executor := &fakeAgent{name: "executor", results: []protocol.AgentResult{
		{AgentName: "executor", Text: "initial implementation"},
		{AgentName: "executor", Text: "fixed implementation"},
	}}
	reviewer := &fakeAgent{name: "reviewer", results: []protocol.AgentResult{
		{
			AgentName: "reviewer",
			Text: reviewJSON(protocol.VerdictChangesRequired, []protocol.Finding{{
				ID:             "HM-1",
				Severity:       "high",
				Claim:          "bug",
				Evidence:       "a.go:1",
				Recommendation: "fix",
			}}),
		},
		{
			AgentName: "reviewer",
			Text:      reviewJSON(protocol.VerdictApprove, nil),
		},
	}}

	cfg := &config.Config{
		Workflow: config.WorkflowConfig{
			Executor:     "executor",
			Reviewer:     "reviewer",
			MaxRounds:    3,
			StopOnRepeat: true,
		},
	}
	runner := Runner{
		Config:    cfg,
		Repo:      "/tmp/repo",
		Executor:  executor,
		Reviewer:  reviewer,
		Projector: fakeProjector{},
	}

	result, err := runner.Run(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "approved" {
		t.Fatalf("status=%s", result.Status)
	}
	if len(result.Rounds) != 2 {
		t.Fatalf("rounds=%d", len(result.Rounds))
	}
	if executor.calls != 2 || reviewer.calls != 2 {
		t.Fatalf("calls executor=%d reviewer=%d", executor.calls, reviewer.calls)
	}
}
