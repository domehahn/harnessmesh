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

func TestImpactPilotComparesKnownPaths(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"calc.go":      "package calc\nfunc Add() {}\n",
		"other.go":     "package calc\nfunc Address() {}\n",
		"calc_test.go": "package calc\nfunc TestAdd() { Add() }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := index.CompareImpact("Add", []string{"calc.go", "calc_test.go"}, []string{"calc.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Scores["syntax"]; len(got.Matched) != 2 || len(got.Missed)+len(got.Extra) != 0 {
		t.Fatalf("syntax comparison: %+v", got)
	}
	if got := report.Scores["baseline"]; len(got.Missed) != 1 || len(got.Extra) != 1 {
		t.Fatalf("baseline comparison: %+v", got)
	}
	if got := report.Scores["external"]; len(got.Missed) != 1 {
		t.Fatalf("external comparison: %+v", got)
	}
}
