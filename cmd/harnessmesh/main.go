package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/report"
	"github.com/domehahn/harnessmesh/internal/workflow"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "collaborate":
		err = collaborate(os.Args[2:])
	case "doctor":
		err = doctor(os.Args[2:])
	case "print-config":
		err = printConfig(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `HarnessMesh - collaboration fabric for coding-agent harnesses

Usage:
  harnessmesh collaborate --task "..." [options]
  harnessmesh doctor [--config harnessmesh.json]
  harnessmesh print-config [--config harnessmesh.json]
  harnessmesh version

Collaborate options:
  --task string            Task for the executor
  --task-file path         Read task from file instead
  --repo path              Repository path (default ".")
  --config path            Config path (default "harnessmesh.json")
  --executor name          Override configured executor agent
  --reviewer name          Override configured reviewer agent
  --max-rounds n           Override max review rounds
  --dry-run                Validate and print plan without invoking agents
`)
}

func collaborate(args []string) error {
	fs := flag.NewFlagSet("collaborate", flag.ContinueOnError)
	task := fs.String("task", "", "task text")
	taskFile := fs.String("task-file", "", "task file")
	repo := fs.String("repo", ".", "repository")
	configPath := fs.String("config", "harnessmesh.json", "config file")
	executorName := fs.String("executor", "", "executor agent override")
	reviewerName := fs.String("reviewer", "", "reviewer agent override")
	maxRounds := fs.Int("max-rounds", 0, "max rounds override")
	dryRun := fs.Bool("dry-run", false, "validate only")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *task != "" && *taskFile != "" {
		return errors.New("use either --task or --task-file, not both")
	}
	if *taskFile != "" {
		raw, err := os.ReadFile(*taskFile)
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
		*task = strings.TrimSpace(string(raw))
	}
	if strings.TrimSpace(*task) == "" {
		return errors.New("task is required")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *executorName != "" {
		cfg.Workflow.Executor = *executorName
	}
	if *reviewerName != "" {
		cfg.Workflow.Reviewer = *reviewerName
	}
	if *maxRounds > 0 {
		cfg.Workflow.MaxRounds = *maxRounds
	}

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	if err := ensureGitRepo(absRepo); err != nil {
		return err
	}

	executorCfg, ok := cfg.Agents[cfg.Workflow.Executor]
	if !ok {
		return fmt.Errorf("executor agent %q not found", cfg.Workflow.Executor)
	}
	reviewerCfg, ok := cfg.Agents[cfg.Workflow.Reviewer]
	if !ok {
		return fmt.Errorf("reviewer agent %q not found", cfg.Workflow.Reviewer)
	}

	if *dryRun {
		plan := map[string]any{
			"repo":       absRepo,
			"executor":   cfg.Workflow.Executor,
			"reviewer":   cfg.Workflow.Reviewer,
			"max_rounds": cfg.Workflow.MaxRounds,
			"switchyard": cfg.Switchyard,
		}
		return json.NewEncoder(os.Stdout).Encode(plan)
	}

	executor, err := agent.New(executorCfg, cfg.Switchyard)
	if err != nil {
		return fmt.Errorf("executor: %w", err)
	}
	reviewer, err := agent.New(reviewerCfg, cfg.Switchyard)
	if err != nil {
		return fmt.Errorf("reviewer: %w", err)
	}

	projector := contextpack.New(absRepo, cfg.Context)
	runDir, err := report.NewRunDir(absRepo)
	if err != nil {
		return err
	}

	ctx := context.Background()
	if cfg.Workflow.MaxWallTimeMinutes > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Workflow.MaxWallTimeMinutes)*time.Minute)
		defer cancel()
	}

	runner := workflow.Runner{
		Config:    cfg,
		Repo:      absRepo,
		RunDir:    runDir,
		Executor:  executor,
		Reviewer:  reviewer,
		Projector: projector,
	}

	result, err := runner.Run(ctx, *task)
	if result != nil {
		if writeErr := report.Write(runDir, result); writeErr != nil {
			fmt.Fprintln(os.Stderr, "WARNING: could not write report:", writeErr)
		}
		fmt.Printf("HarnessMesh run: %s\n", result.Status)
		fmt.Printf("Rounds: %d\n", len(result.Rounds))
		fmt.Printf("Report: %s\n", filepath.Join(runDir, "report.md"))
	}
	return err
}

func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	failed := false
	fmt.Println("HarnessMesh doctor")
	fmt.Println()

	for name, a := range cfg.Agents {
		binary := a.Command
		if binary == "" {
			binary = a.Kind
		}
		path, err := exec.LookPath(binary)
		if err != nil {
			fmt.Printf("FAIL agent %-20s binary %q not found\n", name, binary)
			failed = true
			continue
		}
		fmt.Printf("PASS agent %-20s %s\n", name, path)
	}

	if cfg.Switchyard.Enabled {
		if err := agent.CheckSwitchyard(context.Background(), cfg.Switchyard); err != nil {
			fmt.Printf("FAIL switchyard             %v\n", err)
			failed = true
		} else {
			fmt.Printf("PASS switchyard             %s\n", cfg.Switchyard.BaseURL)
		}
	} else {
		fmt.Println("INFO switchyard             disabled")
	}

	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("FAIL git                    not found")
		failed = true
	} else {
		fmt.Println("PASS git")
	}

	if failed {
		return errors.New("doctor found one or more problems")
	}
	return nil
}

func printConfig(args []string) error {
	fs := flag.NewFlagSet("print-config", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg.Redacted())
}

func ensureGitRepo(repo string) error {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s is not a git repository: %s", repo, strings.TrimSpace(string(out)))
	}
	return nil
}
