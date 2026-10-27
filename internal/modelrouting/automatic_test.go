package modelrouting

import "testing"

func TestAutomaticRouterPromotesBestEligibleRoute(t *testing.T) {
	r := NewAutoRouter(AutoPolicy{Enabled: true, MinSamples: 2, MinSuccessRate: .5})
	r.Observe(Outcome{Route: "safe", Success: true, Cost: .1})
	r.Observe(Outcome{Route: "safe", Success: true, Cost: .1})
	r.Observe(Outcome{Route: "bad", Success: false, Cost: .01})
	r.Observe(Outcome{Route: "bad", Success: false, Cost: .01})
	candidate, err := r.Apply()
	if err != nil || candidate.Route != "safe" || r.Active() != "safe" {
		t.Fatalf("unexpected promotion: %#v %v", candidate, err)
	}
}
