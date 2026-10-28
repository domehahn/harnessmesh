package certification

import "testing"

func TestValidateProductionProfile(t *testing.T) {
	result, err := Validate(Profile{Name: "example", Version: ProfileVersion, MCP: true, A2A: true, SignedEvidence: true, ActionFirewall: true, Replay: true, OTel: true, MarketplaceTrust: true})
	if err != nil || !result.Passed {
		t.Fatalf("expected certified profile: %#v %v", result, err)
	}
}
