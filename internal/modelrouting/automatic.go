package modelrouting

import (
	"errors"
	"sort"
	"sync"
)

type Outcome struct {
	Route     string
	Success   bool
	Cost      float64
	LatencyMS int64
}
type AutoPolicy struct {
	MinSamples     int
	MinSuccessRate float64
	MaxCost        float64
	Enabled        bool
}
type Candidate struct {
	Route       string
	Samples     int
	SuccessRate float64
	AverageCost float64
	Score       float64
}
type AutoRouter struct {
	mu       sync.RWMutex
	policy   AutoPolicy
	outcomes map[string][]Outcome
	active   string
}

func (r *AutoRouter) Snapshot() []Outcome {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Outcome
	for _, outcomes := range r.outcomes {
		out = append(out, outcomes...)
	}
	return out
}

func (r *AutoRouter) Load(outcomes []Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, outcome := range outcomes {
		if outcome.Route != "" {
			r.outcomes[outcome.Route] = append(r.outcomes[outcome.Route], outcome)
		}
	}
}

func NewAutoRouter(policy AutoPolicy) *AutoRouter {
	if policy.MinSamples <= 0 {
		policy.MinSamples = 5
	}
	if policy.MinSuccessRate <= 0 {
		policy.MinSuccessRate = .8
	}
	return &AutoRouter{policy: policy, outcomes: map[string][]Outcome{}}
}
func (r *AutoRouter) Observe(outcome Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes[outcome.Route] = append(r.outcomes[outcome.Route], outcome)
}
func (r *AutoRouter) Recommend() (Candidate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.policy.Enabled {
		return Candidate{}, errors.New("automatic routing is disabled")
	}
	candidates := []Candidate{}
	for route, outcomes := range r.outcomes {
		if len(outcomes) < r.policy.MinSamples {
			continue
		}
		successes, cost := 0, 0.0
		for _, item := range outcomes {
			if item.Success {
				successes++
			}
			cost += item.Cost
		}
		successRate := float64(successes) / float64(len(outcomes))
		averageCost := cost / float64(len(outcomes))
		if successRate < r.policy.MinSuccessRate || (r.policy.MaxCost > 0 && averageCost > r.policy.MaxCost) {
			continue
		}
		candidates = append(candidates, Candidate{Route: route, Samples: len(outcomes), SuccessRate: successRate, AverageCost: averageCost, Score: successRate / (1 + averageCost)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Route < candidates[j].Route
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) == 0 {
		return Candidate{}, errors.New("no route satisfies automatic policy")
	}
	return candidates[0], nil
}
func (r *AutoRouter) Apply() (Candidate, error) {
	candidate, err := r.Recommend()
	if err != nil {
		return Candidate{}, err
	}
	r.mu.Lock()
	r.active = candidate.Route
	r.mu.Unlock()
	return candidate, nil
}
func (r *AutoRouter) Active() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.active }
