package knowledge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveAppendSearchAndContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.hmkz")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		if err := a.Append(ctx, Record{
			ID:        "msg-" + string(rune('a'+i%26)),
			SessionID: "session-1",
			Kind:      "message",
			From:      "codex",
			Text:      "token refresh architecture discussion",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Append(ctx, Record{ID: "other", SessionID: "session-2", Kind: "finding", Text: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	a, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	results, err := a.Search(ctx, "architecture token", SearchOptions{SessionID: "session-1", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 25 {
		t.Fatalf("expected 25 results, got %d", len(results))
	}
	contextText := BuildContext(results, 1000)
	if !strings.Contains(contextText, "architecture discussion") {
		t.Fatalf("expected searchable context, got %q", contextText)
	}
	stats, err := a.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Bytes <= 8 || stats.Compression != "zstd" {
		t.Fatalf("unexpected archive stats: %+v", stats)
	}
}
