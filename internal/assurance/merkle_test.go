package assurance

import (
	"testing"
	"time"
)

func TestMerkleRootAndEvidenceFinalization(t *testing.T) {
	evidence, err := FinalizeEvidence(Evidence{SessionID: "s1", Tenant: "t1", Agent: "a1", Operation: "run", Events: []Observation{{ID: "1", Operation: "plan", At: time.Unix(1, 0)}, {ID: "2", Operation: "tool", At: time.Unix(2, 0)}}})
	if err != nil || evidence.ChainRoot == "" || evidence.IntentDigest == "" {
		t.Fatalf("expected finalized evidence: %#v %v", evidence, err)
	}
	changed, err := FinalizeEvidence(Evidence{SessionID: "s1", Tenant: "t1", Agent: "a1", Operation: "run", Events: []Observation{{ID: "2", Operation: "tool", At: time.Unix(2, 0)}, {ID: "1", Operation: "plan", At: time.Unix(1, 0)}}})
	if err != nil || changed.ChainRoot == evidence.ChainRoot {
		t.Fatal("event order must change the Merkle root")
	}
}
