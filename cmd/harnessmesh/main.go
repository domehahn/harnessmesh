package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/mcp"
	"github.com/domehahn/harnessmesh/internal/modelrouting"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/report"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/workflow"
)

var version = "0.3.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "collaborate":
		err = collaborate(os.Args[2:])
	case "space":
		err = spaceCmd(os.Args[2:])
	case "channel":
		err = channelCmd(os.Args[2:])
	case "thread":
		err = threadCmd(os.Args[2:])
	case "inbox":
		err = inboxCmd(os.Args[2:])
	case "subscriptions":
		err = subscriptionsCmd(os.Args[2:])
	case "decide":
		err = decideCmd(os.Args[2:])
	case "mcp":
		err = mcpCmd(os.Args[2:])
	case "peer":
		err = peerCmd(os.Args[2:])
	case "agents":
		err = agentsCmd(os.Args[2:])
	case "session":
		err = sessionCmd(os.Args[2:])
	case "findings":
		err = findingsCmd(os.Args[2:])
	case "evidence":
		err = evidenceCmd(os.Args[2:])
	case "config":
		err = configCmd(os.Args[2:])
	case "integrate":
		err = integrateCmd(os.Args[2:])
	case "smoke-test":
		err = smokeTestCmd(os.Args[2:])
	case "switchyard":
		err = switchyardCmd(os.Args[2:])
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
	fmt.Fprintf(os.Stderr, `HarnessMesh v%s - persistent multi-agent collaboration fabric for AI coding harnesses

Usage:
  harnessmesh collaborate --task "..." [options]
  harnessmesh space <list|show|create|pause|resume|stop> [options]
  harnessmesh channel <list|create|show> [options]
  harnessmesh thread <list|show|reply> [options]
  harnessmesh inbox list --space <id> [options]
  harnessmesh subscriptions <list|add|remove> [options]
  harnessmesh decide <list|propose|accept> [options]
  harnessmesh integrate antigravity [--config <path>] [--repo <path>] [--dry-run]
  harnessmesh mcp serve [--repo <path>] [--config <path>] [--session <id>] [--caller <name>]
  harnessmesh mcp install <claude|codex|antigravity|copilot> [--scope <project|user>]
  harnessmesh peer converse --message "..." [--peer <name>] [--outcome <type>] [options]
  harnessmesh peer ask --peer <name> --question "..." [options]
  harnessmesh peer review --peer <name> [options]
  harnessmesh peer status --session <id>
  harnessmesh agents <list|show <name>>
  harnessmesh session <list|show|messages|resume|stop> [options]
  harnessmesh findings <session-id> [--json]
  harnessmesh evidence <session-id> [--json]
  harnessmesh config <validate|migrate|print> [options]
  harnessmesh switchyard <doctor|routes|config validate> [options]
  harnessmesh smoke-test antigravity-codex [--config <path>]
  harnessmesh doctor [--config <path>]
  harnessmesh print-config [--config <path>]
  harnessmesh version

Run 'harnessmesh <command> --help' for details on a specific command.
`, version)
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

	cfg, err := loadConfigOrDefault(*configPath)
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

	executor, err := agent.NewHarness(cfg.Workflow.Executor, executorCfg, cfg.Switchyard)
	if err != nil {
		return fmt.Errorf("executor: %w", err)
	}
	reviewer, err := agent.NewHarness(cfg.Workflow.Reviewer, reviewerCfg, cfg.Switchyard)
	if err != nil {
		return fmt.Errorf("reviewer: %w", err)
	}

	projector := contextpack.New(absRepo, cfg.Context)
	runDir, err := report.NewRunDir(absRepo)
	if err != nil {
		return err
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: could not open local SQLite store:", err)
	} else {
		defer st.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
		Store:     st,
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

func mcpCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, `Usage:
  harnessmesh mcp serve [--repo <path>] [--config <path>] [--session <id>] [--caller <name>]
  harnessmesh mcp install <claude|codex> [--scope <project|user>]
`)
		return errors.New("subcommand required: serve or install")
	}

	switch args[0] {
	case "serve":
		return mcpServe(args[1:])
	case "install":
		return mcpInstall(args[1:])
	default:
		return fmt.Errorf("unknown mcp subcommand %q", args[0])
	}
}

func mcpServe(args []string) error {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository path")
	configPath := fs.String("config", "harnessmesh.json", "config file")
	sessionID := fs.String("session", "", "existing session id to attach")
	caller := fs.String("caller", "claude", "caller agent name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}

	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return fmt.Errorf("open sqlite store: %w", err)
	}
	defer st.Close()

	harnesses := make(map[string]agent.Harness)
	for name, aCfg := range cfg.Agents {
		h, err := agent.NewHarness(name, aCfg, cfg.Switchyard)
		if err == nil {
			harnesses[name] = h
		}
	}

	proj := contextpack.New(absRepo, cfg.Context)
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      absRepo,
		Projector: proj,
		Harnesses: harnesses,
	})
	defer eng.Close()

	server := mcp.NewServer(eng, *sessionID, *caller)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return server.ServeStdio(ctx, os.Stdin, os.Stdout)
}

func mcpInstall(args []string) error {
	fs := flag.NewFlagSet("mcp install", flag.ContinueOnError)
	scope := fs.String("scope", "project", "installation scope: 'project' or 'user'")
	repo := fs.String("repo", ".", "repository path for project scope")
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := ""
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	if target == "" {
		target = "claude"
	}

	switch strings.ToLower(target) {
	case "claude", "claude-code":
		return installClaudeMCP(*scope, *repo)
	case "codex":
		return installCodexMCP(*scope, *repo)
	case "antigravity", "google-antigravity":
		return installAntigravityMCP(*scope, *repo)
	case "copilot", "copilot-cli", "gh-copilot":
		return installCopilotMCP(*scope, *repo)
	default:
		return fmt.Errorf("unsupported agent for mcp install %q (supported: claude, codex, antigravity, copilot)", target)
	}
}

func installClaudeMCP(scope, repo string) error {
	binPath, err := os.Executable()
	if err != nil {
		binPath = "harnessmesh"
	}

	if scope == "project" {
		absRepo, err := filepath.Abs(repo)
		if err != nil {
			return err
		}
		mcpJSONPath := filepath.Join(absRepo, ".mcp.json")
		configData := map[string]any{
			"mcpServers": map[string]any{
				"harnessmesh": map[string]any{
					"command": binPath,
					"args":    []string{"mcp", "serve"},
				},
			},
		}
		raw, err := json.MarshalIndent(configData, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(mcpJSONPath, raw, 0644); err != nil {
			return fmt.Errorf("write %s: %w", mcpJSONPath, err)
		}
		fmt.Printf("✓ Successfully configured HarnessMesh MCP server in %s\n", mcpJSONPath)
		fmt.Println("Claude Code will now automatically load HarnessMesh peer collaboration tools.")
		return nil
	}

	// User scope: attempt claude mcp add
	if claudeBin, err := exec.LookPath("claude"); err == nil {
		cmd := exec.Command(claudeBin, "mcp", "add", "harnessmesh", binPath, "mcp", "serve")
		out, err := cmd.CombinedOutput()
		if err == nil {
			fmt.Printf("✓ Registered HarnessMesh with Claude Code: %s\n", strings.TrimSpace(string(out)))
			return nil
		}
	}

	fmt.Println("To configure Claude Code globally, run:")
	fmt.Printf("  claude mcp add harnessmesh %s mcp serve\n", binPath)
	return nil
}

func installCodexMCP(scope, repo string) error {
	binPath, err := os.Executable()
	if err != nil {
		binPath = "harnessmesh"
	}
	absRepo, _ := filepath.Abs(repo)
	codexCfgPath := filepath.Join(absRepo, "codex-mcp.json")
	configData := map[string]any{
		"mcp_servers": map[string]any{
			"harnessmesh": map[string]any{
				"command": binPath,
				"args":    []string{"mcp", "serve", "--caller", "codex"},
			},
		},
	}
	raw, _ := json.MarshalIndent(configData, "", "  ")
	_ = os.WriteFile(codexCfgPath, raw, 0644)
	fmt.Printf("✓ Created Codex MCP configuration template in %s\n", codexCfgPath)
	return nil
}

func mergeMCPConfig(targetJSONPath, serverName string, serverEntry map[string]any, dryRun bool) error {
	var root map[string]any
	if data, err := os.ReadFile(targetJSONPath); err == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			root = make(map[string]any)
		}
	} else {
		root = make(map[string]any)
	}

	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		servers = make(map[string]any)
		root["mcpServers"] = servers
	}
	servers[serverName] = serverEntry

	raw, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("[dry-run] Would update %s with:\n%s\n", targetJSONPath, string(raw))
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(targetJSONPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(targetJSONPath, raw, 0644)
}

const antigravityRuleContent = `---
trigger: always_on
---

# HarnessMesh Collaboration Fabric Rule (v0.3)

You are equipped with HarnessMesh persistent multi-agent collaboration tools via the ` + "`harnessmesh`" + ` MCP server.

## Persistent Collaboration Fabric Capabilities
You operate inside a persistent collaboration space shared with other autonomous agents (e.g. OpenAI Codex, Claude Code, GitHub Copilot CLI):
1. **Persistent Spaces & Channels**: Communicate on dedicated channels such as ` + "`#architecture`" + `, ` + "`#security`" + `, ` + "`#testing`" + `, ` + "`#findings`" + `, ` + "`#decisions`" + `, or ` + "`#general`" + `.
2. **Mentions & Autonomous Activation**: Mentioning a peer (e.g. ` + "`@codex`" + ` or ` + "`mentions: [\"codex\"]`" + `) activates that agent autonomously in the background.
3. **Inbox & Asynchronous Awareness**: Periodically check your inbox with ` + "`collaboration.inbox`" + ` to receive replies, peer reviews, or repository event notifications.
4. **Evidence-Based Decisions**: Record technical decisions, dispute resolutions, and consensus via ` + "`collaboration.decide`" + `.

## Available Collaboration MCP Tools (v0.3)
- ` + "`collaboration.publish`" + `: Publish a message, architectural proposal, or review request to a channel/thread with optional mentions and file scope.
- ` + "`collaboration.reply`" + `: Post a threaded follow-up reply in an existing conversation thread.
- ` + "`collaboration.inbox`" + `: Retrieve pending messages, mentions, and notifications addressed to you.
- ` + "`collaboration.channels`" + `: List available collaboration channels.
- ` + "`collaboration.thread`" + `: Inspect the full transcript of a collaboration thread.
- ` + "`collaboration.decide`" + `: Propose or accept an evidence-backed technical decision.
- ` + "`collaboration.status`" + `: Inspect active spaces, participant activity modes, and open findings.

## Peer Tools (Backward Compatible)
- ` + "`peer.converse`" + `: Interactive peer conversation. Maintains thread continuity across multiple turns without losing context.
- ` + "`peer.ask`" + `: Quick targeted question or code inspection.
- ` + "`peer.request_review`" + `: Structured multi-finding review.
- ` + "`peer.status`" + `: Check active session status and findings.

## Autonomous Collaboration Guidelines
- **Autonomous Invocation**: You do NOT need human permission to consult peers or publish to collaboration channels. Proactively publish or converse whenever a second opinion, security audit, or validation adds value.
- **Zero Human Transfer**: The user works ONLY with you in this chat. NEVER ask the user to copy/paste text between chat windows, terminals, or web interfaces. HarnessMesh delivers peer responses directly into your tool results and inbox.
- **Process Responses In-Context**: Read peer responses, synthesize them, challenge incorrect findings with evidence, accept confirmed recommendations, and proceed with your engineering task autonomously.
- **Iterative Dialogue**: Continue discussions within the same thread until technical agreement or resolution is achieved.
`

func installAntigravityMCP(scope, repo string) error {
	binPath, err := os.Executable()
	if err != nil {
		binPath = "harnessmesh"
	}
	absRepo, _ := filepath.Abs(repo)
	targetDir := filepath.Join(absRepo, ".agent")
	if scope == "user" {
		home, _ := os.UserHomeDir()
		targetDir = filepath.Join(home, ".gemini", "antigravity")
	}
	cfgPath := filepath.Join(targetDir, "mcp_config.json")
	serverEntry := map[string]any{
		"command": binPath,
		"args":    []string{"mcp", "serve", "--caller", "antigravity"},
	}
	if err := mergeMCPConfig(cfgPath, "harnessmesh", serverEntry, false); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	fmt.Printf("✓ Successfully configured HarnessMesh MCP server for Google Antigravity in %s\n", cfgPath)
	return nil
}

func integrateCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh integrate antigravity [--repo <path>] [--config <path>] [--dry-run]")
	}
	switch strings.ToLower(args[0]) {
	case "antigravity", "google-antigravity":
		return integrateAntigravity(args[1:])
	default:
		return fmt.Errorf("unsupported integration target %q (supported: antigravity)", args[0])
	}
}

func integrateAntigravity(args []string) error {
	fs := flag.NewFlagSet("integrate antigravity", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository path")
	configPath := fs.String("config", "configs/antigravity-openai-peer.json", "configuration profile to use")
	dryRun := fs.Bool("dry-run", false, "print changes without modifying files")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}

	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}

	binPath, err := os.Executable()
	if err != nil {
		binPath = "harnessmesh"
	}

	// Determine agent folder: .agents if exists, otherwise .agent
	agentDirName := ".agent"
	if fi, err := os.Stat(filepath.Join(absRepo, ".agents")); err == nil && fi.IsDir() {
		agentDirName = ".agents"
	}

	mcpConfigPath := filepath.Join(absRepo, agentDirName, "mcp_config.json")
	rulePath := filepath.Join(absRepo, agentDirName, "rules", "harnessmesh.md")

	serverEntry := map[string]any{
		"command": binPath,
		"args": []string{
			"mcp",
			"serve",
			"--caller",
			"antigravity",
			"--config",
			absConfig,
			"--repo",
			absRepo,
		},
	}

	if err := mergeMCPConfig(mcpConfigPath, "harnessmesh", serverEntry, *dryRun); err != nil {
		return fmt.Errorf("configure MCP in %s: %w", mcpConfigPath, err)
	}

	if *dryRun {
		fmt.Printf("[dry-run] Would write rule to %s:\n%s\n", rulePath, antigravityRuleContent)
		fmt.Println("✓ Dry-run completed successfully.")
	} else {
		if err := os.MkdirAll(filepath.Dir(rulePath), 0755); err != nil {
			return fmt.Errorf("create rules dir: %w", err)
		}
		if err := os.WriteFile(rulePath, []byte(antigravityRuleContent), 0644); err != nil {
			return fmt.Errorf("write rule to %s: %w", rulePath, err)
		}
		fmt.Printf("✓ Successfully configured HarnessMesh MCP in %s\n", mcpConfigPath)
		fmt.Printf("✓ Successfully installed HarnessMesh peer collaboration rule in %s\n", rulePath)
		fmt.Println("Antigravity will now automatically have access to peer collaboration tools in VS Code.")
	}
	return nil
}

func installCopilotMCP(scope, repo string) error {
	binPath, err := os.Executable()
	if err != nil {
		binPath = "harnessmesh"
	}
	absRepo, _ := filepath.Abs(repo)
	targetDir := filepath.Join(absRepo, ".copilot")
	if scope == "user" {
		home, _ := os.UserHomeDir()
		targetDir = filepath.Join(home, ".copilot")
	}
	_ = os.MkdirAll(targetDir, 0755)
	cfgPath := filepath.Join(targetDir, "mcp.json")
	configData := map[string]any{
		"mcpServers": map[string]any{
			"harnessmesh": map[string]any{
				"command": binPath,
				"args":    []string{"mcp", "serve", "--caller", "copilot"},
			},
		},
	}
	raw, _ := json.MarshalIndent(configData, "", "  ")
	if err := os.WriteFile(cfgPath, raw, 0644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	fmt.Printf("✓ Successfully configured HarnessMesh MCP server for GitHub Copilot CLI in %s\n", cfgPath)
	return nil
}

func peerCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("peer subcommand required: converse, ask, review, or status")
	}
	switch args[0] {
	case "converse":
		return peerConverse(args[1:])
	case "ask":
		return peerAsk(args[1:])
	case "review":
		return peerReview(args[1:])
	case "status":
		return peerStatus(args[1:])
	default:
		return fmt.Errorf("unknown peer subcommand %q", args[0])
	}
}

func peerAsk(args []string) error {
	fs := flag.NewFlagSet("peer ask", flag.ContinueOnError)
	peerName := fs.String("peer", "", "target peer agent name (required)")
	question := fs.String("question", "", "question or inspection request (required)")
	sessionID := fs.String("session", "", "collaboration session id")
	repo := fs.String("repo", ".", "repository path")
	configPath := fs.String("config", "harnessmesh.json", "config file")
	caller := fs.String("caller", "user", "caller agent name")
	jsonOut := fs.Bool("json", false, "output result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *peerName == "" || *question == "" {
		return errors.New("--peer and --question are required")
	}

	absRepo, _ := filepath.Abs(*repo)
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	harnesses := make(map[string]agent.Harness)
	for name, aCfg := range cfg.Agents {
		h, err := agent.NewHarness(name, aCfg, cfg.Switchyard)
		if err == nil {
			harnesses[name] = h
		}
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      absRepo,
		Projector: contextpack.New(absRepo, cfg.Context),
		Harnesses: harnesses,
	})

	ctx := context.Background()
	if *sessionID == "" {
		sess, err := eng.CreateSession(ctx, "", "CLI peer ask")
		if err != nil {
			return err
		}
		*sessionID = sess.ID
	}

	ans, err := eng.Ask(ctx, *sessionID, *caller, protocol.AskRequest{
		Peer:     *peerName,
		Question: *question,
		Context: protocol.AskContextOptions{
			IncludeDiff:      true,
			IncludeGitStatus: true,
		},
	}, 1, "")
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(ans)
	}

	fmt.Printf("[%s -> %s]: %s\n", *caller, *peerName, ans.Answer)
	return nil
}

func peerReview(args []string) error {
	fs := flag.NewFlagSet("peer review", flag.ContinueOnError)
	peerName := fs.String("peer", "", "target peer reviewer name (required)")
	sessionID := fs.String("session", "", "collaboration session id")
	repo := fs.String("repo", ".", "repository path")
	configPath := fs.String("config", "harnessmesh.json", "config file")
	caller := fs.String("caller", "user", "caller agent name")
	focus := fs.String("focus", "", "comma-separated focus areas")
	jsonOut := fs.Bool("json", false, "output result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *peerName == "" {
		return errors.New("--peer is required")
	}

	absRepo, _ := filepath.Abs(*repo)
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	harnesses := make(map[string]agent.Harness)
	for name, aCfg := range cfg.Agents {
		h, err := agent.NewHarness(name, aCfg, cfg.Switchyard)
		if err == nil {
			harnesses[name] = h
		}
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      absRepo,
		Projector: contextpack.New(absRepo, cfg.Context),
		Harnesses: harnesses,
	})

	ctx := context.Background()
	if *sessionID == "" {
		sess, err := eng.CreateSession(ctx, "", "CLI peer review")
		if err != nil {
			return err
		}
		*sessionID = sess.ID
	}

	var focusList []string
	if *focus != "" {
		for _, f := range strings.Split(*focus, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				focusList = append(focusList, f)
			}
		}
	}

	rev, err := eng.RequestReview(ctx, *sessionID, *caller, protocol.ReviewRequestPayload{
		Peer:         *peerName,
		Focus:        focusList,
		IncludeDiff:  true,
		IncludeTests: len(cfg.Workflow.TestCommand) > 0,
	}, 1, "")
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(rev)
	}

	fmt.Printf("Review Status: %s\nSummary: %s\nFindings: %d\n", rev.Status, rev.Summary, len(rev.Findings))
	for _, f := range rev.Findings {
		fmt.Printf("  - [%s] %s (%s:%d): %s\n", strings.ToUpper(f.Severity), f.ID, f.File, f.Line, f.Claim)
	}
	return nil
}

func peerConverse(args []string) error {
	fs := flag.NewFlagSet("peer converse", flag.ContinueOnError)
	peerName := fs.String("peer", "", "target peer agent name")
	capability := fs.String("capability", "", "capability to route to")
	message := fs.String("message", "", "message or request (required)")
	expectedOutcome := fs.String("outcome", "", "expected outcome: second_opinion, review, architecture_check, debugging_help, security_analysis, validation")
	sessionID := fs.String("session", "", "collaboration session id")
	repo := fs.String("repo", ".", "repository path")
	configPath := fs.String("config", "configs/antigravity-openai-peer.json", "config file")
	caller := fs.String("caller", "antigravity", "caller agent name")
	causationID := fs.String("causation-id", "", "causation message ID")
	jsonOut := fs.Bool("json", false, "output result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *message == "" {
		return errors.New("--message is required")
	}

	absRepo, _ := filepath.Abs(*repo)
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	harnesses := make(map[string]agent.Harness)
	for name, aCfg := range cfg.Agents {
		h, err := agent.NewHarness(name, aCfg, cfg.Switchyard)
		if err == nil {
			harnesses[name] = h
		}
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      absRepo,
		Projector: contextpack.New(absRepo, cfg.Context),
		Harnesses: harnesses,
	})

	ctx := context.Background()
	if *sessionID == "" {
		sess, err := eng.CreateSession(ctx, "", "CLI peer converse")
		if err != nil {
			return err
		}
		*sessionID = sess.ID
	}

	resp, err := eng.Converse(ctx, *sessionID, *caller, protocol.ConverseRequest{
		Peer:                 *peerName,
		Capability:           *capability,
		Message:              *message,
		ExpectedResponseType: *expectedOutcome,
		CausationID:          *causationID,
		Context: protocol.ConverseContextOptions{
			IncludeDiff:      true,
			IncludeGitStatus: true,
		},
	}, 1, "")
	if err != nil {
		return err
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	fmt.Printf("[%s -> %s] Type: %s (Status: %s)\n", *caller, resp.Peer, resp.Type, resp.Status)
	fmt.Printf("Message ID: %s\n\n", resp.MessageID)
	fmt.Println(resp.Response)
	if len(resp.Findings) > 0 {
		fmt.Printf("\nFindings (%d):\n", len(resp.Findings))
		for _, f := range resp.Findings {
			fmt.Printf("  - [%s] %s: %s\n", strings.ToUpper(f.Severity), f.ID, f.Claim)
		}
	}
	return nil
}

func peerStatus(args []string) error {
	fs := flag.NewFlagSet("peer status", flag.ContinueOnError)
	sessionID := fs.String("session", "", "collaboration session id (required)")
	repo := fs.String("repo", ".", "repository path")
	configPath := fs.String("config", "harnessmesh.json", "config file")
	jsonOut := fs.Bool("json", false, "output result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *sessionID == "" {
		return errors.New("--session is required")
	}

	absRepo, _ := filepath.Abs(*repo)
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      absRepo,
		Projector: contextpack.New(absRepo, cfg.Context),
		Harnesses: map[string]agent.Harness{},
	})

	ctx := context.Background()
	status, err := eng.Status(ctx, *sessionID)
	if err != nil {
		return err
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	fmt.Printf("Session %s Status:\n", status.SessionID)
	fmt.Printf("  Participants:            %s\n", strings.Join(status.Participants, ", "))
	fmt.Printf("  Open Findings:           %d\n", status.OpenFindings)
	fmt.Printf("  Disputed Findings:       %d\n", status.DisputedFindings)
	fmt.Printf("  Resolved Findings:       %d\n", status.ResolvedFindings)
	fmt.Printf("  Peer Calls:              %d\n", status.PeerCalls)
	fmt.Printf("  Remaining Peer Rounds:   %d\n", status.RemainingPeerRounds)
	return nil
}

func sessionCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("session subcommand required: list, show, resume, or stop")
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	switch args[0] {
	case "list":
		sessions, err := st.ListSessions(ctx)
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Println("No collaboration sessions found.")
			return nil
		}
		fmt.Printf("%-24s %-12s %-20s %s\n", "SESSION ID", "STATUS", "CREATED", "TASK")
		for _, s := range sessions {
			taskBrief := s.Task
			if len(taskBrief) > 40 {
				taskBrief = taskBrief[:37] + "..."
			}
			fmt.Printf("%-24s %-12s %-20s %s\n", s.ID, s.Status, s.CreatedAt.Format("2006-01-02 15:04"), taskBrief)
		}
		return nil

	case "show":
		if len(args) < 2 {
			return errors.New("session id required: harnessmesh session show <id>")
		}
		s, err := st.GetSession(ctx, args[1])
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)

	case "messages":
		if len(args) < 2 {
			return errors.New("session id required: harnessmesh session messages <id>")
		}
		msgs, err := st.GetMessages(ctx, args[1])
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			fmt.Printf("No messages found for session %s.\n", args[1])
			return nil
		}
		fmt.Printf("Session %s Conversation Transcript (%d envelopes):\n\n", args[1], len(msgs))
		for _, m := range msgs {
			causation := ""
			if m.CausationID != "" {
				causation = fmt.Sprintf(" (causation: %s)", m.CausationID)
			}
			duration := ""
			if m.DurationMS > 0 {
				duration = fmt.Sprintf(" [%dms]", m.DurationMS)
			}
			status := ""
			if m.Status != "" {
				status = fmt.Sprintf(" (status: %s)", m.Status)
			}
			fmt.Printf("[%s] %s -> %s | type: %s%s%s%s\n",
				m.CreatedAt.Format("15:04:05.000"), m.From, m.To, m.Type, causation, status, duration)
			if len(m.Payload) > 0 {
				payloadStr := string(m.Payload)
				if len(payloadStr) > 200 {
					payloadStr = payloadStr[:197] + "..."
				}
				fmt.Printf("    payload: %s\n", payloadStr)
			}
			fmt.Println()
		}
		return nil

	case "stop":
		if len(args) < 2 {
			return errors.New("session id required: harnessmesh session stop <id>")
		}
		return st.UpdateSessionStatus(ctx, args[1], "completed", "stopped by user")

	case "resume":
		if len(args) < 2 {
			return errors.New("session id required: harnessmesh session resume <id>")
		}
		s, err := st.GetSession(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Resuming session %s (Task: %s)\n", s.ID, s.Task)
		return nil

	default:
		return fmt.Errorf("unknown session subcommand %q", args[0])
	}
}

func findingsCmd(args []string) error {
	if len(args) < 1 {
		return errors.New("session id required: harnessmesh findings <session-id>")
	}
	sessionID := args[0]
	jsonOut := false
	if len(args) > 1 && args[1] == "--json" {
		jsonOut = true
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	findings, err := st.GetFindings(context.Background(), sessionID)
	if err != nil {
		return err
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(findings)
	}

	if len(findings) == 0 {
		fmt.Printf("No findings found for session %s.\n", sessionID)
		return nil
	}

	fmt.Printf("Findings for session %s:\n\n", sessionID)
	for _, f := range findings {
		dupInfo := ""
		if f.DuplicateOf != "" {
			dupInfo = fmt.Sprintf(" [duplicate of %s]", f.DuplicateOf)
		}
		source := ""
		if f.SourceParticipant != "" {
			source = fmt.Sprintf(" (reported by %s)", f.SourceParticipant)
		}
		fmt.Printf("[%s] %s (%s)%s%s\n", strings.ToUpper(f.Severity), f.ID, f.Status, dupInfo, source)
		if f.File != "" {
			fmt.Printf("  Location: %s:%d\n", f.File, f.Line)
		}
		fmt.Printf("  Claim: %s\n", f.Claim)
		fmt.Printf("  Evidence: %s\n", f.Evidence)
		fmt.Printf("  Recommendation: %s\n\n", f.Recommendation)
	}
	return nil
}

func evidenceCmd(args []string) error {
	if len(args) < 1 {
		return errors.New("session id required: harnessmesh evidence <session-id>")
	}
	sessionID := args[0]
	jsonOut := len(args) > 1 && args[1] == "--json"

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	evs, err := st.GetEvidence(context.Background(), sessionID)
	if err != nil {
		return err
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(evs)
	}

	if len(evs) == 0 {
		fmt.Printf("No evidence recorded for session %s.\n", sessionID)
		return nil
	}

	fmt.Printf("Evidence items for session %s:\n\n", sessionID)
	for _, ev := range evs {
		findingRef := ""
		if ev.FindingID != "" {
			findingRef = fmt.Sprintf(" (finding: %s)", ev.FindingID)
		}
		fmt.Printf("[%s] Type: %s%s\n", ev.ID, ev.Type, findingRef)
		if ev.Command != "" {
			fmt.Printf("  Command: %s (exit: %d)\n", ev.Command, ev.ExitCode)
		}
		if ev.Excerpt != "" {
			fmt.Printf("  Excerpt: %s\n", ev.Excerpt)
		}
		if ev.Result != "" {
			fmt.Printf("  Result: %s\n", ev.Result)
		}
		fmt.Println()
	}
	return nil
}

func agentsCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("agents subcommand required: list or show <name>")
	}
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	subcmd := args[0]
	var remaining []string
	if strings.HasPrefix(subcmd, "-") {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return errors.New("agents subcommand required: list or show <name>")
		}
		subcmd = fs.Arg(0)
		remaining = fs.Args()[1:]
	} else {
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		remaining = fs.Args()
	}

	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}
	switch subcmd {
	case "list":
		fmt.Printf("%-18s %-16s %-12s %-10s %s\n", "AGENT", "ADAPTER", "ROLE", "WRITABLE", "CAPABILITIES")
		for name, a := range cfg.Agents {
			adapter := a.Kind
			if a.Adapter != "" {
				adapter = a.Adapter
			}
			role := a.Role
			if len(a.Roles) > 0 {
				role = strings.Join(a.Roles, ",")
			}
			writable := "no"
			if a.Writable {
				writable = "yes"
			}
			caps := fmt.Sprintf("read:%t write:%t review:%t ask:%t evidence:%t",
				a.Capabilities.ReadRepository,
				a.Capabilities.WriteRepository,
				a.Capabilities.Review,
				a.Capabilities.AnswerQuestions,
				a.Capabilities.SubmitEvidence)
			fmt.Printf("%-18s %-16s %-12s %-10s %s\n", name, adapter, role, writable, caps)
		}
		return nil
	case "show":
		if len(remaining) < 1 {
			return errors.New("usage: harnessmesh agents show <name>")
		}
		name := remaining[0]
		a, ok := cfg.Agents[name]
		if !ok {
			return fmt.Errorf("agent %q not found in config", name)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	default:
		return fmt.Errorf("unknown agents subcommand %q", subcmd)
	}
}

func configCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("config subcommand required: validate, migrate, or print")
	}
	switch args[0] {
	case "validate":
		fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
		configPath := fs.String("config", "harnessmesh.json", "config file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		path := *configPath
		if fs.NArg() > 0 {
			path = fs.Arg(0)
		}
		cfg, err := config.Load(path)
		if err != nil {
			return fmt.Errorf("config validation failed: %w", err)
		}
		fmt.Printf("✓ Configuration file %s is valid (version %d, %d agents configured)\n", path, cfg.Version, len(cfg.Agents))
		return nil
	case "migrate":
		if len(args) < 2 {
			return errors.New("usage: harnessmesh config migrate <v1-config.json> [v2-output.json]")
		}
		v1Path := args[1]
		data, err := os.ReadFile(v1Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", v1Path, err)
		}
		migrated, err := config.MigrateV1ToV2(data)
		if err != nil {
			return fmt.Errorf("migrate config: %w", err)
		}
		if len(args) > 2 {
			outPath := args[2]
			if err := os.WriteFile(outPath, migrated, 0644); err != nil {
				return fmt.Errorf("write %s: %w", outPath, err)
			}
			fmt.Printf("✓ Successfully migrated v1 config to v2 at %s\n", outPath)
		} else {
			fmt.Println(string(migrated))
		}
		return nil
	case "print":
		return printConfig(args[1:])
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", "configs/antigravity-openai-peer.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	failed := false
	fmt.Printf("HarnessMesh v%s doctor\n\n", version)

	// Check Antigravity integration
	agMcpFound := false
	for _, cand := range []string{".agent/mcp_config.json", ".agents/mcp_config.json"} {
		if data, err := os.ReadFile(cand); err == nil {
			var mcpMap map[string]any
			if json.Unmarshal(data, &mcpMap) == nil {
				if srvs, ok := mcpMap["mcpServers"].(map[string]any); ok && srvs["harnessmesh"] != nil {
					fmt.Printf("PASS antigravity MCP server configured (%s)\n", cand)
					agMcpFound = true
					break
				}
			}
		}
	}
	if !agMcpFound {
		fmt.Println("INFO antigravity MCP integration not found (run 'harnessmesh integrate antigravity')")
	}

	agRuleFound := false
	for _, cand := range []string{".agent/rules/harnessmesh.md", ".agents/rules/harnessmesh.md"} {
		if _, err := os.Stat(cand); err == nil {
			fmt.Printf("PASS antigravity rule installed (%s)\n", cand)
			agRuleFound = true
			break
		}
	}
	if !agRuleFound {
		fmt.Println("INFO antigravity collaboration rule not found (run 'harnessmesh integrate antigravity')")
	}

	ctx := context.Background()
	for name, a := range cfg.Agents {
		h, err := agent.NewHarness(name, a, cfg.Switchyard)
		if err != nil {
			fmt.Printf("FAIL agent %-20s adapter init failed: %v\n", name, err)
			failed = true
			continue
		}
		if err := h.Health(ctx); err != nil {
			var authErr *protocol.HarnessAuthenticationRequiredError
			if errors.As(err, &authErr) {
				fmt.Printf("WARN agent %-20s [%s] requires authentication (run '%s login' or check API key)\n", name, h.AdapterType(), h.AdapterType())
			} else {
				fmt.Printf("WARN agent %-20s [%s] %v\n", name, h.AdapterType(), err)
			}
		} else {
			fmt.Printf("PASS agent %-20s [%s] healthy\n", name, h.AdapterType())
		}
	}

	// Check model routing backends
	backends := modelrouting.BuildRegistry(cfg)
	hasSwitchyard := false
	for name, b := range backends {
		if b.Type() == "switchyard" {
			hasSwitchyard = true
			if err := b.Health(ctx); err != nil {
				fmt.Printf("FAIL model-routing switchyard [%s] %v\n", name, err)
				failed = true
			} else {
				fmt.Printf("PASS model-routing switchyard [%s] %s reachable\n", name, b.Diagnostics())
			}
		} else {
			fmt.Printf("INFO model-routing %-10s [%s] %s\n", b.Type(), name, b.Diagnostics())
		}
	}
	if !hasSwitchyard {
		fmt.Println("INFO model-routing switchyard disabled")
	}

	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("FAIL git                    not found")
		failed = true
	} else {
		fmt.Println("PASS git")
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		fmt.Printf("FAIL sqlite store           %v\n", err)
		failed = true
	} else {
		fmt.Println("PASS sqlite store")
		st.Close()
	}

	if failed {
		return errors.New("doctor found one or more problems")
	}
	return nil
}

func smokeTestCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh smoke-test antigravity-codex [--config <path>] [--repo <path>]")
	}
	switch strings.ToLower(args[0]) {
	case "antigravity-codex", "codex":
		return smokeTestAntigravityCodex(args[1:])
	default:
		return fmt.Errorf("unknown smoke-test target %q (supported: antigravity-codex)", args[0])
	}
}

func smokeTestAntigravityCodex(args []string) error {
	fs := flag.NewFlagSet("smoke-test antigravity-codex", flag.ContinueOnError)
	configPath := fs.String("config", "configs/antigravity-openai-peer.json", "config path")
	repo := fs.String("repo", ".", "repository path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Println("=== HarnessMesh Smoke Test: Antigravity <-> OpenAI Codex Peer ===")
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	fmt.Printf("1. Configuration loaded: %s (%d agents configured)\n", *configPath, len(cfg.Agents))

	absRepo, _ := filepath.Abs(*repo)
	st, err := store.OpenSQLite("")
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	fmt.Println("2. SQLite collaboration store initialized.")

	// Check if codex executable is present
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		fmt.Println("STATUS: NOT EXECUTED")
		fmt.Println("Reason: 'codex' executable was not found in PATH.")
		fmt.Println("Remediation: Install OpenAI Codex CLI or ensure it is accessible in PATH.")
		return nil
	}
	fmt.Printf("3. Codex executable found at: %s\n", codexBin)

	// Test Codex health / auth
	peerAgentName := "openai-reviewer"
	peerCfg, ok := cfg.Agents[peerAgentName]
	if !ok {
		peerAgentName = "codex-reviewer"
		peerCfg, ok = cfg.Agents[peerAgentName]
	}
	if !ok {
		for name, a := range cfg.Agents {
			if a.Kind == "codex" || a.Adapter == "codex" {
				peerAgentName = name
				peerCfg = a
				ok = true
				break
			}
		}
	}
	if !ok {
		return fmt.Errorf("no codex peer agent configured in %s", *configPath)
	}

	codexHarness, err := agent.NewHarness(peerAgentName, peerCfg, cfg.Switchyard)
	if err != nil {
		fmt.Println("STATUS: NOT EXECUTED")
		fmt.Printf("Reason: could not initialize Codex harness adapter: %v\n", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := codexHarness.Health(ctx); err != nil {
		fmt.Println("STATUS: NOT EXECUTED")
		fmt.Printf("Reason: OpenAI Codex CLI health check did not pass: %v\n", err)
		var authErr *protocol.HarnessAuthenticationRequiredError
		if errors.As(err, &authErr) {
			fmt.Println("Remediation: Run 'codex login' or provide OPENAI_API_KEY environment variable.")
		} else {
			fmt.Println("Remediation: Check your Codex CLI installation, network connectivity, or sandbox permissions.")
		}
		return nil
	}

	fmt.Printf("4. Codex peer adapter (%s) is authenticated and healthy.\n", peerAgentName)
	_ = absRepo
	fmt.Println("STATUS: PASSED")
	return nil
}

func switchyardCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: harnessmesh switchyard <doctor|routes|config validate> [options]")
	}
	switch args[0] {
	case "doctor":
		return switchyardDoctor(args[1:])
	case "routes":
		return switchyardRoutes(args[1:])
	case "config":
		if len(args) > 1 && args[1] == "validate" {
			return switchyardConfigValidate(args[2:])
		}
		return fmt.Errorf("usage: harnessmesh switchyard config validate [--config <path>]")
	default:
		return fmt.Errorf("unknown switchyard subcommand: %s", args[0])
	}
}

func switchyardDoctor(args []string) error {
	fs := flag.NewFlagSet("switchyard doctor", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	backends := modelrouting.BuildRegistry(cfg)
	var foundSwitchyard bool
	for name, b := range backends {
		if b.Type() == "switchyard" {
			foundSwitchyard = true
			fmt.Printf("Checking switchyard backend %q (%s)...\n", name, b.Diagnostics())
			if err := b.Health(ctx); err != nil {
				return fmt.Errorf("switchyard backend %q unhealthy: %w", name, err)
			}
			fmt.Printf("PASS switchyard backend %q is healthy and reachable\n", name)
		}
	}
	if !foundSwitchyard {
		if !cfg.Switchyard.Enabled {
			fmt.Println("INFO: Switchyard is disabled or not configured.")
			return nil
		}
		if err := agent.CheckSwitchyard(ctx, cfg.Switchyard); err != nil {
			return fmt.Errorf("switchyard unhealthy: %w", err)
		}
		fmt.Printf("PASS switchyard at %s is healthy\n", cfg.Switchyard.BaseURL)
	}
	return nil
}

func switchyardRoutes(args []string) error {
	fs := flag.NewFlagSet("switchyard routes", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	fmt.Printf("%-20s %-15s %-20s %-30s\n", "AGENT", "BACKEND", "ROUTE_ID", "ENDPOINT/INFO")
	for name, a := range cfg.Agents {
		backendName := "fixed"
		routeID := "default"
		endpoint := "direct"
		if a.ModelRouting != nil {
			backendName = a.ModelRouting.Backend
			if backendName == "" {
				backendName = a.ModelRouting.Type
			}
			if backendName == "" {
				backendName = "default"
			}
			if a.ModelRouting.Route != "" {
				routeID = a.ModelRouting.Route
			}
			if b, ok := cfg.ModelRoutingBackends[backendName]; ok && b.BaseURL != "" {
				endpoint = b.BaseURL
			}
		} else if a.UseSwitchyard || cfg.Switchyard.Enabled {
			backendName = "switchyard"
			if a.SwitchyardRouteID != "" {
				routeID = a.SwitchyardRouteID
			} else if cfg.Switchyard.RouteID != "" {
				routeID = cfg.Switchyard.RouteID
			}
			endpoint = cfg.Switchyard.BaseURL
		}
		fmt.Printf("%-20s %-15s %-20s %-30s\n", name, backendName, routeID, endpoint)
	}
	return nil
}

func switchyardConfigValidate(args []string) error {
	fs := flag.NewFlagSet("switchyard config validate", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}

	hasSwitchyard := false
	if cfg.Switchyard.Enabled {
		hasSwitchyard = true
		if cfg.Switchyard.BaseURL == "" {
			return fmt.Errorf("switchyard.base_url is required when switchyard.enabled is true")
		}
	}
	for name, b := range cfg.ModelRoutingBackends {
		if strings.ToLower(b.Type) == "switchyard" {
			hasSwitchyard = true
			if b.BaseURL == "" {
				return fmt.Errorf("model_routing_backends.%s.base_url is required for switchyard backend", name)
			}
		}
	}
	if !hasSwitchyard {
		fmt.Println("INFO: No Switchyard backends configured (running in fixed/external mode).")
		return nil
	}
	fmt.Println("PASS Switchyard configuration is valid.")
	return nil
}

func printConfig(args []string) error {
	fs := flag.NewFlagSet("print-config", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg.Redacted())
}

func loadConfigOrDefault(path string) (*config.Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		// Return standard defaults if file does not exist
		return config.Parse([]byte(`{
			"version": 2,
			"collaboration": {"max_peer_rounds": 3, "max_peer_depth": 2, "max_peer_calls": 10},
			"workflow": {"executor": "claude", "reviewer": "codex", "max_rounds": 3},
			"agents": {
				"claude": {"kind": "claude", "role": "executor", "writable": true},
				"codex": {"kind": "codex", "role": "reviewer", "writable": false}
			}
		}`))
	}
	return config.Load(path)
}

func ensureGitRepo(repo string) error {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s is not a git repository: %s", repo, strings.TrimSpace(string(out)))
	}
	return nil
}

// -------------------------------------------------------------------------
// v0.3 Collaboration Space CLI Commands
// -------------------------------------------------------------------------

func spaceCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh space <list|show|create|pause|resume|stop> [options]")
	}
	switch args[0] {
	case "list":
		return spaceList(args[1:])
	case "show":
		return spaceShow(args[1:])
	case "create":
		return spaceCreate(args[1:])
	case "pause":
		return spaceLifecycleAction(args[1:], protocol.SpaceStatePaused)
	case "resume":
		return spaceLifecycleAction(args[1:], protocol.SpaceStateActive)
	case "stop":
		return spaceLifecycleAction(args[1:], protocol.SpaceStateStopped)
	default:
		return fmt.Errorf("unknown space subcommand %q", args[0])
	}
}

func spaceList(args []string) error {
	fs := flag.NewFlagSet("space list", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	spaces, err := st.ListSpaces(context.Background())
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(spaces)
	}

	if len(spaces) == 0 {
		fmt.Println("No collaboration spaces found.")
		return nil
	}

	fmt.Printf("%-24s %-12s %-16s %s\n", "SPACE ID", "STATUS", "WRITER", "TITLE")
	for _, sp := range spaces {
		fmt.Printf("%-24s %-12s %-16s %s\n", sp.ID, sp.LifecycleState, sp.WriterParticipant, sp.Title)
	}
	return nil
}

func spaceShow(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh space show <id> [--json]")
	}
	spaceID := args[0]
	jsonOut := false
	if len(args) > 1 && args[1] == "--json" {
		jsonOut = true
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	sp, err := st.GetSpace(context.Background(), spaceID)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(sp)
	}

	fmt.Printf("Space ID:        %s\n", sp.ID)
	fmt.Printf("Title:           %s\n", sp.Title)
	fmt.Printf("Purpose:         %s\n", sp.Purpose)
	fmt.Printf("Status:          %s\n", sp.LifecycleState)
	fmt.Printf("Writer:          %s\n", sp.WriterParticipant)
	fmt.Printf("Participants (%d):\n", len(sp.Participants))
	for _, p := range sp.Participants {
		fmt.Printf("  - %-14s (adapter: %-10s mode: %-10s writable: %v)\n", p.ID, p.Adapter, p.Mode, p.Writable)
	}
	fmt.Printf("Channels (%d):\n", len(sp.Channels))
	for _, ch := range sp.Channels {
		fmt.Printf("  - #%-14s %s\n", ch.Name, ch.Description)
	}
	return nil
}

func spaceCreate(args []string) error {
	fs := flag.NewFlagSet("space create", flag.ContinueOnError)
	id := fs.String("id", "", "space ID (optional)")
	title := fs.String("title", "Collaboration Space", "space title")
	purpose := fs.String("purpose", "Multi-agent coding collaboration", "purpose")
	writer := fs.String("writer", "antigravity", "writer participant ID")
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, _ := loadConfigOrDefault(*configPath)
	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config: cfg,
		Store:  st,
	})

	participants := make(map[string]protocol.SpaceParticipant)
	if cfg != nil {
		for name, a := range cfg.Agents {
			mode := protocol.ParticipantModeActive
			if !a.Writable {
				mode = protocol.ParticipantModeOnDemand
			}
			participants[name] = protocol.SpaceParticipant{
				ID:       name,
				Adapter:  a.Kind,
				Mode:     mode,
				Writable: a.Writable,
			}
		}
	}
	if len(participants) == 0 {
		participants[*writer] = protocol.SpaceParticipant{
			ID:       *writer,
			Adapter:  "antigravity",
			Mode:     protocol.ParticipantModeActive,
			Writable: true,
		}
	}

	sp, err := eng.SpaceService().CreateSpace(context.Background(), *id, ".", *title, *purpose, *writer, participants)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Created collaboration space: %s (channels: %d, participants: %d)\n", sp.ID, len(sp.Channels), len(sp.Participants))
	return nil
}

func spaceLifecycleAction(args []string, state protocol.SpaceLifecycleState) error {
	if len(args) == 0 {
		return fmt.Errorf("space ID required")
	}
	spaceID := args[0]
	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.UpdateSpaceLifecycle(context.Background(), spaceID, state); err != nil {
		return err
	}
	fmt.Printf("✓ Space %s state updated to: %s\n", spaceID, state)
	return nil
}

func channelCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh channel <list|create|show> [options]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("channel list", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		jsonOut := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" {
			return errors.New("--space is required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		channels, err := st.ListChannels(context.Background(), *spaceID)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(channels)
		}
		for _, ch := range channels {
			fmt.Printf("#%-16s %s\n", ch.Name, ch.Description)
		}
		return nil

	case "create":
		fs := flag.NewFlagSet("channel create", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		name := fs.String("name", "", "channel name (required)")
		desc := fs.String("description", "", "channel description")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" || *name == "" {
			return errors.New("--space and --name are required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		ch := &protocol.Channel{
			ID:          *name,
			SpaceID:     *spaceID,
			Name:        *name,
			Description: *desc,
			Visibility:  protocol.ChannelVisibilityAll,
			CreatedBy:   "cli",
		}
		if err := st.CreateChannel(context.Background(), ch); err != nil {
			return err
		}
		fmt.Printf("✓ Created channel #%s in space %s\n", *name, *spaceID)
		return nil

	default:
		return fmt.Errorf("unknown channel subcommand %q", args[0])
	}
}

func threadCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh thread <list|show|reply> [options]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("thread list", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		channelID := fs.String("channel", "", "channel name/ID (optional)")
		jsonOut := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" {
			return errors.New("--space is required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		threads, err := st.ListThreads(context.Background(), *spaceID, *channelID)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(threads)
		}
		for _, th := range threads {
			fmt.Printf("%-24s #%-12s %-8s %s\n", th.ID, th.ChannelID, th.Status, th.Title)
		}
		return nil

	case "show":
		if len(args) < 2 {
			return errors.New("usage: harnessmesh thread show <thread-id> [--space <space-id>]")
		}
		threadID := args[1]
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		th, err := st.GetThread(context.Background(), threadID)
		if err != nil {
			return err
		}
		msgs, err := st.GetSpaceMessages(context.Background(), th.SpaceID, th.ChannelID, threadID, 100)
		if err != nil {
			return err
		}
		fmt.Printf("Thread: %s (Title: %s, Channel: #%s, Status: %s)\n", th.ID, th.Title, th.ChannelID, th.Status)
		fmt.Println("---------------------------------------------------------------------")
		for _, m := range msgs {
			fmt.Printf("[%s] %s -> %s: %s\n", m.CreatedAt.Format("15:04:05"), m.From, m.To, string(m.Payload))
		}
		return nil

	case "reply":
		fs := flag.NewFlagSet("thread reply", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		channelID := fs.String("channel", "", "channel name/ID (required)")
		threadID := fs.String("thread", "", "thread ID (required)")
		message := fs.String("message", "", "reply message (required)")
		from := fs.String("from", "human", "from participant")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" || *threadID == "" || *message == "" {
			return errors.New("--space, --thread, and --message are required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		eng := collaboration.NewEngine(collaboration.EngineConfig{Store: st})
		resp, err := eng.PublishReply(context.Background(), *spaceID, *channelID, *threadID, *from, *message, nil, nil)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Reply published (message: %s, thread: %s)\n", resp.MessageID, resp.ThreadID)
		return nil

	default:
		return fmt.Errorf("unknown thread subcommand %q", args[0])
	}
}

func inboxCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh inbox list --space <id> [--participant <name>] [--unread]")
	}
	fs := flag.NewFlagSet("inbox list", flag.ContinueOnError)
	spaceID := fs.String("space", "", "space ID (required)")
	participant := fs.String("participant", "antigravity", "participant ID")
	unread := fs.Bool("unread", false, "unread only")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *spaceID == "" {
		return errors.New("--space is required")
	}

	st, err := store.OpenSQLite("")
	if err != nil {
		return err
	}
	defer st.Close()

	eng := collaboration.NewEngine(collaboration.EngineConfig{Store: st})
	inbox, err := eng.GetInbox(context.Background(), *spaceID, *participant, nil, *unread)
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(inbox)
	}

	fmt.Printf("Inbox for %s in space %s (Total: %d):\n", *participant, *spaceID, len(inbox.Items))
	for _, it := range inbox.Items {
		flag := " "
		if it.MentionsMe {
			flag = "@"
		}
		fmt.Printf("%s [%s] from %-12s #%-12s: %s\n", flag, it.CreatedAt.Format("15:04"), it.From, it.ChannelID, it.Summary)
	}
	return nil
}

func subscriptionsCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh subscriptions <list|add|remove> [options]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("subscriptions list", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		participant := fs.String("participant", "", "optional participant filter")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" {
			return errors.New("--space is required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()

		var subs []protocol.Subscription
		if *participant != "" {
			subs, err = st.GetParticipantSubscriptions(context.Background(), *spaceID, *participant)
		} else {
			subs, err = st.GetSubscriptions(context.Background(), *spaceID)
		}
		if err != nil {
			return err
		}
		for _, s := range subs {
			fmt.Printf("%-24s %-12s channels: %v events: %v\n", s.ID, s.ParticipantID, s.Channels, s.EventTypes)
		}
		return nil

	case "add":
		fs := flag.NewFlagSet("subscriptions add", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		participant := fs.String("participant", "", "participant ID (required)")
		channelsStr := fs.String("channels", "", "comma-separated channels")
		eventsStr := fs.String("events", "", "comma-separated event types")
		scopeStr := fs.String("scope", "", "comma-separated scope patterns")
		modeStr := fs.String("mode", "active", "activity mode (active, passive, reviewer, coordinator)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" {
			return errors.New("--space is required")
		}
		if *participant == "" {
			return errors.New("--participant is required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()

		var channels []string
		if *channelsStr != "" {
			for _, c := range strings.Split(*channelsStr, ",") {
				if tr := strings.TrimSpace(c); tr != "" {
					channels = append(channels, tr)
				}
			}
		}
		var events []string
		if *eventsStr != "" {
			for _, e := range strings.Split(*eventsStr, ",") {
				if tr := strings.TrimSpace(e); tr != "" {
					events = append(events, tr)
				}
			}
		}
		var scopes []string
		if *scopeStr != "" {
			for _, s := range strings.Split(*scopeStr, ",") {
				if tr := strings.TrimSpace(s); tr != "" {
					scopes = append(scopes, tr)
				}
			}
		}
		sub := &protocol.Subscription{
			ID:            fmt.Sprintf("sub_%d", time.Now().UnixNano()),
			SpaceID:       *spaceID,
			ParticipantID: *participant,
			Channels:      channels,
			EventTypes:    events,
			ScopePatterns: scopes,
			Mode:          protocol.ParticipantActivityMode(*modeStr),
			CreatedAt:     time.Now().UTC(),
		}
		if err := st.SaveSubscription(context.Background(), sub); err != nil {
			return err
		}
		fmt.Printf("✓ Created subscription %s for participant %s in space %s\n", sub.ID, sub.ParticipantID, *spaceID)
		return nil

	case "remove":
		fs := flag.NewFlagSet("subscriptions remove", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		id := fs.String("id", "", "subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		subID := *id
		if subID == "" && len(fs.Args()) > 0 {
			subID = fs.Args()[0]
		}
		if *spaceID == "" {
			return errors.New("--space is required")
		}
		if subID == "" {
			return errors.New("subscription ID is required (use --id <id> or provide as argument)")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.DeleteSubscription(context.Background(), *spaceID, subID); err != nil {
			return err
		}
		fmt.Printf("✓ Removed subscription %s from space %s\n", subID, *spaceID)
		return nil

	default:
		return fmt.Errorf("unknown subscriptions subcommand %q", args[0])
	}
}

func decideCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh decide <list|propose|accept> [options]")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("decide list", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		jsonOut := fs.Bool("json", false, "output JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" {
			return errors.New("--space is required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		decs, err := st.ListDecisions(context.Background(), *spaceID)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(decs)
		}
		for _, d := range decs {
			fmt.Printf("%-20s %-12s proposed_by: %-12s %s\n", d.ID, d.Status, d.ProposedBy, d.Title)
		}
		return nil

	case "propose":
		fs := flag.NewFlagSet("decide propose", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		title := fs.String("title", "", "decision title (required)")
		statement := fs.String("statement", "", "decision statement (required)")
		rationale := fs.String("rationale", "", "decision rationale")
		from := fs.String("from", "human", "proposer participant ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" || *title == "" || *statement == "" {
			return errors.New("--space, --title, and --statement are required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		eng := collaboration.NewEngine(collaboration.EngineConfig{Store: st})
		d, err := eng.CreateDecision(context.Background(), *spaceID, *title, *statement, *rationale, *from, nil)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Proposed decision: %s (%s)\n", d.ID, d.Title)
		return nil

	case "accept":
		fs := flag.NewFlagSet("decide accept", flag.ContinueOnError)
		spaceID := fs.String("space", "", "space ID (required)")
		decisionID := fs.String("decision", "", "decision ID (required)")
		from := fs.String("from", "human", "accepting participant ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *spaceID == "" || *decisionID == "" {
			return errors.New("--space and --decision are required")
		}
		st, err := store.OpenSQLite("")
		if err != nil {
			return err
		}
		defer st.Close()
		eng := collaboration.NewEngine(collaboration.EngineConfig{Store: st})
		d, err := eng.AcceptDecision(context.Background(), *spaceID, *decisionID, *from)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Accepted decision: %s (status: %s)\n", d.ID, d.Status)
		return nil

	default:
		return fmt.Errorf("unknown decide subcommand %q", args[0])
	}
}
