// Package codeintel provides a deterministic local code map. It is deliberately
// dependency-free and starts with Go symbols/references, which makes it safe to
// use as an MCP backend without uploading a repository to a SaaS search index.
package codeintel

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type Match struct {
	Symbol
	Score int `json:"score"`
}

type Index struct {
	Root    string
	Symbols []Symbol
	Refs    map[string][]string
}

func Build(root string) (*Index, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	i := &Index{Root: root, Refs: map[string][]string{}}
	var files []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil
		} // incomplete files should not break discovery
		short, _ := filepath.Rel(root, path)
		fset := token.NewFileSet()
		f, err = parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var name, kind string
			switch x := n.(type) {
			case *ast.FuncDecl:
				name, kind = x.Name.Name, "function"
			case *ast.TypeSpec:
				name, kind = x.Name.Name, "type"
			case *ast.ValueSpec:
				kind = "value"
				if len(x.Names) > 0 {
					name = x.Names[0].Name
				}
			}
			if name != "" && ast.IsExported(name) {
				pos := fset.Position(n.Pos())
				i.Symbols = append(i.Symbols, Symbol{Name: name, Kind: kind, File: short, Line: pos.Line})
			}
			return true
		})
		return nil
	})
	if err != nil {
		return i, err
	}
	for _, path := range files {
		file, openErr := os.Open(path)
		if openErr != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		rel, _ := filepath.Rel(root, path)
		for scanner.Scan() {
			line := scanner.Text()
			for _, symbol := range i.Symbols {
				if strings.Contains(line, symbol.Name) {
					i.Refs[symbol.Name] = append(i.Refs[symbol.Name], rel)
				}
			}
		}
		_ = file.Close()
	}
	sort.Slice(i.Symbols, func(a, b int) bool {
		if i.Symbols[a].Name == i.Symbols[b].Name {
			return i.Symbols[a].File < i.Symbols[b].File
		}
		return i.Symbols[a].Name < i.Symbols[b].Name
	})
	return i, err
}

func (i *Index) Search(query string, limit int) []Match {
	q := strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 20
	}
	var out []Match
	for _, s := range i.Symbols {
		name := strings.ToLower(s.Name)
		score := 0
		if name == q {
			score = 100
		} else if strings.Contains(name, q) {
			score = 70
		} else if strings.Contains(strings.ToLower(s.File), q) {
			score = 35
		}
		if score > 0 {
			out = append(out, Match{Symbol: s, Score: score})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score == out[b].Score {
			return out[a].Name < out[b].Name
		}
		return out[a].Score > out[b].Score
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (i *Index) Impact(symbol string) []string {
	refs := append([]string(nil), i.Refs[symbol]...)
	sort.Strings(refs)
	result := refs[:0]
	for _, f := range refs {
		if len(result) == 0 || result[len(result)-1] != f {
			result = append(result, f)
		}
	}
	return result
}
