package codeintel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildSearchAndImpact(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc Exported() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\nfunc Call() { Exported() }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	i, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := i.Search("exported", 10); len(got) != 1 || got[0].Name != "Exported" {
		t.Fatalf("unexpected search result: %+v", got)
	}
	impact := i.Impact("Exported")
	if len(impact) != 2 {
		t.Fatalf("expected two impacted files, got %v", impact)
	}
}
