package protocol

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEnvelopeValidation(t *testing.T) {
	valid := PeerEnvelope{
		Protocol:  PeerProtocolV1,
		ID:        "msg_123",
		SessionID: "hm_abc",
		From:      "claude",
		To:        "codex",
		Type:      MsgReviewRequest,
		CreatedAt: time.Now().UTC(),
		Payload:   json.RawMessage(`{"scope": ["internal/"]}`),
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid envelope, got: %v", err)
	}

	invalidType := valid
	invalidType.Type = "unknown_type"
	if err := invalidType.Validate(); err == nil {
		t.Fatal("expected error on unknown message type")
	}

	invalidProtocol := valid
	invalidProtocol.Protocol = "invalid/v9"
	if err := invalidProtocol.Validate(); err == nil {
		t.Fatal("expected error on invalid protocol")
	}

	missingID := valid
	missingID.ID = ""
	if err := missingID.Validate(); err == nil {
		t.Fatal("expected error on missing id")
	}

	missingSession := valid
	missingSession.SessionID = ""
	if err := missingSession.Validate(); err == nil {
		t.Fatal("expected error on missing session_id")
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected RetryCategory
	}{
		{
			name:     "timeout error",
			err:      &PeerTimeoutError{Peer: "codex", Timeout: 5 * time.Second},
			expected: RetryCategoryTimeout,
		},
		{
			name:     "peer unavailable",
			err:      &PeerUnavailableError{Peer: "codex", Reason: "connection reset"},
			expected: RetryCategoryTransientTransport,
		},
		{
			name:     "depth exceeded",
			err:      &PeerDepthExceededError{CurrentDepth: 3, MaxDepth: 2},
			expected: RetryCategoryNonRetryable,
		},
		{
			name:     "auth 401",
			err:      errors.New("HTTP 401 Unauthorized: invalid api key"),
			expected: RetryCategoryPermanentAuth,
		},
		{
			name:     "rate limit 429",
			err:      errors.New("API rate limit exceeded (429)"),
			expected: RetryCategoryRateLimit,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: RetryCategoryNonRetryable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyError(tc.err)
			if got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}
