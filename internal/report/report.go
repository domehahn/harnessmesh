package report

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func NewRunDir(repo string) (string, error) {
	base := os.Getenv("HARNESSMESH_STATE_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			dir := filepath.Join(home, ".harnessmesh")
			testFile := filepath.Join(dir, ".permtest")
			if os.MkdirAll(dir, 0700) == nil && os.WriteFile(testFile, []byte("ok"), 0600) == nil {
				_ = os.Remove(testFile)
				base = dir
			}
		}
		if base == "" {
			base = ".harnessmesh"
		}
	}
	key := repoKey(repo)
	id := time.Now().UTC().Format("20060102T150405.000000000Z")
	dir := filepath.Join(base, "runs", key, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func repoKey(repo string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repo)))
	name := filepath.Base(filepath.Clean(repo))
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, name)
	return fmt.Sprintf("%s-%x", name, sum[:4])
}

func Write(dir string, result *protocol.RunResult) error {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	md := renderMarkdown(result)
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0600)
}

func renderMarkdown(r *protocol.RunResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HarnessMesh Run\n\n")
	fmt.Fprintf(&b, "- **Status:** `%s`\n", r.Status)
	fmt.Fprintf(&b, "- **Executor:** `%s`\n", r.Executor)
	fmt.Fprintf(&b, "- **Reviewer:** `%s`\n", r.Reviewer)
	fmt.Fprintf(&b, "- **Rounds:** %d\n", len(r.Rounds))
	fmt.Fprintf(&b, "- **Stop reason:** %s\n\n", r.StopReason)
	fmt.Fprintf(&b, "## Task\n\n%s\n\n", r.Task)

	for _, round := range r.Rounds {
		if round.Number == 0 {
			fmt.Fprintf(&b, "## Execution Failure (Pre-Review)\n\n")
			if round.Executor.RawOutput != "" {
				fmt.Fprintf(&b, "```\n%s\n```\n\n", round.Executor.RawOutput)
			}
			continue
		}

		fmt.Fprintf(&b, "## Round %d\n\n", round.Number)
		fmt.Fprintf(&b, "### Executor\n\n%s\n\n", round.Executor.Text)
		fmt.Fprintf(&b, "### Repository state\n\n")
		fmt.Fprintf(&b, "- HEAD: `%s`\n", round.Context.Head)
		fmt.Fprintf(&b, "- Branch: `%s`\n", round.Context.Branch)
		if round.Context.TestExitCode != nil {
			fmt.Fprintf(&b, "- Test exit code: `%d`\n", *round.Context.TestExitCode)
		}
		fmt.Fprintf(&b, "\n### Reviewer\n\n")
		if round.Review.Verdict != "" {
			fmt.Fprintf(&b, "**Verdict:** `%s`\n\n%s\n\n", round.Review.Verdict, round.Review.Summary)
		} else if round.Reviewer.RawOutput != "" {
			fmt.Fprintf(&b, "**Diagnostic Output / Error:**\n\n```\n%s\n```\n\n", round.Reviewer.RawOutput)
		}
		if len(round.Review.Findings) > 0 {
			fmt.Fprintf(&b, "#### Findings\n\n")
			for _, f := range round.Review.Findings {
				location := f.File
				if f.Line > 0 {
					location = fmt.Sprintf("%s:%d", location, f.Line)
				}
				fmt.Fprintf(&b, "- **%s** `%s`", f.ID, f.Severity)
				if location != "" {
					fmt.Fprintf(&b, " — `%s`", location)
				}
				fmt.Fprintf(&b, "\n  - Claim: %s\n", f.Claim)
				fmt.Fprintf(&b, "  - Evidence: %s\n", f.Evidence)
				fmt.Fprintf(&b, "  - Recommendation: %s\n", f.Recommendation)
			}
			fmt.Fprintln(&b)
		}
	}
	return b.String()
}
