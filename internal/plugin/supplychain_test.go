package plugin

import (
	"context"
	"strings"
	"testing"
)

func TestHTTPArtifactStoreRejectsTraversal(t *testing.T) {
	store := HTTPArtifactStore{BaseURL: "http://127.0.0.1"}
	if _, err := store.Put(context.Background(), "../escape", strings.NewReader("x")); err == nil {
		t.Fatal("expected traversal rejection")
	}
}
