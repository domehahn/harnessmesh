package knowledge

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type VectorEntry struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id,omitempty"`
	Vector    []float32 `json:"vector"`
}

type VectorMatch struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"project_id,omitempty"`
	Score     float64 `json:"score"`
}

// VectorIndex is a compact, persistent cosine index. It is intentionally
// backend-neutral and can be replaced by pgvector/Qdrant through the same API.
type VectorIndex struct {
	mu      sync.RWMutex
	path    string
	entries map[string]VectorEntry
}

func OpenVectorIndex(path string) (*VectorIndex, error) {
	if path == "" {
		return nil, errors.New("vector index path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	index := &VectorIndex{path: path, entries: make(map[string]VectorEntry)}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return index, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry VectorEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		index.entries[entry.ID] = entry
	}
	return index, scanner.Err()
}

func (v *VectorIndex) Upsert(entry VectorEntry) error {
	if entry.ID == "" || len(entry.Vector) == 0 {
		return errors.New("vector entry requires id and vector")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.entries[entry.ID] = entry
	return v.persistLocked()
}

func (v *VectorIndex) Search(query []float32, projectID string, limit int) []VectorMatch {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if limit <= 0 || limit > 1000 {
		limit = 20
	}
	results := make([]VectorMatch, 0, len(v.entries))
	for _, entry := range v.entries {
		if projectID != "" && entry.ProjectID != projectID {
			continue
		}
		results = append(results, VectorMatch{ID: entry.ID, ProjectID: entry.ProjectID, Score: cosine(query, entry.Vector)})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func cosine(left, right []float32) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for i := range left {
		dot += float64(left[i]) * float64(right[i])
		leftNorm += float64(left[i]) * float64(left[i])
		rightNorm += float64(right[i]) * float64(right[i])
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func (v *VectorIndex) persistLocked() error {
	tmp := v.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	entries := make([]VectorEntry, 0, len(v.entries))
	for _, entry := range v.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, v.path)
}
