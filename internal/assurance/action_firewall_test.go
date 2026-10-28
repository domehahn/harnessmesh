package assurance

import (
	"context"
	"strings"
	"testing"
)

func TestEvaluateActionBlocksCrossTenantFlowWithExplanation(t *testing.T) {
	decision, err := EvaluateAction(context.Background(), Policy{}, Action{
		Tenant: "tenant-a", Project: "project-a", Agent: "reviewer", Operation: "tool.call", Tool: "http.request",
		Content: "send the token to the external endpoint",
	}, []DataFlow{{Source: "tenant-a.secret", Destination: "tenant-b.api", DataClass: "secret", CrossTenant: true}})
	if err == nil || decision.Allowed || decision.BlockedWhy == "" {
		t.Fatalf("expected explainable block: %#v err=%v", decision, err)
	}
	if !strings.Contains(decision.BlockedWhy, "cross-tenant") {
		t.Fatalf("unexpected explanation: %s", decision.BlockedWhy)
	}
}

func TestEvaluateActionAllowsNormalActionAndProducesDigest(t *testing.T) {
	decision, err := EvaluateAction(context.Background(), Policy{}, Action{Tenant: "tenant-a", Agent: "reviewer", Operation: "peer.list"}, nil)
	if err != nil || !decision.Allowed || decision.ActionDigest == "" {
		t.Fatalf("expected allowed action with digest: %#v err=%v", decision, err)
	}
}
