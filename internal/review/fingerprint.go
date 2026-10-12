package review

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// LocationBucket coarsens a line number into a small window, so two
// reports a few lines apart about the same logical issue (e.g. a
// multi-line function) can still correlate, while two genuinely distinct
// locations in the same file do not. Deliberately small and explicit -
// this is the ONLY fuzziness allowed in correlation; everything else
// (subject, rule/category, finding type) must match exactly.
const locationBucketSize = 5

// ClaimInput is the normalized set of fields ComputeClaimFingerprint
// combines. Correlation is NEVER based on free-text claim similarity
// (e.g. "SQL injection" vs. "Unsanitized query construction" at the same
// file:line DO correlate here, because they share subject+location+
// category even though their text differs - exactly the example ADR
// 0002 Phase 4 gives); but two reports at different locations in the
// same file never blindly merge, however similar their wording.
type ClaimInput struct {
	Subject     SubjectRef
	Category    string // e.g. "security", "correctness" - protocol.FindingPayload.Category
	FindingType string // e.g. a specific rule/analyzer ID, if known; may be empty
	Line        int
}

// ComputeClaimFingerprint deterministically derives a ClaimFingerprint
// from normalized fields - never from raw claim text. Two calls with
// equal normalized inputs always produce the same fingerprint; this is a
// pure function with no I/O.
func ComputeClaimFingerprint(in ClaimInput) ClaimFingerprint {
	repo := strings.TrimSpace(in.Subject.Repository)
	path := filepath.Clean(strings.TrimSpace(in.Subject.Path))
	category := strings.ToLower(strings.TrimSpace(in.Category))
	findingType := strings.ToLower(strings.TrimSpace(in.FindingType))
	bucket := 0
	if locationBucketSize > 0 && in.Line > 0 {
		bucket = (in.Line - 1) / locationBucketSize
	}

	h := sha256.New()
	for _, part := range []string{repo, path, category, findingType} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write([]byte{byte(bucket), byte(bucket >> 8), byte(bucket >> 16), byte(bucket >> 24)})
	return ClaimFingerprint(hex.EncodeToString(h.Sum(nil)))
}

// FindGroup locates the ReviewGroup an observation with the given
// fingerprint/artifact version/tenant belongs to, among existing groups.
// A ReviewGroup is scoped to exactly one ArtifactVersion: matching
// fingerprint + tenant but a DIFFERENT ArtifactVersion never joins an
// existing group (this is the staleness boundary - see
// StaleArtifactEvidence and docs/decisions/0002's Phase 4 "Stale
// Findings" section).
func FindGroup(groups []ReviewGroup, fingerprint ClaimFingerprint, version ArtifactVersion, tenant string) (ReviewGroup, bool) {
	for _, g := range groups {
		if g.Claim == fingerprint && g.ArtifactVersion == version && g.Tenant == tenant {
			return g, true
		}
	}
	return ReviewGroup{}, false
}
