package governance

import "sort"

type RoutingCandidate struct {
	Agent          string  `json:"agent"`
	Model          string  `json:"model,omitempty"`
	TrustScore     float64 `json:"trust_score"`
	CostPerSuccess float64 `json:"cost_per_success"`
	LatencyMS      int64   `json:"latency_ms"`
	Available      bool    `json:"available"`
}

type RoutingRecommendation struct {
	Selected       string             `json:"selected,omitempty"`
	Reason         string             `json:"reason"`
	AutomaticApply bool               `json:"automatic_apply"`
	Candidates     []RoutingCandidate `json:"candidates"`
}

// Recommend ranks candidates deterministically. It is deliberately a
// recommendation, never an implicit production mutation.
func Recommend(candidates []RoutingCandidate) RoutingRecommendation {
	available := make([]RoutingCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Available {
			available = append(available, candidate)
		}
	}
	sort.SliceStable(available, func(i, j int) bool {
		left := available[i].TrustScore - available[i].CostPerSuccess*0.1 - float64(available[i].LatencyMS)/100000
		right := available[j].TrustScore - available[j].CostPerSuccess*0.1 - float64(available[j].LatencyMS)/100000
		if left == right {
			return available[i].Agent < available[j].Agent
		}
		return left > right
	})
	recommendation := RoutingRecommendation{AutomaticApply: false, Candidates: available}
	if len(available) == 0 {
		recommendation.Reason = "No available candidate satisfies the current policy"
		return recommendation
	}
	recommendation.Selected = available[0].Agent
	recommendation.Reason = "Selected the highest transparent trust/cost/latency score; automatic changes remain disabled"
	return recommendation
}
