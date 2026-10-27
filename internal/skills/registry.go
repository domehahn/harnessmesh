// Package skills implements HarnessMesh's lazy, policy-aware skill layer.
// A registry exposes metadata first; the full SKILL.md is loaded only after a
// skill is selected. Skills narrow MCP visibility, they never grant access
// beyond the configured capability and tool allowlists.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Manifest struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description,omitempty"`
	Triggers     []string `json:"triggers,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	SHA256       string   `json:"sha256,omitempty"`
	Signature    string   `json:"signature,omitempty"`
	PublicKey    string   `json:"public_key,omitempty"`
}

type Entry struct {
	Manifest Manifest `json:"manifest"`
	Path     string   `json:"path"`
	Loaded   bool     `json:"loaded"`
	Verified bool     `json:"verified"`
	Content  string   `json:"content,omitempty"`
	root     *os.Root
	relative string
}
type Registry struct {
	Root        string
	Entries     map[string]Entry
	RequireHash bool
	root        *os.Root
}

func Discover(root string, requireHash bool) (*Registry, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("skill root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootFS, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, err
	}
	entries := map[string]Entry{}
	err = fs.WalkDir(rootFS.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "skill.json" {
			return nil
		}
		raw, readErr := rootFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var m Manifest
		if json.Unmarshal(raw, &m) != nil || strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("invalid skill manifest %s", path)
		}
		if m.Version == "" {
			m.Version = "0.0.0"
		}
		contentPath := filepath.Join(filepath.Dir(path), "SKILL.md")
		entry := Entry{Manifest: m, Path: filepath.Join(absRoot, contentPath), root: rootFS, relative: contentPath}
		entry.Verified = verifyContent(entry, requireHash)
		entries[m.Name] = entry
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Registry{Root: absRoot, Entries: entries, RequireHash: requireHash, root: rootFS}, nil
}

func (r *Registry) List() []Entry {
	out := make([]Entry, 0, len(r.Entries))
	for _, e := range r.Entries {
		e.Content = ""
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.Name < out[j].Manifest.Name })
	return out
}

func (r *Registry) Select(query string) (Entry, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return Entry{}, errors.New("skill query is required")
	}
	for _, e := range r.Entries {
		if strings.EqualFold(e.Manifest.Name, query) {
			return r.Load(e.Manifest.Name)
		}
	}
	for _, e := range r.Entries {
		for _, trigger := range e.Manifest.Triggers {
			if strings.Contains(q, strings.ToLower(trigger)) {
				return r.Load(e.Manifest.Name)
			}
		}
	}
	return Entry{}, fmt.Errorf("no skill matches %q", query)
}

func (r *Registry) Load(name string) (Entry, error) {
	e, ok := r.Entries[name]
	if !ok {
		return Entry{}, fmt.Errorf("skill %q not found", name)
	}
	if e.root == nil {
		return Entry{}, errors.New("skill registry root is unavailable")
	}
	raw, err := e.root.ReadFile(e.relative)
	if err != nil {
		return Entry{}, err
	}
	e.Content = string(raw)
	e.Loaded = true
	e.Verified = verifyContent(e, r.RequireHash)
	if r.RequireHash && !e.Verified {
		return Entry{}, fmt.Errorf("skill %q failed hash verification", name)
	}
	return e, nil
}

func (e Entry) AllowsTool(name string) bool {
	if len(e.Manifest.Tools) == 0 {
		return false
	}
	for _, allowed := range e.Manifest.Tools {
		if allowed == name || allowed == "*" {
			return true
		}
	}
	return false
}
func (e Entry) AllowsCapability(name string) bool {
	for _, allowed := range e.Manifest.Capabilities {
		if allowed == name || allowed == "*" {
			return true
		}
	}
	return false
}
func (e Entry) SchemaTokenEstimate() int { return len(e.Manifest.Tools) * 80 }

func verifyContent(e Entry, required bool) bool {
	if e.Manifest.SHA256 == "" {
		return !required
	}
	if e.root == nil {
		return false
	}
	raw, err := e.root.ReadFile(e.relative)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(raw)
	return strings.EqualFold(e.Manifest.SHA256, hex.EncodeToString(sum[:]))
}
