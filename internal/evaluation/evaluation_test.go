package evaluation

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestScoreRunIsDeterministicAndFailClosed(t *testing.T) {
	score, err := ScoreRun(Run{Tenant: "t1", TaskID: "job-1", Expected: "ok", Output: "ok", Success: true, PolicyOK: true, HumanApproved: true, Steps: []Step{{Name: "tool", Kind: "tool", Status: "completed", PolicyOK: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if score.Overall != 1 {
		t.Fatalf("expected perfect score, got %#v", score)
	}
	unsafe, err := ScoreRun(Run{Tenant: "t1", TaskID: "job-2", Success: true, PolicyOK: false})
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.Safety != 0 || unsafe.Overall >= score.Overall {
		t.Fatalf("unsafe run was not penalized: %#v", unsafe)
	}
}

func TestAggregateAndRank(t *testing.T) {
	runs := []Run{{Tenant: "t", TaskID: "a", Success: true, PolicyOK: true, HumanApproved: true}, {Tenant: "t", TaskID: "b", Success: false, PolicyOK: true}}
	agg, err := AggregateRuns(runs)
	if err != nil {
		t.Fatal(err)
	}
	if agg.Runs != 2 || agg.SuccessRate != .5 {
		t.Fatalf("unexpected aggregate: %#v", agg)
	}
	if got := Rank(map[string]Score{"slow": {Overall: .2}, "good": {Overall: .9}}); got[0] != "good" {
		t.Fatalf("unexpected rank: %v", got)
	}
}

func TestDeepEvalExportRedactsSecretsAndPreservesTools(t *testing.T) {
	run := Run{
		Tenant:           "tenant-a",
		SessionID:        "sess-1",
		TaskID:           "task-1",
		Input:            "inspect token sk-secret12345678901234567890",
		Expected:         "clean",
		Output:           "clean",
		RetrievalContext: []string{`doc with password="supersecretvalue"`},
		Success:          true,
		PolicyOK:         true,
		HumanApproved:    true,
		Steps: []Step{
			{
				Name:       "repository.search",
				Kind:       "tool",
				Status:     "completed",
				ToolInput:  "query=auth",
				ToolOutput: "found 2 files",
				Context:    "step context",
				PolicyOK:   true,
			},
		},
	}

	var rawRuns bytes.Buffer
	if err := json.NewEncoder(&rawRuns).Encode(run); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRunsJSONL(&rawRuns)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadRunsJSONL failed: %v (%d)", err, len(loaded))
	}

	var exported bytes.Buffer
	if err := ExportDeepEvalJSONL(&exported, loaded); err != nil {
		t.Fatal(err)
	}
	out := exported.String()
	if strings.Contains(out, "sk-secret12345678901234567890") || strings.Contains(out, "supersecretvalue") {
		t.Fatalf("expected secrets to be redacted in DeepEval export: %s", out)
	}
	var tc DeepEvalTestCase
	if err := json.Unmarshal(exported.Bytes(), &tc); err != nil {
		t.Fatal(err)
	}
	if len(tc.ToolsCalled) != 1 || tc.ToolsCalled[0].Name != "repository.search" {
		t.Fatalf("unexpected tools_called: %#v", tc.ToolsCalled)
	}
	if tc.AdditionalMetadata["tenant"] != "tenant-a" {
		t.Fatalf("missing tenant metadata: %#v", tc.AdditionalMetadata)
	}
}

func TestFilterGoldenRunsAndCompareHarnesses(t *testing.T) {
	baseline := []Run{
		{Tenant: "t1", SessionID: "s1", TaskID: "task-1", Input: "fix auth", Output: "partial", Success: false, PolicyOK: true, HumanApproved: true},
		{Tenant: "t1", SessionID: "s2", TaskID: "task-2", Input: "fix db", Output: "ok", Success: true, PolicyOK: true, HumanApproved: true},
	}
	candidate := []Run{
		{Tenant: "t1", SessionID: "s3", TaskID: "task-1", Input: "fix auth sk-secret12345678901234567890", Output: "ok", Success: true, PolicyOK: true, HumanApproved: true},
		{Tenant: "t1", SessionID: "s4", TaskID: "task-2", Input: "fix db", Output: "ok", Success: true, PolicyOK: true, HumanApproved: true},
	}

	comp, err := CompareHarnesses(baseline, candidate, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	if comp.Recommendation != "promote" || comp.ScoreDelta <= 0 {
		t.Fatalf("expected promote recommendation with positive delta, got %#v", comp)
	}

	// Unsafe candidate must fail closed with rollback
	unsafeCandidate := []Run{
		{Tenant: "t1", SessionID: "s5", TaskID: "task-1", Output: "ok", Success: true, PolicyOK: false, HumanApproved: true},
	}
	unsafeComp, err := CompareHarnesses(baseline, unsafeCandidate, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	if unsafeComp.Recommendation != "rollback" {
		t.Fatalf("expected rollback for unsafe candidate, got %#v", unsafeComp)
	}

	var goldenBuf bytes.Buffer
	if err := ExportGoldenJSONL(&goldenBuf, append(baseline, candidate...), 0.9); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(goldenBuf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 golden traces, got %d: %s", len(lines), goldenBuf.String())
	}
	if strings.Contains(goldenBuf.String(), "sk-secret12345678901234567890") {
		t.Fatalf("expected golden trace export to redact secrets: %s", goldenBuf.String())
	}
}
