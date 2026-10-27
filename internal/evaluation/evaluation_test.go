package evaluation

import "testing"

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
