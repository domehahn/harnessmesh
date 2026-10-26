package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPAdapterSubmit(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Fatal("missing token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"wf-1","status":"queued"}`))
	}))
	defer s.Close()
	id, err := (HTTPAdapter{Endpoint: s.URL, Token: "t"}).Submit(context.Background(), WorkflowRequest{Tenant: "tenant", Kind: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "wf-1" {
		t.Fatalf("got %q", id)
	}
}
