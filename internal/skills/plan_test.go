package skills

import "testing"

func TestBuildExecutionPlanUsesDeclaredDeterministicSteps(t *testing.T) {
	plan, err := BuildExecutionPlan(Entry{Manifest: Manifest{Name: "github", Version: "1.2.0", Tools: []string{"issues.list", "repo.get"}}, Content: "Phase 1 [fn: repo.get]\nPhase 2 [fn: issues.list]"}, 800)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 2 || plan.Steps[0].Tool != "repo.get" || plan.Steps[1].Tool != "issues.list" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.EstimatedTokenSavings != 640 || plan.EstimatedSavingsPercent != 80 {
		t.Fatalf("unexpected token estimate: %+v", plan)
	}
	if plan.AllowsTool("repo.delete") {
		t.Fatal("undeclared tool must not be allowed by plan")
	}
}

func TestBuildExecutionPlanRejectsToollessSkill(t *testing.T) {
	if _, err := BuildExecutionPlan(Entry{Manifest: Manifest{Name: "empty"}}, 100); err == nil {
		t.Fatal("expected tool-less skill to be rejected")
	}
}
