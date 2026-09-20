package knowledge

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAIEmbeddingProvider(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing auth header")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)), Header: make(http.Header)}, nil
	})}
	provider := &OpenAIEmbeddingProvider{APIKey: "test-key", BaseURL: "http://embedding.test", Client: client, Model: "test"}
	vectors, err := provider.Embed(context.Background(), []string{"hello"})
	if err != nil || len(vectors) != 1 || len(vectors[0]) != 2 {
		t.Fatalf("unexpected embedding result: err=%v vectors=%v", err, vectors)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
