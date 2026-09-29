package bridge

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FuzzPostRequestBody proves that arbitrary/malformed request bodies to the
// bridge's mutating endpoints never panic - they must only ever produce an
// ordinary HTTP error response. This is the primary externally-reachable
// parser surface of the local bridge.
func FuzzPostRequestBody(f *testing.F) {
	f.Add("/api/v1/tasks", `{"space_id":"s1","title":"t","author_participant":"claude-executor"}`)
	f.Add("/api/v1/messages", `{"space_id":"s1","channel_id":"c1","message":"hi"}`)
	f.Add("/api/v1/findings", `{"session_id":"sess","id":"f1","severity":"low","category":"style","claim":"x"}`)
	f.Add("/api/v1/artifacts", `{"session_id":"sess","source_agent":"claude-executor"}`)
	f.Add("/api/v1/tasks", `not json`)
	f.Add("/api/v1/tasks", `{`)
	f.Add("/api/v1/tasks", `null`)
	f.Add("/api/v1/tasks", `{"space_id": {"nested":"object instead of string"}}`)
	f.Add("/api/v1/tasks", `{"unknown_field_xyz": true}`)

	f.Fuzz(func(t *testing.T, route string, body string) {
		switch route {
		case "/api/v1/tasks", "/api/v1/messages", "/api/v1/findings", "/api/v1/artifacts":
		default:
			t.Skip("only fuzz the known mutating routes")
		}

		// A fresh server per iteration keeps state isolated and side-steps
		// idempotency-cache growth across fuzz iterations.
		s, _, _ := setupTestBridge(t)
		h := s.Handler()

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("handler panicked on route=%q body=%q: %v", route, body, r)
			}
		}()

		r := httptest.NewRequest(http.MethodPost, route, bytes.NewReader([]byte(body)))
		r.Header.Set("Authorization", "Bearer bridge-test-token")
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		if rec.Code >= 500 {
			t.Fatalf("route=%q body=%q produced a 5xx (%d): %s", route, body, rec.Code, rec.Body.String())
		}
	})
}
