package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/domehahn/harnessmesh/internal/executil"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type Workspace struct {
	Root                 string               `json:"root"`
	RepositoryIdentity   string               `json:"repository_identity"`
	Branch               string               `json:"branch"`
	Head                 string               `json:"head"`
	WriterParticipant    string               `json:"writer_participant"`
	ReadOnlyParticipants []string             `json:"read_only_participants"`
	PathPolicy           *protocol.PathPolicy `json:"path_policy,omitempty"`
}

// CanonicalizeWorkspace resolves and validates the workspace path, detects git metadata,
// and enforces the single-writer invariant.
func CanonicalizeWorkspace(ctx context.Context, repoPath string, writer string, readOnly []string, policy *protocol.PathPolicy) (*Workspace, error) {
	if strings.TrimSpace(repoPath) == "" {
		repoPath = "."
	}

	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, &protocol.WorkspaceInvalidError{Path: repoPath, Reason: fmt.Sprintf("failed to resolve absolute path: %v", err)}
	}

	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, &protocol.WorkspaceInvalidError{Path: repoPath, Reason: fmt.Sprintf("failed to resolve symlinks: %v", err)}
	}

	fi, err := os.Stat(realPath)
	if err != nil {
		return nil, &protocol.WorkspaceInvalidError{Path: realPath, Reason: fmt.Sprintf("workspace path does not exist: %v", err)}
	}
	if !fi.IsDir() {
		return nil, &protocol.WorkspaceInvalidError{Path: realPath, Reason: "workspace path is not a directory"}
	}

	// Git metadata discovery
	branch := "unknown"
	head := "unknown"
	repoID := filepath.Base(realPath)

	runToplevel, err := executil.Run(ctx, realPath, nil, "", "git", "rev-parse", "--show-toplevel")
	if err == nil && runToplevel.ExitCode == 0 {
		top := strings.TrimSpace(runToplevel.Stdout)
		if top != "" {
			realTop, errTop := filepath.EvalSymlinks(top)
			if errTop == nil {
				realPath = realTop
				repoID = filepath.Base(realTop)
			}
		}
	}

	runBranch, err := executil.Run(ctx, realPath, nil, "", "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && runBranch.ExitCode == 0 {
		b := strings.TrimSpace(runBranch.Stdout)
		if b != "" {
			branch = b
		}
	}

	runHead, err := executil.Run(ctx, realPath, nil, "", "git", "rev-parse", "HEAD")
	if err == nil && runHead.ExitCode == 0 {
		h := strings.TrimSpace(runHead.Stdout)
		if h != "" {
			head = h
		}
	}

	ws := &Workspace{
		Root:                 realPath,
		RepositoryIdentity:   repoID,
		Branch:               branch,
		Head:                 head,
		WriterParticipant:    writer,
		ReadOnlyParticipants: readOnly,
		PathPolicy:           policy,
	}

	return ws, nil
}

// CheckPath verifies that targetPath is inside the workspace root and complies with path policies.
func (w *Workspace) CheckPath(targetPath string) error {
	cleaned := filepath.Clean(targetPath)
	if filepath.IsAbs(cleaned) {
		rel, err := filepath.Rel(w.Root, cleaned)
		if err != nil || strings.HasPrefix(rel, "..") {
			return &protocol.ContextRejectedError{Path: targetPath, Reason: "path traverses outside workspace root"}
		}
		cleaned = rel
	} else if strings.HasPrefix(cleaned, "..") {
		return &protocol.ContextRejectedError{Path: targetPath, Reason: "relative path traverses outside workspace root"}
	}

	fullPath := filepath.Join(w.Root, cleaned)
	realPath, err := filepath.EvalSymlinks(fullPath)
	if err == nil {
		rel, err := filepath.Rel(w.Root, realPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return &protocol.ContextRejectedError{Path: targetPath, Reason: "symlink escapes workspace root"}
		}
	}

	if w.PathPolicy != nil {
		slashPath := filepath.ToSlash(cleaned)
		for _, denied := range w.PathPolicy.DeniedPaths {
			if matched, _ := filepath.Match(denied, slashPath); matched || strings.HasPrefix(slashPath, strings.TrimSuffix(denied, "*")) {
				return &protocol.PolicyDeniedError{Action: "read_path", Reason: fmt.Sprintf("path %q is denied by workspace policy %q", targetPath, denied)}
			}
		}
		if len(w.PathPolicy.AllowedPaths) > 0 {
			allowed := false
			for _, allow := range w.PathPolicy.AllowedPaths {
				if matched, _ := filepath.Match(allow, slashPath); matched || strings.HasPrefix(slashPath, strings.TrimSuffix(allow, "*")) {
					allowed = true
					break
				}
			}
			if !allowed {
				return &protocol.PolicyDeniedError{Action: "read_path", Reason: fmt.Sprintf("path %q is not in allowed workspace paths", targetPath)}
			}
		}
	}

	return nil
}
