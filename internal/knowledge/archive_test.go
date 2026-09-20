package knowledge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if !stats.Indexed || stats.IndexPath == "" {
		t.Fatalf("expected persistent search index: %+v", stats)
	}
}

func TestArchiveEncryptionProjectIsolationAndCompaction(t *testing.T) {
	t.Setenv("HARNESSMESH_KNOWLEDGE_KEY", "test-key")
	t.Setenv("HARNESSMESH_PROJECT_ID", "project-a")
	path := filepath.Join(t.TempDir(), "knowledge.hmkz")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	if err := a.Append(context.Background(), Record{ID: "old", Timestamp: old, ProjectID: "project-a", Kind: "finding", Text: "old secret sk-test-1234567890"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(context.Background(), Record{ID: "new", Timestamp: time.Now().UTC(), ProjectID: "project-b", Kind: "decision", Text: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompactBefore(context.Background(), time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	results, err := a.Search(context.Background(), "new", SearchOptions{ProjectID: "project-b", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "new" {
		t.Fatalf("unexpected compacted results: %+v", results)
	}
	if got, err := a.Search(context.Background(), "old", SearchOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("expected old record to be compacted, got %+v", got)
	}
}

func FuzzArchiveSearchDoesNotPanic(f *testing.F) {
	f.Add("architecture token")
	f.Add("{}[]\\x00 malformed")
	f.Fuzz(func(t *testing.T, query string) {
		path := filepath.Join(t.TempDir(), "knowledge.hmkz")
		a, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Append(context.Background(), Record{Kind: "message", Text: "stable archive search"}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Search(context.Background(), query, SearchOptions{Limit: 5}); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func BenchmarkArchiveSearch(b *testing.B) {
	a, err := Open(filepath.Join(b.TempDir(), "benchmark.hmkz"))
	if err != nil {
		b.Fatal(err)
	}
	defer a.Close()
	for i := 0; i < 5000; i++ {
		if err := a.Append(context.Background(), Record{Kind: "message", Text: "architecture migration reliability discussion"}); err != nil {
			b.Fatal(err)
		}
	}
	if err := a.Flush(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Search(context.Background(), "architecture reliability", SearchOptions{Limit: 20}); err != nil {
			b.Fatal(err)
		}
	}
}
