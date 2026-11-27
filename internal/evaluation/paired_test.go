package evaluation

import "testing"

func TestComparePaired(t *testing.T) {
	elapsed := int64(120)
	zero := int64(0)
	base := Run{Tenant: "repo", TaskID: "check", PolicyOK: true, CostMicrousd: &zero}
	candidate := base
	candidate.Success = true
	candidate.WallTimeMS = &elapsed
	result, err := ComparePaired([]Run{base}, []Run{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateOnly != 1 || result.Candidate.SuccessRate != 1 ||
		result.BaselineMetrics.MeanWallTimeMS != nil ||
		result.CandidateMetrics.MeanWallTimeMS == nil || *result.CandidateMetrics.MeanWallTimeMS != 120 ||
		result.BaselineMetrics.MeanCostMicrousd == nil || *result.BaselineMetrics.MeanCostMicrousd != 0 {
		t.Fatalf("unexpected paired result: %+v", result)
	}

	candidate.Tenant = "other"
	if _, err := ComparePaired([]Run{base}, []Run{candidate}); err == nil {
		t.Fatal("comparison accepted a task from a different tenant")
	}
	if _, err := ComparePaired([]Run{base, base}, []Run{base, base}); err == nil {
		t.Fatal("comparison accepted duplicate tasks")
	}
}
