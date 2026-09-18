package contextpack

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type Projector struct {
	repo string
	cfg  config.ContextConfig
}

func New(repo string, cfg config.ContextConfig) *Projector {
	return &Projector{repo: repo, cfg: cfg}
}

func (p *Projector) Capture(ctx context.Context, testCommand []string) (protocol.ContextSnapshot, error) {
	head, _ := p.git(ctx, "rev-parse", "HEAD")
	branch, _ := p.git(ctx, "branch", "--show-current")
	status, _ := p.git(ctx, "status", "--short")
	diffStat, _ := p.git(ctx, "diff", "--stat", "--no-ext-diff")

	contextLines := strconv.Itoa(p.cfg.DiffContextLines)
	diff, _ := p.git(ctx, "diff", "--no-ext-diff", "--no-color", "--unified="+contextLines)
	cached, _ := p.git(ctx, "diff", "--cached", "--no-ext-diff", "--no-color", "--unified="+contextLines)
	if strings.TrimSpace(cached) != "" {
		diff += "\n\n--- STAGED DIFF ---\n" + cached
	}

	untracked := []string{}
	if p.cfg.IncludeUntracked {
		out, _ := p.git(ctx, "ls-files", "--others", "--exclude-standard", "--exclude=.harnessmesh/**")
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				untracked = append(untracked, line)
			}
		}
	}

	truncated := false
	if len(diff) > p.cfg.MaxDiffChars {
		diff = middleOut(diff, p.cfg.MaxDiffChars)
		truncated = true
	}

	snap := protocol.ContextSnapshot{
		RepoRoot:       p.repo,
		Head:           strings.TrimSpace(head),
		Branch:         strings.TrimSpace(branch),
		Status:         strings.TrimSpace(status),
		DiffStat:       strings.TrimSpace(diffStat),
		Diff:           diff,
		UntrackedFiles: untracked,
		Truncated:      truncated,
	}

	if len(testCommand) > 0 {
		exit, output, err := runTest(ctx, p.repo, testCommand)
		snap.TestCommand = append([]string{}, testCommand...)
		snap.TestExitCode = &exit
		if len(output) > p.cfg.MaxTestOutputChars {
			output = middleOut(output, p.cfg.MaxTestOutputChars)
			snap.Truncated = true
		}
		snap.TestOutput = output
		if err != nil && ctx.Err() != nil {
			return snap, ctx.Err()
		}
	}
	return snap, nil
}

func (p *Projector) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", p.repo}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runTest(ctx context.Context, repo string, argv []string) (int, string, error) {
	if len(argv) == 0 {
		return 0, "", nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out), nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), string(out), nil
	}
	return -1, string(out), err
}

func middleOut(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	marker := "\n...<HarnessMesh middle truncation>...\n"
	keep := max - len(marker)
	if keep < 2 {
		return s[:max]
	}
	left := keep / 2
	right := keep - left
	return s[:left] + marker + s[len(s)-right:]
}

// ReadFileForContext is intentionally conservative and used by future adapters.
func ReadFileForContext(root, relative string, max int) (string, error) {
	full := filepath.Clean(filepath.Join(root, relative))
	rootClean := filepath.Clean(root) + string(os.PathSeparator)
	if full != filepath.Clean(root) && !strings.HasPrefix(full, rootClean) {
		return "", fmt.Errorf("path escapes repository: %s", relative)
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	text := string(raw)
	if max > 0 && len(text) > max {
		text = middleOut(text, max)
	}
	return text, nil
}
