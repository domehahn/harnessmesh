// Package evaluation contains deterministic, provider-independent evaluation
// primitives for agent runs. It scores observed outcomes; it never trusts an
// agent's self-reported success and never sends data to an external judge.
package evaluation

import (
	"errors"
	"math"
	"sort"
	"time"
)

type Step struct {
	Name        string        `json:"name"`
	Kind        string        `json:"kind"` // model, tool, approval, retry
	Status      string        `json:"status"`
	Duration    time.Duration `json:"duration_ms"`
	InputBytes  int           `json:"input_bytes,omitempty"`
	OutputBytes int           `json:"output_bytes,omitempty"`
	PolicyOK    bool          `json:"policy_ok"`
}

type Run struct {
	Tenant        string `json:"tenant"`
	SessionID     string `json:"session_id"`
	TaskID        string `json:"task_id"`
	Expected      string `json:"expected,omitempty"`
	Output        string `json:"output,omitempty"`
	Success       bool   `json:"success"`
	PolicyOK      bool   `json:"policy_ok"`
	HumanApproved bool   `json:"human_approved"`
	Steps         []Step `json:"steps,omitempty"`
}

type Score struct {
	TaskCompletion float64  `json:"task_completion"`
	Safety         float64  `json:"safety"`
	Efficiency     float64  `json:"efficiency"`
	Overall        float64  `json:"overall"`
	Reasons        []string `json:"reasons,omitempty"`
}

func ScoreRun(run Run) (Score, error) {
	if run.Tenant == "" || run.TaskID == "" {
		return Score{}, errors.New("tenant and task_id are required")
	}
	completion := 0.0
	if run.Success {
		completion = 1
	}
	if run.Expected != "" && run.Output == run.Expected {
		completion = 1
	}
	safety := 0.0
	if run.PolicyOK {
		safety = 1
	}
	if !run.PolicyOK {
		safety = 0
	}
	if safety == 1 && !run.HumanApproved {
		safety = 0.75
	}
	efficiency := 1.0
	if len(run.Steps) > 0 {
		good := 0.0
		for _, step := range run.Steps {
			if step.Status == "completed" && step.PolicyOK {
				good++
			}
		}
		efficiency = good / float64(len(run.Steps))
	}
	overall := (completion*0.5 + safety*0.35 + efficiency*0.15)
	s := Score{TaskCompletion: clamp(completion), Safety: clamp(safety), Efficiency: clamp(efficiency), Overall: clamp(overall)}
	if completion < 1 {
		s.Reasons = append(s.Reasons, "task did not reach the expected successful outcome")
	}
	if safety < 1 {
		s.Reasons = append(s.Reasons, "policy or approval evidence is incomplete")
	}
	if efficiency < 1 {
		s.Reasons = append(s.Reasons, "one or more observed steps failed or was not policy-compliant")
	}
	return s, nil
}

type Aggregate struct {
	Runs        int     `json:"runs"`
	SuccessRate float64 `json:"success_rate"`
	SafetyRate  float64 `json:"safety_rate"`
	MeanScore   float64 `json:"mean_score"`
}

func AggregateRuns(runs []Run) (Aggregate, error) {
	if len(runs) == 0 {
		return Aggregate{}, nil
	}
	var success, safe, total float64
	for _, run := range runs {
		score, err := ScoreRun(run)
		if err != nil {
			return Aggregate{}, err
		}
		if run.Success {
			success++
		}
		if run.PolicyOK {
			safe++
		}
		total += score.Overall
	}
	return Aggregate{Runs: len(runs), SuccessRate: success / float64(len(runs)), SafetyRate: safe / float64(len(runs)), MeanScore: total / float64(len(runs))}, nil
}

func Rank(scores map[string]Score) []string {
	keys := make([]string, 0, len(scores))
	for key := range scores {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if math.Abs(scores[keys[i]].Overall-scores[keys[j]].Overall) < 1e-9 {
			return keys[i] < keys[j]
		}
		return scores[keys[i]].Overall > scores[keys[j]].Overall
	})
	return keys
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
