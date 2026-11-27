package codeintel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

type ImpactPilot struct {
	Symbol        string           `json:"symbol"`
	BaselineFiles []string         `json:"baseline_files"`
	SyntaxFiles   []string         `json:"syntax_files"`
	TestFiles     []string         `json:"test_files"`
	ExternalFiles []string         `json:"external_files,omitempty"`
	Scores        map[string]Score `json:"scores,omitempty"`
}

type Score struct {
	Matched []string `json:"matched"`
	Missed  []string `json:"missed"`
	Extra   []string `json:"extra"`
}

// CompareImpact uses exact Go identifiers for advisory review scope. Names
// with distinct declarations can coincide, so matches remain hints.
func (i *Index) CompareImpact(symbol string, expected, external []string) (ImpactPilot, error) {
	report := ImpactPilot{Symbol: symbol, BaselineFiles: uniquePaths(i.Impact(symbol))}
	var allTests []string
	packages := map[string]bool{}
	err := filepath.WalkDir(i.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(i.Root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		isTest := strings.HasSuffix(rel, "_test.go")
		if isTest {
			allTests = append(allTests, rel)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		found := false
		ast.Inspect(file, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Name == symbol {
				found = true
			}
			return !found
		})
		if found && isTest {
			report.TestFiles = append(report.TestFiles, rel)
		}
		if found && !isTest {
			report.SyntaxFiles = append(report.SyntaxFiles, rel)
			packages[filepath.Dir(rel)] = true
		}
		return nil
	})
	if err != nil {
		return ImpactPilot{}, err
	}
	for _, test := range allTests {
		if packages[filepath.Dir(test)] {
			report.TestFiles = append(report.TestFiles, test)
		}
	}
	report.TestFiles = uniquePaths(report.TestFiles)
	report.ExternalFiles = uniquePaths(external)
	if expected != nil {
		report.Scores = map[string]Score{
			"baseline": scorePaths(report.BaselineFiles, expected),
			"syntax":   scorePaths(append(slices.Clone(report.SyntaxFiles), report.TestFiles...), expected),
		}
		if external != nil {
			report.Scores["external"] = scorePaths(report.ExternalFiles, expected)
		}
	}
	return report, nil
}

func uniquePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		result = append(result, filepath.ToSlash(filepath.Clean(path)))
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func scorePaths(observed, expected []string) Score {
	result := Score{Matched: []string{}, Missed: []string{}, Extra: []string{}}
	target := uniquePaths(expected)
	observed = uniquePaths(observed)
	for _, path := range target {
		if slices.Contains(observed, path) {
			result.Matched = append(result.Matched, path)
		} else {
			result.Missed = append(result.Missed, path)
		}
	}
	for _, path := range observed {
		if !slices.Contains(target, path) {
			result.Extra = append(result.Extra, path)
		}
	}
	return result
}
