package executil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Result struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	DurationMS int64
}

type SandboxPolicy struct {
	AllowedCommands []string
	AllowedPaths    []string
	DeniedPaths     []string
	MaxOutputBytes  int
	NetworkMode     string // "host" (default) or "none"
}

func (p SandboxPolicy) Validate(dir, command string) error {
	if len(p.AllowedCommands) > 0 {
		allowed := false
		for _, candidate := range p.AllowedCommands {
			if candidate == command || filepath.Base(candidate) == filepath.Base(command) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("sandbox command %q is not allowed", command)
		}
	}
	realDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, denied := range p.DeniedPaths {
		if matchesPath(realDir, denied) {
			return fmt.Errorf("sandbox working directory %q is denied", realDir)
		}
	}
	if len(p.AllowedPaths) > 0 {
		allowed := false
		for _, candidate := range p.AllowedPaths {
			if matchesPath(realDir, candidate) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("sandbox working directory %q is outside allowed paths", realDir)
		}
	}
	return nil
}

func matchesPath(value, pattern string) bool {
	pattern, _ = filepath.Abs(pattern)
	value = filepath.Clean(value)
	pattern = filepath.Clean(pattern)
	return value == pattern || strings.HasPrefix(value, pattern+string(filepath.Separator))
}

func RunWithPolicy(ctx context.Context, dir string, env map[string]string, stdin string, policy SandboxPolicy, name string, args ...string) (Result, error) {
	if err := policy.Validate(dir, name); err != nil {
		return Result{}, err
	}
	if policy.NetworkMode == "none" {
		wrapper, err := exec.LookPath("bwrap")
		if err != nil {
			return Result{}, fmt.Errorf("network-isolated execution requires bubblewrap (bwrap): %w", err)
		}
		// Bubblewrap creates a private network namespace while retaining a
		// read-only view of the image and a writable view only of the agent's
		// configured working directory. If the OS sandbox is unavailable,
		// execution fails closed instead of silently using host networking.
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return Result{}, fmt.Errorf("resolve network-isolated working directory: %w", err)
		}
		wrappedArgs := []string{
			"--die-with-parent", "--new-session", "--unshare-net",
			"--tmpfs", "/",
			"--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		}
		// Keep the runtime and configuration read-only, but do not expose
		// arbitrary application data (including mounted secret volumes).
		for _, path := range []string{"/usr", "/bin", "/sbin", "/lib", "/etc"} {
			if _, statErr := os.Stat(path); statErr == nil {
				wrappedArgs = append(wrappedArgs, "--ro-bind", path, path)
			}
		}
		wrappedArgs = append(wrappedArgs,
			"--dir", "/workspace", "--bind", absDir, "/workspace",
			"--chdir", "/workspace", "--", name,
		)
		wrappedArgs = append(wrappedArgs, args...)
		return Run(ctx, dir, env, stdin, wrapper, wrappedArgs...)
	}
	return Run(ctx, dir, env, stdin, name, args...)
}

func Run(ctx context.Context, dir string, env map[string]string, stdin string, name string, args ...string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), env)

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := Result{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   0,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if err == nil {
		return result, nil
	}

	var exitErr *exec.ExitError
	if ok := errorAs(err, &exitErr); ok {
		result.ExitCode = exitErr.ExitCode()
		return result, fmt.Errorf("%s exited with code %d", name, result.ExitCode)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.ExitCode = -1
	return result, err
}

func mergeEnv(base []string, extra map[string]string) []string {
	index := map[string]int{}
	out := append([]string{}, base...)
	for i, item := range out {
		if eq := strings.IndexByte(item, '='); eq > 0 {
			index[item[:eq]] = i
		}
	}
	for k, v := range extra {
		item := k + "=" + v
		if i, ok := index[k]; ok {
			out[i] = item
		} else {
			index[k] = len(out)
			out = append(out, item)
		}
	}
	return out
}

// small wrapper keeps this file dependency-free while still using errors.As.
func errorAs(err error, target any) bool {
	switch t := target.(type) {
	case **exec.ExitError:
		if e, ok := err.(*exec.ExitError); ok {
			*t = e
			return true
		}
	}
	return false
}
