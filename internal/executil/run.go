package executil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Result struct {
	Stdout     string
	Stderr     string
	ExitCode   int
	DurationMS int64
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
