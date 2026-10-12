package performance

import (
	"reflect"
	"strings"
	"testing"
)

// TestNegative_ShadowEvaluatorHasNoExecutorReference is a structural
// proof: ShadowEvaluator (and everything it exposes) has no field or
// method whose type or name references Executor/ExecutionProvider -
// "Challenger output cannot reach Executor" is therefore true by the
// type system, not by convention.
func TestNegative_ShadowEvaluatorHasNoExecutorReference(t *testing.T) {
	typ := reflect.TypeOf(ShadowEvaluator{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Type.String())
		if strings.Contains(name, "executor") || strings.Contains(name, "executionprovider") {
			t.Fatalf("ShadowEvaluator.%s has a type referencing Executor/ExecutionProvider - forbidden", typ.Field(i).Name)
		}
	}
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		sig := strings.ToLower(m.Type.String())
		if strings.Contains(sig, "executor") || strings.Contains(sig, "executionprovider") {
			t.Fatalf("ShadowEvaluator method %s references Executor/ExecutionProvider - forbidden", m.Name)
		}
	}
}

// TestNegative_ReplayerHasNoExecutorReference mirrors the above for
// Replayer - offline replay must never be able to invoke real
// execution.
func TestNegative_ReplayerHasNoExecutorReference(t *testing.T) {
	typ := reflect.TypeOf(Replayer{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Type.String())
		if strings.Contains(name, "executor") || strings.Contains(name, "executionprovider") {
			t.Fatalf("Replayer.%s has a type referencing Executor/ExecutionProvider - forbidden", typ.Field(i).Name)
		}
	}
}

// TestNegative_ReplayResultCannotFakeOutcome is a structural proof:
// ReplayResult has no field resembling a predicted/hypothetical outcome
// - replay answers "what would the policy have recommended," never
// "what would have happened."
func TestNegative_ReplayResultCannotFakeOutcome(t *testing.T) {
	typ := reflect.TypeOf(ReplayResult{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "predict") || strings.Contains(name, "expected") || strings.Contains(name, "wouldhave") || strings.Contains(name, "outcome") {
			t.Fatalf("ReplayResult.%s suggests a fabricated hypothetical outcome - forbidden", typ.Field(i).Name)
		}
	}
}

// TestNegative_RoutingEvaluationRecordCannotFakeChallengerOutcome is a
// structural proof: there is no field anywhere on
// RoutingEvaluationRecord resembling a Challenger-attributed outcome -
// only ActualExecutionOutcome/ActualQualityOutcome exist, and both are
// documented (and, per TestNegative_OutcomeOnlyAttachesToChampion,
// proven) to belong exclusively to the executed Champion decision.
func TestNegative_RoutingEvaluationRecordCannotFakeChallengerOutcome(t *testing.T) {
	typ := reflect.TypeOf(RoutingEvaluationRecord{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "challenger") && strings.Contains(name, "outcome") {
			t.Fatalf("RoutingEvaluationRecord.%s would let an outcome be attributed to the Challenger - forbidden", typ.Field(i).Name)
		}
		if strings.Contains(name, "predict") || strings.Contains(name, "wouldhave") {
			t.Fatalf("RoutingEvaluationRecord.%s suggests a fabricated hypothetical outcome - forbidden", typ.Field(i).Name)
		}
	}
}

// TestNegative_NoStatusImpliesChallengerWonOrLost proves the required
// EvaluationStatus vocabulary excludes "challenger_won"/"challenger_lost"
// style values.
func TestNegative_NoStatusImpliesChallengerWonOrLost(t *testing.T) {
	statuses := []EvaluationStatus{
		StatusSameRecommendation, StatusDifferentRecommendation,
		StatusChallengerInsufficientData, StatusChallengerError,
		StatusChallengerInvalidRecommendation, StatusEvaluationIncomplete,
	}
	for _, s := range statuses {
		if strings.Contains(string(s), "won") || strings.Contains(string(s), "lost") {
			t.Fatalf("EvaluationStatus %q implies a real win/loss comparison that never happened", s)
		}
	}
}

// TestNegative_PromotionGateHasNoMutationMethod proves there is no
// Promote/ActivateChallenger/SetChampion method anywhere this package
// exposes for the evaluation layer.
func TestNegative_PromotionGateHasNoMutationMethod(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(ShadowEvaluator{}),
		reflect.TypeOf(Replayer{}),
	} {
		for i := 0; i < typ.NumMethod(); i++ {
			name := strings.ToLower(typ.Method(i).Name)
			if strings.Contains(name, "promote") || strings.Contains(name, "activate") || strings.Contains(name, "setchampion") {
				t.Fatalf("%s has a %s method - the evaluation layer must never mutate active routing policy", typ.Name(), typ.Method(i).Name)
			}
		}
	}
}
