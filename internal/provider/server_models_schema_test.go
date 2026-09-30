package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file proves the fix for a real Codex app-server decode failure
// against GET /v1/models: "failed to decode models response: Data at
// line 1 column 98 (body: 99 bytes)". The response WAS valid JSON (jq
// validated it), but it omitted "created" and "owned_by" - both REQUIRED
// fields of the documented OpenAI Model object
// (developers.openai.com/api/reference/resources/models, fetched
// 2026-09-30: id:string, object:"model", created:number, owned_by:string,
// shutdown_date:optional). "Valid JSON" was not sufficient; the Codex
// model-catalog deserializer expects this exact, complete shape.

// codexModel mirrors the documented OpenAI Model object schema exactly,
// for asserting the response the Codex app-server actually deserializes
// against - not just "is this valid JSON".
type codexModel struct {
	ID           string  `json:"id"`
	Object       string  `json:"object"`
	Created      int64   `json:"created"`
	OwnedBy      string  `json:"owned_by"`
	ShutdownDate *string `json:"shutdown_date,omitempty"`
}

type codexModelList struct {
	Object string       `json:"object"`
	Data   []codexModel `json:"data"`
}

func TestServer_Models_MatchesCodexExpectedSchema(t *testing.T) {
	backend := &fakeInferenceBackend{name: "chatgpt", typ: "chatgpt-subscription", events: simpleTextEvents("resp_1", "ok")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"chatgpt": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "chatgpt"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Decode against the documented Model object schema (extra fields like
	// HarnessMesh's own "backend_type" are intentionally still permitted -
	// standard, conforming JSON deserializers ignore unknown fields by
	// default, and this endpoint deliberately keeps that field for
	// HarnessMesh's own diagnostic use). This is the actual regression
	// check: zero-valued required fields (created=0, owned_by="") are
	// exactly what a previous version of this endpoint produced by never
	// setting them at all.
	var list codexModelList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("response does not match the documented Codex-expected Model schema (id/object/created/owned_by): %v\nbody: %s", err, rec.Body.String())
	}
	if list.Object != "list" {
		t.Fatalf(`expected top-level object="list", got %q`, list.Object)
	}
	if len(list.Data) == 0 {
		t.Fatalf("expected at least one model")
	}
	for _, m := range list.Data {
		if m.ID == "" {
			t.Fatalf("expected non-empty id, got %+v", m)
		}
		if m.Object != "model" {
			t.Fatalf(`expected object="model", got %q`, m.Object)
		}
		if m.Created == 0 {
			t.Fatalf("expected a non-zero created timestamp (the confirmed root cause: this field was previously omitted entirely), got %+v", m)
		}
		if m.OwnedBy == "" {
			t.Fatalf("expected a non-empty owned_by (the confirmed root cause: this field was previously omitted entirely), got %+v", m)
		}
	}
}
