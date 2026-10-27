package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVoiceClientIsTenantBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HarnessMesh-Tenant") != "tenant-a" {
			t.Fatalf("tenant header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello","confidence":0.99}`))
	}))
	defer server.Close()
	result, err := (VoiceClient{Endpoint: server.URL}).Transcribe(context.Background(), "tenant-a", "wav", []byte("audio"))
	if err != nil || result.Text != "hello" {
		t.Fatalf("unexpected transcription: %#v err=%v", result, err)
	}
}
