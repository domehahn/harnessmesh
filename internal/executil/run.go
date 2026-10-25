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
	PassEnv         []string
}

// DefaultSafeEnvKeys defines host environment variables permitted to be inherited
// by sandboxed agent processes.
var DefaultSafeEnvKeys = map[string]bool{
	"PATH":            true,
	"HOME":            true,
	"USER":            true,
	"LOGNAME":         true,
	"SHELL":           true,
	"TMPDIR":          true,
	"TMP":             true,
	"TEMP":            true,
	"LANG":            true,
	"LC_ALL":          true,
	"LC_CTYPE":        true,
	"LC_MESSAGES":     true,
	"TERM":            true,
	"TZ":              true,
	"GOROOT":          true,
	"GOPATH":          true,
	"GOCACHE":         true,
	"GOPROXY":         true,
	"GOPRIVATE":       true,
	"GONOPROXY":       true,
	"GONOSUMDB":       true,
	"XDG_CACHE_HOME":  true,
	"XDG_CONFIG_HOME": true,
	"XDG_DATA_HOME":   true,
	"SSL_CERT_FILE":   true,
	"SSL_CERT_DIR":    true,
	"SYSTEMROOT":      true,
	"WINDIR":          true,
}

func (p SandboxPolicy) Validate(dir, command string) error {
	if len(p.AllowedCommands) > 0 {
		allowed := false
		hasSep := strings.Contains(command, string(filepath.Separator))
		for _, candidate := range p.AllowedCommands {
			if !hasSep {
				if candidate == command || filepath.Base(candidate) == command {
					allowed = true
					break
				}
			} else {
				if filepath.Clean(candidate) == filepath.Clean(command) {
					allowed = true
					break
				}
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
	if evaluated, evalErr := filepath.EvalSymlinks(realDir); evalErr == nil {
		realDir = evaluated
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
	cleanPattern, _ := filepath.Abs(pattern)
	if evalPattern, err := filepath.EvalSymlinks(cleanPattern); err == nil {
		cleanPattern = evalPattern
	}
	value = filepath.Clean(value)
	cleanPattern = filepath.Clean(cleanPattern)
	return value == cleanPattern || strings.HasPrefix(value, cleanPattern+string(filepath.Separator))
}

// SanitizeHostEnv filters the host environment to only include safe standard variables
// and any explicitly requested variables in extraAllowed, strictly stripping control plane
// secrets (e.g. HARNESSMESH_*, VAULT_*).
func SanitizeHostEnv(extraAllowed []string) []string {
	allowed := make(map[string]bool, len(DefaultSafeEnvKeys)+len(extraAllowed))
	for k := range DefaultSafeEnvKeys {
		allowed[k] = true
	}
	for _, k := range extraAllowed {
		allowed[k] = true
	}

	var res []string
	for _, item := range os.Environ() {
		eq := strings.IndexByte(item, '=')
		if eq <= 0 {
			continue
		}
		key := item[:eq]
		if isForbiddenControlPlaneKey(key) {
			continue
		}
		if allowed[key] {
			res = append(res, item)
		}
	}
	return res
}

// StripControlPlaneSecrets removes known control plane secrets from an environment slice.
func StripControlPlaneSecrets(base []string) []string {
	var res []string
	for _, item := range base {
		eq := strings.IndexByte(item, '=')
		if eq <= 0 {
			continue
		}
		key := item[:eq]
		if isForbiddenControlPlaneKey(key) {
			continue
		}
		res = append(res, item)
	}
	return res
}

func isForbiddenControlPlaneKey(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "HARNESSMESH_") || strings.HasPrefix(upper, "VAULT_") {
		return true
	}
	if upper == "DATABASE_URL" || upper == "PGPASSWORD" || upper == "POSTGRES_PASSWORD" {
		return true
	}
	return false
}

func RunWithPolicy(ctx context.Context, dir string, env map[string]string, stdin string, policy SandboxPolicy, name string, args ...string) (Result, error) {
	if err := policy.Validate(dir, name); err != nil {
		return Result{}, err
	}
	baseEnv := SanitizeHostEnv(policy.PassEnv)
	if policy.NetworkMode == "none" {
		wrapper, err := exec.LookPath("bwrap")
		if err != nil {
			return Result{}, fmt.Errorf("network-isolated execution requires bubblewrap (bwrap): %w", err)
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return Result{}, fmt.Errorf("resolve network-isolated working directory: %w", err)
		}
		if evaluated, evalErr := filepath.EvalSymlinks(absDir); evalErr == nil {
			absDir = evaluated
		}
		wrappedArgs := []string{
			"--die-with-parent", "--new-session", "--unshare-net",
			"--tmpfs", "/",
			"--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		}
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
		return runWithBaseEnv(ctx, dir, baseEnv, env, stdin, wrapper, wrappedArgs...)
	}
	return runWithBaseEnv(ctx, dir, baseEnv, env, stdin, name, args...)
}

func Run(ctx context.Context, dir string, env map[string]string, stdin string, name string, args ...string) (Result, error) {
	return runWithBaseEnv(ctx, dir, StripControlPlaneSecrets(os.Environ()), env, stdin, name, args...)
}

func runWithBaseEnv(ctx context.Context, dir string, baseEnv []string, env map[string]string, stdin string, name string, args ...string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(baseEnv, env)

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
