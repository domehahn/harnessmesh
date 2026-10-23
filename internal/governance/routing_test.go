package governance

import "testing"

func TestRecommendIsDeterministicAndDoesNotAutoApply(t *testing.T) {
	result := Recommend([]RoutingCandidate{
		{Agent: "slow-premium", TrustScore: .95, CostPerSuccess: 2, LatencyMS: 900, Available: true},
		{Agent: "efficient", TrustScore: .88, CostPerSuccess: .1, LatencyMS: 100, Available: true},
		{Agent: "offline", TrustScore: 1, Available: false},
	})
	if result.Selected != "efficient" || result.AutomaticApply || len(result.Candidates) != 2 {
		t.Fatalf("unexpected recommendation: %+v", result)
	}
}

func TestRecommendEmpty(t *testing.T) {
	result := Recommend([]RoutingCandidate{{Agent: "offline", Available: false}})
	if result.Selected != "" || result.Reason == "" {
		t.Fatalf("unexpected empty recommendation: %+v", result)
	}
}
