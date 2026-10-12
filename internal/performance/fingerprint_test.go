package performance

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFingerprint_SameSemanticsSameFingerprint(t *testing.T) {
	a := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", map[string]string{"minObs": "10"})
	b := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", map[string]string{"minObs": "10"})
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("expected identical semantic inputs to produce the same fingerprint")
	}
}

func TestFingerprint_DeterministicAcrossMapOrdering(t *testing.T) {
	config1 := map[string]string{"a": "1", "b": "2", "c": "3"}
	config2 := map[string]string{"c": "3", "a": "1", "b": "2"} // different insertion order
	d1 := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", config1)
	d2 := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", config2)
	if d1.Fingerprint != d2.Fingerprint {
		t.Fatalf("expected map insertion order to never affect the fingerprint, got %q vs %q", d1.Fingerprint, d2.Fingerprint)
	}
}

func TestFingerprint_ConfigSemanticChangeProducesDifferentFingerprint(t *testing.T) {
	a := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", map[string]string{"minObs": "10"})
	b := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", map[string]string{"minObs": "20"})
	if a.Fingerprint == b.Fingerprint {
		t.Fatalf("expected a config semantic change to produce a different fingerprint")
	}
}

func TestFingerprint_SemanticsVersionChangeProducesDifferentFingerprint(t *testing.T) {
	a := NewRoutingPolicyDescriptor("legacy-deterministic", "v1", "semantics-1", nil)
	b := NewRoutingPolicyDescriptor("legacy-deterministic", "v1", "semantics-2", nil)
	if a.Fingerprint == b.Fingerprint {
		t.Fatalf("expected a SemanticsVersion change to produce a different fingerprint even with the same ID/Version")
	}
}

// TestNegative_FingerprintNeverIncludesTimestamp is structural: the
// factory function's signature has no time.Time parameter at all, so a
// timestamp cannot participate in the fingerprint even accidentally.
func TestNegative_FingerprintNeverIncludesTimestamp(t *testing.T) {
	fn := reflect.TypeOf(NewRoutingPolicyDescriptor)
	for i := 0; i < fn.NumIn(); i++ {
		if strings.Contains(strings.ToLower(fn.In(i).String()), "time") {
			t.Fatalf("NewRoutingPolicyDescriptor must never accept a time-typed parameter, got %s at position %d", fn.In(i), i)
		}
	}
}

func TestFingerprint_StableAcrossRepeatedConstruction(t *testing.T) {
	// Simulates "process restart" - a fresh construction with identical
	// inputs, no shared state, must reproduce the same fingerprint.
	config := map[string]string{"route": "strong-reasoning", "minObs": "10"}
	first := NewRoutingPolicyDescriptor("performance", "v2", "semantics-3", config)
	second := NewRoutingPolicyDescriptor("performance", "v2", "semantics-3", map[string]string{"minObs": "10", "route": "strong-reasoning"})
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("expected a fresh construction with identical semantic inputs to reproduce the same fingerprint")
	}
}

// TestNegative_NoSecretMaterialInDescriptor is a structural proof:
// RoutingPolicyDescriptor has no field named anything resembling a
// secret/credential, and the factory's own config parameter is
// documented as non-secret-only - this test confirms the TYPE itself
// has no place to smuggle one in beyond the caller-controlled config
// map, which is the caller's own responsibility per the factory's
// doc comment.
func TestNegative_NoSecretMaterialInDescriptor(t *testing.T) {
	typ := reflect.TypeOf(RoutingPolicyDescriptor{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "secret") || strings.Contains(name, "credential") || strings.Contains(name, "password") || strings.Contains(name, "apikey") {
			t.Fatalf("RoutingPolicyDescriptor.%s suggests a place for secret material - forbidden", typ.Field(i).Name)
		}
	}
}

// TestNegative_ArbitraryCallerFingerprintCannotOverrideComputed proves
// the trusted factory ALWAYS computes its own fingerprint - there is no
// parameter through which a caller could inject an arbitrary
// "Fingerprint: anything" value into a descriptor built this way.
func TestNegative_ArbitraryCallerFingerprintCannotOverrideComputed(t *testing.T) {
	fn := reflect.TypeOf(NewRoutingPolicyDescriptor)
	// id, version, semanticsVersion, config - exactly 4 parameters, none
	// of which is a pre-computed fingerprint string the caller could
	// substitute.
	if fn.NumIn() != 4 {
		t.Fatalf("expected exactly 4 parameters (id, version, semanticsVersion, config), got %d - an extra parameter could be a smuggled fingerprint override", fn.NumIn())
	}
	d := NewRoutingPolicyDescriptor("x", "v1", "s1", nil)
	if d.FingerprintScheme != FingerprintSchemeCanonicalV2 {
		t.Fatalf("expected the factory to always tag its output with the canonical scheme, got %q", d.FingerprintScheme)
	}
}

func TestNegative_SameIDVersionDifferentFingerprintIsConflict(t *testing.T) {
	store := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(store, DefaultEvaluationLimits(), nil)
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())

	configA := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", map[string]string{"minObs": "10"})
	configB := RoutingPolicyDescriptor{ID: "performance", Version: "v1", Fingerprint: "deliberately-different-fingerprint"}

	challengerA := StaticRoutingPolicy{Descriptor_: configA, AgentID: "agent-A"}
	if _, err := evaluator.Evaluate(context.Background(), snap, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challengerA}); err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}

	challengerB := StaticRoutingPolicy{Descriptor_: configB, AgentID: "agent-A"}
	snap2 := testSnapshot(t, "tenant-a", "T2", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	_, err := evaluator.Evaluate(context.Background(), snap2, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challengerB})
	if err == nil {
		t.Fatalf("expected a PolicyIdentityConflict for the same ID:Version with a different fingerprint")
	}
	if _, ok := err.(*PolicyIdentityConflict); !ok {
		t.Fatalf("expected *PolicyIdentityConflict, got %T: %v", err, err)
	}
}
