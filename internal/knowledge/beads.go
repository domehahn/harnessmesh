package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/contextpack"
)

const (
	BeadStatusOpen       = "open"
	BeadStatusInProgress = "in_progress"
	BeadStatusBlocked    = "blocked"
	BeadStatusDone       = "done"
)

// BeadTask represents a dependency-aware task node inspired by Beads (bd) to
// eliminate long-horizon agentic amnesia across sessions and compactions.
type BeadTask struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"` // open, in_progress, blocked, done
	Priority   int       `json:"priority"`
	DependsOn  []string  `json:"depends_on,omitempty"`
	Assignee   string    `json:"assignee,omitempty"`
	FileScope  []string  `json:"file_scope,omitempty"`
	EvidenceID string    `json:"evidence_id,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// BeadGraph is the resolved dependency view of all task beads in a project.
type BeadGraph struct {
	Tasks   []BeadTask `json:"tasks"`
	Ready   []BeadTask `json:"ready"`
	Blocked []BeadTask `json:"blocked"`
	Done    []BeadTask `json:"done"`
	Cycles  []string   `json:"cycles,omitempty"`
}

func NormalizeBeadTask(t BeadTask) (BeadTask, error) {
	t.ID = strings.TrimSpace(t.ID)
	t.Title = contextpack.RedactSecrets(strings.TrimSpace(t.Title))
	if t.ID == "" || t.Title == "" {
		return BeadTask{}, errors.New("bead task id and title are required")
	}
	status := strings.ToLower(strings.TrimSpace(t.Status))
	if status == "" {
		status = BeadStatusOpen
	}
	switch status {
	case BeadStatusOpen, BeadStatusInProgress, BeadStatusBlocked, BeadStatusDone:
		t.Status = status
	default:
		return BeadTask{}, fmt.Errorf("unsupported bead status %q (expected open, in_progress, blocked or done)", t.Status)
	}
	if t.Priority <= 0 {
		t.Priority = 3
	}
	if t.Priority > 5 {
		t.Priority = 5
	}
	var deps []string
	seen := map[string]bool{}
	for _, d := range t.DependsOn {
		d = strings.TrimSpace(d)
		if d != "" && d != t.ID && !seen[d] {
			seen[d] = true
			deps = append(deps, d)
		}
	}
	sort.Strings(deps)
	t.DependsOn = deps
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = time.Now().UTC()
	}
	return t, nil
}

// ResolveBeads folds an append-only sequence of BeadTask updates into the
// latest state per task ID, detects dependency cycles, and computes which tasks
// are immediately Ready for an agent to execute.
func ResolveBeads(tasks []BeadTask) BeadGraph {
	byID := make(map[string]BeadTask, len(tasks))
	for _, raw := range tasks {
		norm, err := NormalizeBeadTask(raw)
		if err != nil {
			continue
		}
		if prev, ok := byID[norm.ID]; ok && prev.UpdatedAt.After(norm.UpdatedAt) {
			continue
		}
		byID[norm.ID] = norm
	}

	all := make([]BeadTask, 0, len(byID))
	for _, t := range byID {
		all = append(all, t)
	}
	sortBeads(all)

	inCycle := detectBeadCycles(byID)
	var cycles []string
	for id := range inCycle {
		cycles = append(cycles, id)
	}
	sort.Strings(cycles)

	var ready, blocked, done []BeadTask
	for _, t := range all {
		if t.Status == BeadStatusDone {
			done = append(done, t)
			continue
		}
		if t.Status == BeadStatusBlocked || inCycle[t.ID] {
			blocked = append(blocked, t)
			continue
		}
		depsDone := true
		for _, depID := range t.DependsOn {
			dep, exists := byID[depID]
			if !exists || dep.Status != BeadStatusDone {
				depsDone = false
				break
			}
		}
		if depsDone {
			ready = append(ready, t)
		} else {
			blocked = append(blocked, t)
		}
	}

	return BeadGraph{
		Tasks:   all,
		Ready:   ready,
		Blocked: blocked,
		Done:    done,
		Cycles:  cycles,
	}
}

// BeadsFromRecords extracts BeadTask entries stored as "task_bead" records in
// the HarnessMesh knowledge archive.
func BeadsFromRecords(records []Record) []BeadTask {
	out := make([]BeadTask, 0, len(records))
	// Records from archive search are newest-first or arbitrary order; parse JSON from Text/Payload
	for _, r := range records {
		if r.Kind != "task_bead" {
			continue
		}
		var task BeadTask
		if len(r.Payload) > 0 && json.Unmarshal(r.Payload, &task) == nil && task.ID != "" {
			if task.UpdatedAt.IsZero() {
				task.UpdatedAt = r.Timestamp
			}
			out = append(out, task)
			continue
		}
		if json.Unmarshal([]byte(r.Text), &task) == nil && task.ID != "" {
			if task.UpdatedAt.IsZero() {
				task.UpdatedAt = r.Timestamp
			}
			out = append(out, task)
		}
	}
	return out
}

func detectBeadCycles(byID map[string]BeadTask) map[string]bool {
	inCycle := map[string]bool{}
	for startID := range byID {
		visited := map[string]int{} // 0=unvisited, 1=visiting, 2=done
		var dfs func(curr string) bool
		dfs = func(curr string) bool {
			visited[curr] = 1
			for _, dep := range byID[curr].DependsOn {
				if _, ok := byID[dep]; !ok {
					continue
				}
				if visited[dep] == 1 {
					inCycle[curr] = true
					inCycle[dep] = true
					return true
				}
				if visited[dep] == 0 && dfs(dep) {
					inCycle[curr] = true
					return true
				}
			}
			visited[curr] = 2
			return false
		}
		_ = dfs(startID)
	}
	return inCycle
}

func sortBeads(tasks []BeadTask) {
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority < tasks[j].Priority
		}
		return tasks[i].ID < tasks[j].ID
	})
}
