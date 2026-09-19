package contextpack

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

var defaultSecretPatterns = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"id_rsa",
	"id_rsa.*",
	"id_ed25519",
	"id_ed25519.*",
	"secrets/**",
	"**/secrets/**",
	"credentials/**",
	"**/credentials/**",
	"terraform.tfstate",
	"*.tfstate",
	"*.tfstate.backup",
	".git/**",
	"**/.git/**",
	".harnessmesh/**",
	"**/.harnessmesh/**",
}

var (
	privateKeyRegex     = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	openAITokenRegex    = regexp.MustCompile(`\b(sk-[a-zA-Z0-9_\-]{20,})\b`)
	anthropicTokenRegex = regexp.MustCompile(`\b(sk-ant-[a-zA-Z0-9_\-]{20,})\b`)
	gitHubTokenRegex    = regexp.MustCompile(`\b(gh[pousr]_[a-zA-Z0-9]{20,}|github_pat_[a-zA-Z0-9_]{22,})\b`)
	awsKeyRegex         = regexp.MustCompile(`\b((?:AKIA|ASIA|AROA)[0-9A-Z]{16})\b`)
	bearerRegex         = regexp.MustCompile(`(?i)\b(Bearer\s+)[a-zA-Z0-9_\-\.]{20,}\b`)
	configSecretRegex   = regexp.MustCompile(`(?i)\b((?:api[_-]?key|secret|password|passwd|auth[_-]?token)\s*[:=]\s*["'])([^"'\r\n]{8,})(["'])`)
)

// RedactSecrets scans string content and replaces sensitive credentials with safe placeholders.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = privateKeyRegex.ReplaceAllString(s, "[REDACTED_PRIVATE_KEY]")
	s = openAITokenRegex.ReplaceAllString(s, "[REDACTED_API_KEY]")
	s = anthropicTokenRegex.ReplaceAllString(s, "[REDACTED_API_KEY]")
	s = gitHubTokenRegex.ReplaceAllString(s, "[REDACTED_TOKEN]")
	s = awsKeyRegex.ReplaceAllString(s, "[REDACTED_AWS_KEY]")
	s = bearerRegex.ReplaceAllString(s, "${1}[REDACTED_BEARER_TOKEN]")
	s = configSecretRegex.ReplaceAllString(s, "${1}[REDACTED_SECRET]${3}")
	return s
}

// RedactSecrets wraps the package function as a method on Projector.
func (p *Projector) RedactSecrets(s string) string {
	return RedactSecrets(s)
}

type Projector struct {
	repo string
	cfg  config.ContextConfig
}

func New(repo string, cfg config.ContextConfig) *Projector {
	return &Projector{repo: repo, cfg: cfg}
}

type ProjectRequest struct {
	Task             string
	Plan             string
	Question         string
	Scope            []string
	IncludeDiff      bool
	IncludeTests     bool
	IncludeGitStatus bool
	TestCommand      []string
	AllowedPaths     []string
	DeniedPaths      []string
	Findings         []protocol.FindingPayload
	Evidence         []protocol.EvidencePayload
}

type ProjectedContext struct {
	Task          string                     `json:"task,omitempty"`
	Plan          string                     `json:"plan,omitempty"`
	Question      string                     `json:"question,omitempty"`
	RepoRoot      string                     `json:"repo_root"`
	Head          string                     `json:"head"`
	Branch        string                     `json:"branch"`
	Status        string                     `json:"status,omitempty"`
	ChangedFiles  []string                   `json:"changed_files,omitempty"`
	DiffStat      string                     `json:"diff_stat,omitempty"`
	Diff          string                     `json:"diff,omitempty"`
	Files         map[string]string          `json:"files,omitempty"`
	TestCommand   []string                   `json:"test_command,omitempty"`
	TestExitCode  *int                       `json:"test_exit_code,omitempty"`
	TestOutput    string                     `json:"test_output,omitempty"`
	Findings      []protocol.FindingPayload  `json:"findings,omitempty"`
	Evidence      []protocol.EvidencePayload `json:"evidence,omitempty"`
	Truncated     bool                       `json:"truncated"`
	OriginalChars int                        `json:"original_chars"`
	IncludedChars int                        `json:"included_chars"`
}

// Capture implements the v0.1 interface for backward compatibility
func (p *Projector) Capture(ctx context.Context, testCommand []string) (protocol.ContextSnapshot, error) {
	proj, err := p.Project(ctx, ProjectRequest{
		IncludeDiff:      true,
		IncludeTests:     len(testCommand) > 0,
		IncludeGitStatus: true,
		TestCommand:      testCommand,
	})
	if err != nil {
		return protocol.ContextSnapshot{}, err
	}

	snap := protocol.ContextSnapshot{
		RepoRoot:      proj.RepoRoot,
		Head:          proj.Head,
		Branch:        proj.Branch,
		Status:        proj.Status,
		DiffStat:      proj.DiffStat,
		Diff:          proj.Diff,
		TestCommand:   proj.TestCommand,
		TestExitCode:  proj.TestExitCode,
		TestOutput:    proj.TestOutput,
		Truncated:     proj.Truncated,
		OriginalChars: proj.OriginalChars,
		IncludedChars: proj.IncludedChars,
	}
	return snap, nil
}

func (p *Projector) Project(ctx context.Context, req ProjectRequest) (*ProjectedContext, error) {
	head, _ := p.git(ctx, "rev-parse", "HEAD")
	branch, _ := p.git(ctx, "branch", "--show-current")

	allowed := append([]string{}, p.cfg.AllowedPaths...)
	allowed = append(allowed, req.AllowedPaths...)

	denied := append([]string{}, p.cfg.DeniedPaths...)
	denied = append(denied, req.DeniedPaths...)

	proj := &ProjectedContext{
		Task:         RedactSecrets(req.Task),
		Plan:         RedactSecrets(req.Plan),
		Question:     RedactSecrets(req.Question),
		RepoRoot:     p.repo,
		Head:         strings.TrimSpace(head),
		Branch:       strings.TrimSpace(branch),
		Files:        make(map[string]string),
		Findings:     req.Findings,
		Evidence:     req.Evidence,
		ChangedFiles: []string{},
	}

	totalOriginalChars := len(req.Task) + len(req.Plan) + len(req.Question)

	if req.IncludeGitStatus {
		status, _ := p.git(ctx, "status", "--short")
		filteredStatus, changed := filterGitStatus(status, allowed, denied)
		proj.Status = strings.TrimSpace(filteredStatus)
		proj.ChangedFiles = changed
		totalOriginalChars += len(status)
	}

	if req.IncludeDiff {
		diffStat, _ := p.git(ctx, "diff", "--stat", "--no-ext-diff")
		proj.DiffStat = strings.TrimSpace(filterDiffStat(diffStat, allowed, denied))

		contextLines := strconv.Itoa(p.cfg.DiffContextLines)
		diff, _ := p.git(ctx, "diff", "--no-ext-diff", "--no-color", "--unified="+contextLines)
		cached, _ := p.git(ctx, "diff", "--cached", "--no-ext-diff", "--no-color", "--unified="+contextLines)
		combinedDiff := diff
		if strings.TrimSpace(cached) != "" {
			combinedDiff += "\n\n--- STAGED DIFF ---\n" + cached
		}

		filteredDiff := filterDiffContent(combinedDiff, allowed, denied)
		totalOriginalChars += len(combinedDiff)

		maxDiff := p.cfg.MaxDiffChars
		if maxDiff <= 0 {
			maxDiff = 50000
		}
		if len(filteredDiff) > maxDiff {
			filteredDiff = MiddleOut(filteredDiff, maxDiff)
			proj.Truncated = true
		}
		proj.Diff = RedactSecrets(filteredDiff)
		proj.DiffStat = RedactSecrets(proj.DiffStat)
	}

	// Read selected files from Scope
	maxFiles := p.cfg.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 20
	}
	maxFileChars := p.cfg.MaxFileChars
	if maxFileChars <= 0 {
		maxFileChars = 20000
	}

	filesLoaded := 0
	for _, sc := range req.Scope {
		if filesLoaded >= maxFiles {
			break
		}
		sc = strings.TrimSpace(sc)
		if sc == "" {
			continue
		}

		// Security: Validate path
		if ok, reason := IsPathAllowed(sc, allowed, denied); !ok {
			return nil, &protocol.ContextRejectedError{Path: sc, Reason: reason}
		}

		fullPath := filepath.Clean(filepath.Join(p.repo, sc))
		if err := validatePathInsideRepo(p.repo, fullPath); err != nil {
			return nil, &protocol.ContextRejectedError{Path: sc, Reason: err.Error()}
		}

		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}

		content, err := ReadFileForContext(p.repo, sc, maxFileChars)
		if err != nil {
			return nil, &protocol.ContextRejectedError{Path: sc, Reason: err.Error()}
		}
		proj.Files[sc] = RedactSecrets(content)
		totalOriginalChars += int(info.Size())
		filesLoaded++
	}

	if req.IncludeTests && len(req.TestCommand) > 0 {
		exit, output, err := runTest(ctx, p.repo, req.TestCommand)
		proj.TestCommand = append([]string{}, req.TestCommand...)
		proj.TestExitCode = &exit
		totalOriginalChars += len(output)

		maxTestChars := p.cfg.MaxTestOutputChars
		if maxTestChars <= 0 {
			maxTestChars = 20000
		}
		if len(output) > maxTestChars {
			output = MiddleOut(output, maxTestChars)
			proj.Truncated = true
		}
		proj.TestOutput = RedactSecrets(output)
		if err != nil && ctx.Err() != nil {
			return proj, ctx.Err()
		}
	}

	maxContext := p.cfg.MaxContextChars
	if maxContext <= 0 {
		maxContext = 100000
	}

	calcIncludedChars := func() int {
		total := len(proj.Task) + len(proj.Plan) + len(proj.Question) + len(proj.Status) +
			len(proj.DiffStat) + len(proj.Diff) + len(proj.TestOutput)
		for _, content := range proj.Files {
			total += len(content)
		}
		return total
	}

	includedChars := calcIncludedChars()
	if includedChars > maxContext {
		proj.Truncated = true

		// 1. Truncate / drop files
		if includedChars > maxContext && len(proj.Files) > 0 {
			for k, content := range proj.Files {
				if includedChars <= maxContext {
					break
				}
				neededCut := includedChars - maxContext
				if len(content) <= neededCut {
					delete(proj.Files, k)
				} else {
					proj.Files[k] = MiddleOut(content, len(content)-neededCut)
				}
				includedChars = calcIncludedChars()
			}
		}

		// 2. Truncate diff if still over limit
		if includedChars > maxContext && len(proj.Diff) > 0 {
			neededCut := includedChars - maxContext
			if len(proj.Diff) <= neededCut {
				proj.Diff = ""
			} else {
				proj.Diff = MiddleOut(proj.Diff, len(proj.Diff)-neededCut)
			}
			includedChars = calcIncludedChars()
		}

		// 3. Truncate test output if still over limit
		if includedChars > maxContext && len(proj.TestOutput) > 0 {
			neededCut := includedChars - maxContext
			if len(proj.TestOutput) <= neededCut {
				proj.TestOutput = ""
			} else {
				proj.TestOutput = MiddleOut(proj.TestOutput, len(proj.TestOutput)-neededCut)
			}
			includedChars = calcIncludedChars()
		}

		// 4. Truncate DiffStat if still over limit
		if includedChars > maxContext && len(proj.DiffStat) > 0 {
			neededCut := includedChars - maxContext
			if len(proj.DiffStat) <= neededCut {
				proj.DiffStat = ""
			} else {
				proj.DiffStat = MiddleOut(proj.DiffStat, len(proj.DiffStat)-neededCut)
			}
			includedChars = calcIncludedChars()
		}

		// 5. Truncate Plan if still over limit
		if includedChars > maxContext && len(proj.Plan) > 0 {
			neededCut := includedChars - maxContext
			if len(proj.Plan) <= neededCut {
				proj.Plan = ""
			} else {
				proj.Plan = MiddleOut(proj.Plan, len(proj.Plan)-neededCut)
			}
			includedChars = calcIncludedChars()
		}

		// 6. Truncate Task if still over limit
		if includedChars > maxContext && len(proj.Task) > 0 {
			neededCut := includedChars - maxContext
			if len(proj.Task) <= neededCut {
				proj.Task = ""
			} else {
				proj.Task = MiddleOut(proj.Task, len(proj.Task)-neededCut)
			}
			includedChars = calcIncludedChars()
		}

		// 7. Truncate Question if still over limit
		if includedChars > maxContext && len(proj.Question) > 0 {
			neededCut := includedChars - maxContext
			if len(proj.Question) <= neededCut {
				proj.Question = ""
			} else {
				proj.Question = MiddleOut(proj.Question, len(proj.Question)-neededCut)
			}
			includedChars = calcIncludedChars()
		}

		// 8. Truncate Status if still over limit
		if includedChars > maxContext && len(proj.Status) > 0 {
			neededCut := includedChars - maxContext
			if len(proj.Status) <= neededCut {
				proj.Status = ""
			} else {
				proj.Status = proj.Status[:len(proj.Status)-neededCut]
			}
			includedChars = calcIncludedChars()
		}
	}

	proj.OriginalChars = totalOriginalChars
	proj.IncludedChars = includedChars

	return proj, nil
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

func MiddleOut(s string, max int) string {
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

func middleOut(s string, max int) string {
	return MiddleOut(s, max)
}

// IsPathAllowed checks whether a relative or cleaned path is permitted
func IsPathAllowed(relPath string, allowedPaths, deniedPaths []string) (bool, string) {
	cleaned := filepath.Clean(filepath.ToSlash(relPath))
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return false, "path traversal (..) detected"
	}
	if filepath.IsAbs(cleaned) {
		return false, "absolute path not permitted"
	}

	// 1. Check default secrets
	for _, pattern := range defaultSecretPatterns {
		if matchPattern(pattern, cleaned) {
			return false, fmt.Sprintf("path matches default secret pattern %q", pattern)
		}
	}

	// 2. Check explicitly denied paths
	for _, pattern := range deniedPaths {
		if matchPattern(pattern, cleaned) {
			return false, fmt.Sprintf("path matches denied path pattern %q", pattern)
		}
	}

	// 3. If allowed paths are configured, must match at least one
	if len(allowedPaths) > 0 {
		matched := false
		for _, pattern := range allowedPaths {
			if matchPattern(pattern, cleaned) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "path not in allowed_paths policy"
		}
	}

	return true, ""
}

func matchPattern(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// Exact match
	if pattern == path {
		return true
	}

	// Directory wildcard prefix or suffix
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		if strings.HasSuffix(pattern, "/**") {
			sub := strings.TrimSuffix(suffix, "/**")
			if strings.Contains(path, "/"+sub+"/") || strings.HasPrefix(path, sub+"/") {
				return true
			}
		}
		if path == suffix || strings.HasSuffix(path, "/"+suffix) {
			return true
		}
		// Glob on base
		if matched, _ := filepath.Match(suffix, filepath.Base(path)); matched {
			return true
		}
	}

	// Check basename match for patterns like *.key, *.pem, .env
	base := filepath.Base(path)
	if matched, _ := filepath.Match(pattern, base); matched {
		return true
	}
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}
	return false
}

func validatePathInsideRepo(root, full string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootClean := filepath.Clean(rootAbs)
	realRoot, err := filepath.EvalSymlinks(rootClean)
	if err == nil {
		rootClean = realRoot
	}

	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return err
	}
	fullClean := filepath.Clean(fullAbs)

	// Check logical containment
	relLogical, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil || strings.HasPrefix(relLogical, "..") {
		return fmt.Errorf("path escapes repository root: %s", full)
	}

	// Symlink escape check
	realPath, err := filepath.EvalSymlinks(fullClean)
	if err == nil {
		relReal, errRel := filepath.Rel(rootClean, realPath)
		if errRel != nil || strings.HasPrefix(relReal, "..") {
			return fmt.Errorf("symlink target escapes repository root: %s -> %s", full, realPath)
		}
	}

	return nil
}

func ReadFileForContext(root, relative string, max int) (string, error) {
	if ok, reason := IsPathAllowed(relative, nil, nil); !ok {
		return "", fmt.Errorf("path rejected: %s", reason)
	}

	full := filepath.Clean(filepath.Join(root, relative))
	if err := validatePathInsideRepo(root, full); err != nil {
		return "", err
	}

	raw, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	text := string(raw)
	if max > 0 && len(text) > max {
		text = MiddleOut(text, max)
	}
	return text, nil
}

func filterGitStatus(status string, allowed, denied []string) (string, []string) {
	var kept []string
	var changed []string
	for _, line := range strings.Split(status, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 3 {
			continue
		}
		path := strings.TrimSpace(trimmed[2:])
		if strings.Contains(path, " -> ") {
			parts := strings.Split(path, " -> ")
			path = strings.TrimSpace(parts[len(parts)-1])
		}
		if ok, _ := IsPathAllowed(path, allowed, denied); ok {
			kept = append(kept, line)
			changed = append(changed, path)
		}
	}
	return strings.Join(kept, "\n"), changed
}

func filterDiffStat(diffStat string, allowed, denied []string) string {
	var kept []string
	for _, line := range strings.Split(diffStat, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "|", 2)
		if len(parts) == 2 {
			path := strings.TrimSpace(parts[0])
			if ok, _ := IsPathAllowed(path, allowed, denied); !ok {
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func filterDiffContent(diff string, allowed, denied []string) string {
	var kept []string
	chunks := strings.Split(diff, "diff --git ")
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		lines := strings.SplitN(chunk, "\n", 2)
		firstLine := lines[0]
		// e.g. "a/path/to/file b/path/to/file"
		words := strings.Fields(firstLine)
		includeChunk := true
		for _, w := range words {
			p := strings.TrimPrefix(strings.TrimPrefix(w, "a/"), "b/")
			if ok, _ := IsPathAllowed(p, allowed, denied); !ok {
				includeChunk = false
				break
			}
		}
		if includeChunk {
			kept = append(kept, "diff --git "+chunk)
		}
	}
	return strings.Join(kept, "")
}
