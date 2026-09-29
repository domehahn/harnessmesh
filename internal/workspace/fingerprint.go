package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/domehahn/harnessmesh/internal/executil"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

// PathHash computes the SHA-256 hash of the file content at filePath.
func PathHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeTreeHash calculates a deterministic fingerprint of the working tree state.
// It first attempts isolated Git plumbing using GIT_INDEX_FILE without touching .git/index.
// If Git plumbing is unavailable or fails (e.g. read-only .git, non-git dir), it falls back
// to a deterministic SHA-256 tree hash across all unignored repository files.
func ComputeTreeHash(ctx context.Context, repoPath string) (string, error) {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return "", err
	}

	// Attempt Git isolated tree hash
	gitTree, err := computeGitTreeHash(ctx, absPath)
	if err == nil && gitTree != "" {
		return gitTree, nil
	}

	// Fallback to deterministic SHA-256 tree
	return computeFallbackTreeHash(ctx, absPath)
}

func computeGitTreeHash(ctx context.Context, repoPath string) (string, error) {
	// Verify git repo
	revParse, err := executil.Run(ctx, repoPath, nil, "", "git", "rev-parse", "--git-dir")
	if err != nil || revParse.ExitCode != 0 {
		return "", fmt.Errorf("not a git repository")
	}

	tmpFile, err := os.CreateTemp("", "harnessmesh_idx_*")
	if err != nil {
		return "", err
	}
	tmpIndexPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpIndexPath)

	env := map[string]string{
		"GIT_INDEX_FILE": tmpIndexPath,
	}

	// If HEAD exists, populate synthetic index
	headCheck, _ := executil.Run(ctx, repoPath, env, "", "git", "rev-parse", "--verify", "HEAD")
	if headCheck.ExitCode == 0 {
		_, _ = executil.Run(ctx, repoPath, env, "", "git", "read-tree", "HEAD")
	}

	// Add all changes to synthetic index
	addRes, err := executil.Run(ctx, repoPath, env, "", "git", "add", "-A")
	if err != nil || addRes.ExitCode != 0 {
		return "", fmt.Errorf("git add failed in synthetic index: %s", addRes.Stderr)
	}

	// Write tree object
	writeRes, err := executil.Run(ctx, repoPath, env, "", "git", "write-tree")
	if err != nil || writeRes.ExitCode != 0 {
		return "", fmt.Errorf("git write-tree failed: %s", writeRes.Stderr)
	}

	treeHash := strings.TrimSpace(writeRes.Stdout)
	if len(treeHash) == 0 {
		return "", fmt.Errorf("empty tree hash returned by git write-tree")
	}
	return treeHash, nil
}

func computeFallbackTreeHash(ctx context.Context, repoPath string) (string, error) {
	var files []string

	// Check if git ls-files can give us unignored files
	lsRes, err := executil.Run(ctx, repoPath, nil, "", "git", "ls-files", "--cached", "--others", "--exclude-standard")
	if err == nil && lsRes.ExitCode == 0 {
		lines := strings.Split(lsRes.Stdout, "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				files = append(files, trimmed)
			}
		}
	} else {
		// Pure filesystem walk
		err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == ".agent" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, rErr := filepath.Rel(repoPath, path)
			if rErr != nil {
				return rErr
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return "", err
		}
	}

	sort.Strings(files)

	h := sha256.New()
	for _, rel := range files {
		fullPath := filepath.Join(repoPath, filepath.FromSlash(rel))
		fHash, err := PathHash(fullPath)
		if err != nil {
			// If file was deleted or cannot be read, skip
			continue
		}
		entry := fmt.Sprintf("%s:%s\n", rel, fHash)
		h.Write([]byte(entry))
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeDiff detects changed paths relative to baseTree or HEAD.
// If baseTree is empty, it detects uncommitted working tree changes against HEAD.
func ComputeDiff(ctx context.Context, repoPath string, baseTree string, targetTree string) ([]protocol.ChangePath, error) {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, err
	}

	// If both baseTree and targetTree are provided Git trees
	if baseTree != "" && targetTree != "" && baseTree != targetTree {
		diffRes, err := executil.Run(ctx, absPath, nil, "", "git", "diff-tree", "-r", "--name-status", baseTree, targetTree)
		if err == nil && diffRes.ExitCode == 0 {
			return parseNameStatusOutput(diffRes.Stdout, absPath), nil
		}
	}

	// Working tree diff against baseTree or HEAD
	var args []string
	if baseTree != "" {
		args = []string{"diff", "--name-status", baseTree}
	} else {
		args = []string{"status", "--porcelain=v1"}
	}

	runRes, err := executil.Run(ctx, absPath, nil, "", "git", args...)
	if err == nil && runRes.ExitCode == 0 {
		if baseTree != "" {
			return parseNameStatusOutput(runRes.Stdout, absPath), nil
		}
		return parsePorcelainStatusOutput(runRes.Stdout, absPath), nil
	}

	// Pure filesystem fallback if not a git repository
	var fallbackPaths []protocol.ChangePath
	_ = filepath.WalkDir(absPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == ".agent") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rErr := filepath.Rel(absPath, path)
		if rErr != nil {
			return nil
		}
		newHash, _ := PathHash(path)
		fallbackPaths = append(fallbackPaths, protocol.ChangePath{
			Path:             filepath.ToSlash(rel),
			ChangeType:       "modified",
			ContentHashAfter: newHash,
		})
		return nil
	})
	if len(fallbackPaths) > 0 {
		return fallbackPaths, nil
	}

	return nil, fmt.Errorf("unable to compute diff: %v", err)
}

func parseNameStatusOutput(output string, repoPath string) []protocol.ChangePath {
	var paths []protocol.ChangePath
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		p := parts[1]
		changeType := "modified"
		if strings.HasPrefix(status, "A") {
			changeType = "added"
		} else if strings.HasPrefix(status, "D") {
			changeType = "deleted"
		} else if strings.HasPrefix(status, "R") {
			changeType = "renamed"
			if len(parts) >= 3 {
				p = parts[2]
			}
		}

		newHash := ""
		if changeType != "deleted" {
			newHash, _ = PathHash(filepath.Join(repoPath, p))
		}

		paths = append(paths, protocol.ChangePath{
			Path:             filepath.ToSlash(p),
			ChangeType:       changeType,
			ContentHashAfter: newHash,
		})
	}
	return paths
}

func parsePorcelainStatusOutput(output string, repoPath string) []protocol.ChangePath {
	var paths []protocol.ChangePath
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		code := line[0:2]
		pathPart := strings.TrimSpace(line[3:])

		changeType := "modified"
		if strings.Contains(code, "?") {
			changeType = "added"
		} else if strings.Contains(code, "A") {
			changeType = "added"
		} else if strings.Contains(code, "D") {
			changeType = "deleted"
		} else if strings.Contains(code, "R") {
			changeType = "renamed"
			if arrowIdx := strings.Index(pathPart, "->"); arrowIdx != -1 {
				pathPart = strings.TrimSpace(pathPart[arrowIdx+2:])
			}
		}

		newHash := ""
		if changeType != "deleted" {
			newHash, _ = PathHash(filepath.Join(repoPath, pathPart))
		}

		paths = append(paths, protocol.ChangePath{
			Path:             filepath.ToSlash(pathPart),
			ChangeType:       changeType,
			ContentHashAfter: newHash,
		})
	}
	return paths
}
