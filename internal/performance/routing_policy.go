package performance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// RoutingPolicyID identifies a routing policy's IDENTITY (the "kind" of
// policy - e.g. "legacy", "performance", "pair-aware"), distinct from
// Version. Two policies with the same ID but different Version are
// related but not identical - see RoutingPolicyDescriptor.
type RoutingPolicyID string

// RoutingPolicyDescriptor is an immutable, versioned policy identity.
// ADR 0008's explicit requirement: "performance-v1" and "performance-v2"
// must never silently share one identity - a behavior change always
// produces a NEW Version, never a silent redefinition of an existing
// one. Fingerprint is an OPTIONAL, caller-supplied collision-detection
// aid (e.g. a hash of the policy's own config) - this package computes
// no fingerprint automatically (there is no existing deterministic
// config-hashing utility anywhere in this repository to reuse for
// policy configs specifically), so a caller that cares about detecting
// a same-ID-same-Version-different-semantics collision must supply one
// itself. Documented here as a deliberate scope limit, not an oversight.
type RoutingPolicyDescriptor struct {
	ID      RoutingPolicyID
	Version string
	// SemanticsVersion distinguishes a routing-BEHAVIOR change from a
	// mere label change - ADR 0009's requirement that static,
	// code-only policies (e.g. "legacy-deterministic") still carry an
	// explicit semantics identity. Optional; participates in the
	// computed fingerprint when set via NewRoutingPolicyDescriptor.
	SemanticsVersion string
	// Fingerprint is either caller-supplied (legacy/manual construction
	// - FingerprintScheme left empty, "manual-v1" implied) or computed
	// deterministically by NewRoutingPolicyDescriptor
	// (FingerprintScheme == FingerprintSchemeCanonicalV2). Historical
	// Phase 9 records with manually-supplied fingerprints are NEVER
	// rewritten - see FingerprintScheme's doc comment.
	Fingerprint       string
	FingerprintScheme string
}

// FingerprintScheme values. Historical records are never migrated
// between schemes - a record's FingerprintScheme is itself a fact about
// how its Fingerprint was produced, kept forever as written.
const (
	FingerprintSchemeManualV1    = "manual-v1"
	FingerprintSchemeCanonicalV2 = "canonical-v2"
)

// String renders a stable "id:version" form used as part of
// RoutingEvaluationRecord's idempotency key.
func (d RoutingPolicyDescriptor) String() string {
	return fmt.Sprintf("%s:%s", d.ID, d.Version)
}

// NewRoutingPolicyDescriptor is the TRUSTED factory that computes a
// Fingerprint deterministically from routing-semantic inputs - the
// preferred construction path for any policy driving a PRODUCTION
// evaluation record (ADR 0009's "avoid a public workflow where
// arbitrary callers can freely inject Fingerprint: 'anything'"
// instruction). config is a flat map of routing-relevant, NON-SECRET
// configuration values only - callers must never pass a secret value
// into it (see the doc comment on computeCanonicalFingerprint); if a
// secret affects routing semantics, the caller should derive and pass a
// stable, non-secret identifier for that secret's EFFECT instead (e.g.
// a config-version label), never the secret itself.
func NewRoutingPolicyDescriptor(id RoutingPolicyID, version, semanticsVersion string, config map[string]string) RoutingPolicyDescriptor {
	return RoutingPolicyDescriptor{
		ID: id, Version: version, SemanticsVersion: semanticsVersion,
		Fingerprint:       computeCanonicalFingerprint(id, version, semanticsVersion, config),
		FingerprintScheme: FingerprintSchemeCanonicalV2,
	}
}

// computeCanonicalFingerprint derives a deterministic identity from
// (id, version, semanticsVersion, config) - sorted config keys, so map
// iteration order never affects the result; no timestamp, no runtime
// pointer, no random value participates. Two calls with the same
// semantic inputs ALWAYS produce the same fingerprint, regardless of
// process restart or map insertion order; any change to id, version,
// semanticsVersion, or any config key/value produces a different one.
//
// config must contain only non-secret, routing-relevant values - this
// function has no way to detect a secret accidentally passed in, so the
// CALLER is responsible for that boundary (see
// NewRoutingPolicyDescriptor's doc comment). This mirrors every other
// "never log/persist secrets" boundary already established throughout
// this codebase (e.g. internal/performance's own PerformanceObservation
// never carries prompt/secret content) - the discipline is enforced by
// what callers are told to pass in, not by content inspection here.
func computeCanonicalFingerprint(id RoutingPolicyID, version, semanticsVersion string, config map[string]string) string {
	h := sha256.New()
	h.Write([]byte(id))
	h.Write([]byte{0})
	h.Write([]byte(version))
	h.Write([]byte{0})
	h.Write([]byte(semanticsVersion))
	h.Write([]byte{0})
	keys := make([]string, 0, len(config))
	for k := range config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(config[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RoutingEvaluationInput is what a RoutingPolicy is asked to recommend
// against - built ONLY from an already-captured CandidateSnapshot
// (never a live registry query), so Champion and Challenger(s)
// evaluating the SAME decision always see the SAME candidate set,
// regardless of what has changed in the live registry since.
type RoutingEvaluationInput struct {
	Tenant    string
	TaskID    supervisor.TaskID
	TaskClass TaskClass
	Snapshot  CandidateSnapshot
}

// RoutingRecommendation is a policy's output - structurally identical in
// spirit to supervisor.AgentRecommendation (ADR 0006): an AgentID,
// bounded reasons, and confidence/sufficiency metadata. There is no
// model/provider field here either - a RoutingPolicy recommends agents,
// never models (ADR 0007's authority boundary, unchanged).
// Insufficient, if true, means the policy had no usable signal (e.g.
// cold start) rather than returning a guess - distinct from an error.
type RoutingRecommendation struct {
	AgentID      supervisor.AgentID
	Reasons      []string
	Confidence   supervisor.ConfidenceBand
	Insufficient bool
}

// RoutingPolicy is Phase 9's evaluation-time policy boundary. Champion
// and Challenger are both just RoutingPolicy values - there is no
// separate "ChampionPolicy"/"ChallengerPolicy" Go type, only a role
// assigned by the CALLER (ShadowEvaluator.Evaluate's parameters), which
// is also exactly how "Challenger output can never reach Executor" is
// proven structurally: nothing in this interface, or anything that
// calls it, has any reference to supervisor.Executor or
// supervisor.ExecutionProvider anywhere - a RoutingPolicy value cannot
// execute a task even if it wanted to, because the type it would need
// to call is not importable from where Recommend's signature lives.
type RoutingPolicy interface {
	Descriptor() RoutingPolicyDescriptor
	Recommend(ctx context.Context, input RoutingEvaluationInput) (RoutingRecommendation, error)
}

// AdvisorRoutingPolicy adapts an existing supervisor.AgentDecisionAdvisor
// (ADR 0006 Phase 7B - e.g. DeterministicAdvisor) to RoutingPolicy,
// rather than inventing a parallel policy abstraction. This is the
// explicitly recommended reuse path: "wenn bestehender
// AgentDecisionAdvisor sauber als Policy genutzt werden kann,
// adaptiere ihn."
type AdvisorRoutingPolicy struct {
	Advisor     supervisor.AgentDecisionAdvisor
	Descriptor_ RoutingPolicyDescriptor
}

func NewAdvisorRoutingPolicy(advisor supervisor.AgentDecisionAdvisor, descriptor RoutingPolicyDescriptor) *AdvisorRoutingPolicy {
	return &AdvisorRoutingPolicy{Advisor: advisor, Descriptor_: descriptor}
}

func (p *AdvisorRoutingPolicy) Descriptor() RoutingPolicyDescriptor { return p.Descriptor_ }

func (p *AdvisorRoutingPolicy) Recommend(ctx context.Context, input RoutingEvaluationInput) (RoutingRecommendation, error) {
	candidates := make([]supervisor.AgentCandidate, len(input.Snapshot.AgentIDs))
	for i, id := range input.Snapshot.AgentIDs {
		candidates[i] = supervisor.AgentCandidate{AgentID: supervisor.AgentID(id)}
	}
	task := supervisor.Task{ID: input.TaskID, Tenant: input.Tenant, Type: string(input.TaskClass)}
	advice, err := p.Advisor.Advise(ctx, task, candidates)
	if err != nil {
		return RoutingRecommendation{}, err
	}
	if len(advice.Ranked) == 0 {
		return RoutingRecommendation{Insufficient: true}, nil
	}
	top := advice.Ranked[0]
	if top.Confidence == supervisor.ConfidenceInsufficient {
		return RoutingRecommendation{Insufficient: true, AgentID: top.AgentID, Reasons: top.Reasons, Confidence: top.Confidence}, nil
	}
	return RoutingRecommendation{AgentID: top.AgentID, Reasons: top.Reasons, Confidence: top.Confidence}, nil
}

var _ RoutingPolicy = (*AdvisorRoutingPolicy)(nil)

// StaticRoutingPolicy is a trivial RoutingPolicy that always recommends
// one fixed agent - used to represent a "legacy-v1" deterministic
// Champion (e.g. the existing capability-based routeAgent fallback's
// ACTUAL decision for this task) without needing to re-run any
// eligibility logic inside the evaluation plane itself. The evaluation
// plane takes the Champion's REAL, already-made decision as ground
// truth input - it never re-derives what Champion "would" have picked.
type StaticRoutingPolicy struct {
	Descriptor_ RoutingPolicyDescriptor
	AgentID     supervisor.AgentID
	Reasons     []string
}

func (p StaticRoutingPolicy) Descriptor() RoutingPolicyDescriptor { return p.Descriptor_ }
func (p StaticRoutingPolicy) Recommend(ctx context.Context, input RoutingEvaluationInput) (RoutingRecommendation, error) {
	return RoutingRecommendation{AgentID: p.AgentID, Reasons: p.Reasons}, nil
}

var _ RoutingPolicy = StaticRoutingPolicy{}
