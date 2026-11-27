package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

type failedGitHubRun struct {
	HeadSHA   string `json:"headSha"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	FailedJob string `json:"-"`
	Jobs      []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
	} `json:"jobs"`
}

// readFailedGitHubRun imports metadata from gh run view. Job metadata stays
// descriptive and cannot supply a proof command or mark an obligation passed.
func readFailedGitHubRun(path, job string) (failedGitHubRun, error) {
	var run failedGitHubRun
	raw, err := os.ReadFile(path)
	if err != nil {
		return run, fmt.Errorf("read GitHub Actions run: %w", err)
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		return run, fmt.Errorf("parse GitHub Actions run: %w", err)
	}
	if run.Status != "completed" {
		return run, errors.New("GitHub Actions run must be completed")
	}
	if _, err := hex.DecodeString(run.HeadSHA); err != nil || len(run.HeadSHA) != 40 && len(run.HeadSHA) != 64 {
		return run, errors.New("GitHub Actions run has an invalid head SHA")
	}
	u, err := url.Parse(run.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || !strings.Contains(u.Path, "/actions/runs/") {
		return run, errors.New("GitHub Actions run has an invalid URL")
	}
	for _, entry := range run.Jobs {
		if entry.Name == "" || entry.Conclusion != "failure" || (job != "" && entry.Name != job) {
			continue
		}
		if run.FailedJob != "" {
			return run, errors.New("multiple failed jobs matched; select one by its exact --github-job name")
		}
		run.FailedJob = entry.Name
	}
	if run.FailedJob == "" {
		return run, errors.New("no matching failed GitHub Actions job")
	}
	return run, nil
}

func verifyRecoveryCheckout(base string) (string, error) {
	head, err := exec.Command("git", "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read current worktree HEAD: %w", err)
	}
	expected, err := exec.Command("git", "rev-parse", "--verify", "--end-of-options", base+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("resolve CI checkout commit %q: %w", base, err)
	}
	headSHA := strings.TrimSpace(string(head))
	if headSHA != strings.TrimSpace(string(expected)) {
		return "", fmt.Errorf("worktree HEAD %s does not match the CI checkout %s", headSHA, strings.TrimSpace(string(expected)))
	}
	return headSHA, nil
}
