package knowledge

import (
	"path/filepath"
	"testing"
)

func TestVectorIndexPersistsAndFiltersProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.jsonl")
	index, err := OpenVectorIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(VectorEntry{ID: "a", ProjectID: "one", Vector: []float32{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(VectorEntry{ID: "b", ProjectID: "two", Vector: []float32{0, 1}}); err != nil {
		t.Fatal(err)
	}
	results := index.Search([]float32{1, 0}, "one", 10)
	if len(results) != 1 || results[0].ID != "a" || results[0].Score < 0.99 {
		t.Fatalf("unexpected vector results: %+v", results)
	}
	reopened, err := OpenVectorIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Search([]float32{0, 1}, "two", 10); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("unexpected persisted vector results: %+v", got)
	}
}
