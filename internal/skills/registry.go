// Package skills implements HarnessMesh's lazy, policy-aware skill layer.
// A registry exposes metadata first; the full SKILL.md is loaded only after a
// skill is selected. Skills narrow MCP visibility, they never grant access
// beyond the configured capability and tool allowlists.
package skills

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Manifest struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description,omitempty"`
	Triggers     []string `json:"triggers,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	StrictOrder  bool     `json:"strict_order,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	SHA256       string   `json:"sha256,omitempty"`
	Signature    string   `json:"signature,omitempty"`
	PublicKey    string   `json:"public_key,omitempty"`
}

type Entry struct {
	Manifest          Manifest `json:"manifest"`
	Path              string   `json:"path"`
	Loaded            bool     `json:"loaded"`
	Verified          bool     `json:"verified"`
	SignatureVerified bool     `json:"signature_verified"`
	Content           string   `json:"content,omitempty"`
	root              *os.Root
	relative          string
}
type Registry struct {
	Root              string
	Entries           map[string]Entry
	RequireHash       bool
	RequireSignature  bool
	TrustedPublicKeys []ed25519.PublicKey
	root              *os.Root
	evalMu            sync.RWMutex
	evaluations       map[string]Evaluation
	evaluationPath    string
}

type Evaluation struct {
	Runs        int     `json:"runs"`
	Successful  int     `json:"successful"`
	SuccessRate float64 `json:"success_rate"`
}

func Discover(root string, requireHash bool) (*Registry, error) {
	return DiscoverWithOptions(root, requireHash, false, nil)
}

func DiscoverWithOptions(root string, requireHash, requireSignature bool, trusted []ed25519.PublicKey) (*Registry, error) {
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
		entry.Verified, entry.SignatureVerified = verifyContent(entry, requireHash, requireSignature, trusted)
		if requireSignature && !entry.SignatureVerified {
			return fmt.Errorf("skill %q failed signature verification", m.Name)
		}
		entries[m.Name] = entry
		return nil
	})
	if err != nil {
		return nil, err
	}
	registry := &Registry{
		Root:              absRoot,
		Entries:           entries,
		RequireHash:       requireHash,
		RequireSignature:  requireSignature,
		TrustedPublicKeys: trusted,
		root:              rootFS,
		evaluations:       map[string]Evaluation{},
		evaluationPath:    filepath.Join(absRoot, ".harnessmesh-evaluations.json"),
	}
	if err := registry.loadEvaluations(); err != nil {
		return nil, fmt.Errorf("load skill evaluations: %w", err)
	}
	return registry, nil
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

// SelectContext chooses the highest-scoring skill for a task description and
// optional capabilities. Mature evaluation results can exclude unreliable
// skills and prefer reliable ones; ties remain deterministic by name.
func (r *Registry) SelectContext(query string, capabilities []string) (Entry, error) {
	text := strings.ToLower(strings.TrimSpace(query))
	if text == "" {
		return Entry{}, errors.New("skill context is required")
	}
	capSet := map[string]bool{}
	for _, capability := range capabilities {
		capSet[strings.ToLower(strings.TrimSpace(capability))] = true
	}
	bestName, bestScore := "", 0
	for name, entry := range r.Entries {
		eval := r.Evaluation(name)
		if eval.Runs >= 3 && eval.SuccessRate < 0.5 {
			continue
		}
		score := 0
		if strings.Contains(text, strings.ToLower(entry.Manifest.Name)) {
			score += 100
		}
		for _, trigger := range entry.Manifest.Triggers {
			if strings.Contains(text, strings.ToLower(trigger)) {
				score += 20
			}
		}
		for _, capability := range entry.Manifest.Capabilities {
			if capSet[strings.ToLower(capability)] {
				score += 10
			}
		}
		if eval.Runs >= 3 {
			score += int(eval.SuccessRate * 10)
		}
		if score > bestScore || (score > 0 && score == bestScore && (bestName == "" || name < bestName)) {
			bestName, bestScore = name, score
		}
	}
	if bestScore == 0 {
		return Entry{}, fmt.Errorf("no skill matches context %q", query)
	}
	return r.Load(bestName)
}

func (r *Registry) ObserveOutcome(name string, success bool) {
	version := ""
	if r != nil {
		if entry, ok := r.Entries[name]; ok {
			version = entry.Manifest.Version
		}
	}
	r.ObserveOutcomeVersion(name, version, success)
}

// ObserveOutcomeVersion records a result for an exact skill version. A new
// artifact version therefore starts with a clean evaluation history.
func (r *Registry) ObserveOutcomeVersion(name, version string, success bool) {
	if r == nil || name == "" {
		return
	}
	key := evaluationKey(name, version)
	r.evalMu.Lock()
	defer r.evalMu.Unlock()
	if r.evaluations == nil {
		r.evaluations = map[string]Evaluation{}
	}
	eval := r.evaluations[key]
	eval.Runs++
	if success {
		eval.Successful++
	}
	eval.SuccessRate = float64(eval.Successful) / float64(eval.Runs)
	r.evaluations[key] = eval
	// Evaluation persistence is best effort. A read-only skill root must not
	// turn an otherwise authorized tool call into a failure.
	_ = r.persistEvaluationsLocked()
}

func (r *Registry) Evaluation(name string) Evaluation {
	r.evalMu.RLock()
	defer r.evalMu.RUnlock()
	version := ""
	if entry, ok := r.Entries[name]; ok {
		version = entry.Manifest.Version
	}
	return r.evaluations[evaluationKey(name, version)]
}

func evaluationKey(name, version string) string {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if version == "" {
		return name
	}
	return name + "@" + version
}

// SetEvaluationPath changes the durable evaluation file before the registry is
// used for context selection. It is intended for deployments that mount a
// writable state directory separately from the skill artifacts.
func (r *Registry) SetEvaluationPath(path string) error {
	if r == nil {
		return errors.New("skill registry is nil")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	r.evalMu.Lock()
	defer r.evalMu.Unlock()
	r.evaluationPath = absPath
	r.evaluations = map[string]Evaluation{}
	return r.loadEvaluations()
}

func (r *Registry) loadEvaluations() error {
	if r == nil || r.evaluationPath == "" {
		return nil
	}
	raw, err := os.ReadFile(r.evaluationPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var evaluations map[string]Evaluation
	if err := json.Unmarshal(raw, &evaluations); err != nil {
		return err
	}
	for name, evaluation := range evaluations {
		if strings.TrimSpace(name) == "" || evaluation.Runs < 0 || evaluation.Successful < 0 || evaluation.Successful > evaluation.Runs {
			return fmt.Errorf("invalid evaluation for %q", name)
		}
		if evaluation.Runs == 0 {
			evaluation.SuccessRate = 0
		} else {
			evaluation.SuccessRate = float64(evaluation.Successful) / float64(evaluation.Runs)
		}
		r.evaluations[name] = evaluation
	}
	return nil
}

func (r *Registry) persistEvaluationsLocked() error {
	if r == nil || r.evaluationPath == "" {
		return nil
	}
	raw, err := json.MarshalIndent(r.evaluations, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.evaluationPath), ".harnessmesh-evaluations-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, r.evaluationPath)
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
	e.Verified, e.SignatureVerified = verifyContent(e, r.RequireHash, r.RequireSignature, r.TrustedPublicKeys)
	if r.RequireHash && !e.Verified {
		return Entry{}, fmt.Errorf("skill %q failed hash verification", name)
	}
	if r.RequireSignature && !e.SignatureVerified {
		return Entry{}, fmt.Errorf("skill %q failed signature verification", name)
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

func verifyContent(e Entry, required, requireSignature bool, trusted []ed25519.PublicKey) (bool, bool) {
	if e.Manifest.SHA256 == "" {
		return !required, false
	}
	if e.root == nil {
		return false, false
	}
	raw, err := e.root.ReadFile(e.relative)
	if err != nil {
		return false, false
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	verified := strings.EqualFold(e.Manifest.SHA256, digest)
	if !verified || e.Manifest.Signature == "" || e.Manifest.PublicKey == "" {
		return verified, false
	}
	pub, pubErr := base64.StdEncoding.DecodeString(e.Manifest.PublicKey)
	sig, sigErr := base64.StdEncoding.DecodeString(e.Manifest.Signature)
	if pubErr != nil || sigErr != nil || len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return verified, false
	}
	if len(trusted) > 0 {
		trustedKey := false
		for _, key := range trusted {
			if string(key) == string(pub) {
				trustedKey = true
				break
			}
		}
		if !trustedKey {
			return verified, false
		}
	}
	return verified, ed25519.Verify(ed25519.PublicKey(pub), []byte(digest), sig)
}
