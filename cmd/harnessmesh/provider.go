package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/provider"
)

func providerCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, `Usage:
  harnessmesh provider serve [--config <path>] [--listen <addr>] [--token <token>]
  harnessmesh provider doctor [--config <path>]
  harnessmesh provider auth chatgpt [--token-path <path>]
`)
		return errors.New("subcommand required: serve, doctor, or auth")
	}
	switch args[0] {
	case "serve":
		return providerServe(args[1:])
	case "doctor":
		return providerDoctorCmd(args[1:])
	case "auth":
		return providerAuthCmd(args[1:])
	default:
		return fmt.Errorf("unknown provider subcommand %q", args[0])
	}
}

func providerAuthCmd(args []string) error {
	if len(args) == 0 || strings.ToLower(args[0]) != "chatgpt" {
		return errors.New("usage: harnessmesh provider auth chatgpt [--token-path <path>]")
	}
	fs := flag.NewFlagSet("provider auth chatgpt", flag.ContinueOnError)
	tokenPath := fs.String("token-path", provider.DefaultSIWCTokenPath(), "where to store the signed-in credential")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to wait for the browser sign-in to complete")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	fmt.Println("HarnessMesh: Sign in with ChatGPT")
	fmt.Println("This authorizes HarnessMesh's provider gateway to make Responses API")
	fmt.Println("requests billed against your ChatGPT plan usage allowance - never your")
	fmt.Println("separate OpenAI API billing, and never Codex CLI invocation.")
	fmt.Println("On plans where Codex usage is bundled with general ChatGPT plan usage,")
	fmt.Println("this DOES draw from the same shared allowance Codex itself draws from -")
	fmt.Println("see docs/codex-provider.md before relying on this as a free/unlimited lane.")
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := provider.StartLoginFlow(ctx, "127.0.0.1:0", "")
	if err != nil {
		return fmt.Errorf("start sign-in flow: %w", err)
	}

	fmt.Println("Open this URL in your browser to sign in with ChatGPT:")
	fmt.Println()
	fmt.Println("  " + result.AuthorizeURL)
	fmt.Println()
	_ = tryOpenBrowser(result.AuthorizeURL)
	fmt.Printf("Waiting up to %s for sign-in to complete...\n", timeout.String())

	outcome := <-result.Done
	if outcome.Err != nil {
		return fmt.Errorf("sign-in failed: %w", outcome.Err)
	}
	if err := provider.SaveSIWCTokenSetTo(*tokenPath, outcome.Tokens); err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	fmt.Printf("Signed in. Credential stored at %s.\n", *tokenPath)
	fmt.Println(`Configure a backend with {"type": "chatgpt-subscription"} in provider.backends to use it.`)
	return nil
}

// tryOpenBrowser best-effort opens url in the default browser; failure is
// non-fatal since the URL is always printed for manual use too.
func tryOpenBrowser(target string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{target}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		cmd, args = "xdg-open", []string{target}
	}
	return exec.Command(cmd, args...).Start()
}

// providerServe starts the Codex-compatible model-provider gateway. It is
// deliberately independent of the collaboration engine/store used by `mcp
// serve` and `bridge serve` - the provider plane and the collaboration
// plane share no server-side state.
func providerServe(args []string) error {
	fs := flag.NewFlagSet("provider serve", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	listen := fs.String("listen", "", "listen address (default 127.0.0.1:8789 or config.provider.listen)")
	token := fs.String("token", os.Getenv("HARNESSMESH_PROVIDER_TOKEN"), "provider gateway bearer token")
	metadataOnly := fs.Bool("metadata-only", false, "test-only: serve models/health and reject /responses (requires HARNESSMESH_METADATA_ONLY_TEST=1)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}
	if !cfg.Provider.Enabled {
		return errors.New("provider gateway is not enabled in config (set provider.enabled = true)")
	}

	listenAddr := *listen
	if listenAddr == "" {
		listenAddr = cfg.Provider.Listen
	}
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8789"
	}

	gwCfg := cfg.Provider
	if *token != "" {
		gwCfg.Token = *token
	}
	if gwCfg.Token == "" {
		return errors.New("provider gateway requires a bearer token: pass --token, set HARNESSMESH_PROVIDER_TOKEN, or configure provider.token")
	}

	registry, err := provider.NewRegistry(gwCfg, listenAddr)
	if err != nil {
		return fmt.Errorf("build provider registry: %w", err)
	}
	// Fail fast on genuine misconfiguration (an unresolvable or
	// policy-denied default_backend) before ever printing "listening" or
	// binding the port - a config that would 403 on every request is
	// caught here instead of only surfacing on the Codex extension's first
	// prompt. Deliberately not a Health() check too: a backend that is
	// merely unreachable *right now* (e.g. it hasn't finished starting yet
	// in a docker-compose/k8s dependency-ordering sense) should not
	// prevent the gateway process itself from starting - that is what
	// /readyz is for; use `harnessmesh provider doctor` to check backend
	// health explicitly before relying on a fresh deployment.
	if _, err := registry.Resolve(""); err != nil {
		return fmt.Errorf("resolve default backend: %w", err)
	}
	var srv *provider.Server
	if *metadataOnly {
		if os.Getenv("HARNESSMESH_METADATA_ONLY_TEST") != "1" {
			return errors.New("--metadata-only requires HARNESSMESH_METADATA_ONLY_TEST=1")
		}
		srv = provider.NewMetadataOnlyServer(gwCfg, registry)
	} else {
		srv = provider.NewServer(gwCfg, registry)
	}
	srv.SetAuditSink(provider.NewJSONAuditSink(os.Stderr))

	mode := "zero_api_billing_mode"
	if !gwCfg.IsZeroCreditMode() {
		mode = "UNRESTRICTED (zero_api_billing_mode=false)"
	}
	fmt.Fprintf(os.Stderr, "HarnessMesh provider gateway listening on %s (default_backend=%q, mode=%s)\n", listenAddr, gwCfg.DefaultBackend, mode)
	fmt.Fprintf(os.Stderr, "Codex config.toml: run `harnessmesh integrate codex-provider --listen %s`\n", listenAddr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return srv.Serve(ctx, listenAddr)
}

func providerDoctorCmd(args []string) error {
	fs := flag.NewFlagSet("provider doctor", flag.ContinueOnError)
	configPath := fs.String("config", "harnessmesh.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		return err
	}
	if doctorProvider(cfg) {
		return errors.New("provider doctor found one or more problems")
	}
	return nil
}

// doctorProvider diagnoses the Codex-compatible provider gateway: config
// presence, listener, selected backend and its reachability, zero-credit
// mode, whether OpenAI API / Codex fallback are allowed or denied, and
// streaming/tool-call capability of the resolved default backend. Returns
// true if it found a problem.
func doctorProvider(cfg *config.Config) bool {
	fmt.Println("\nCodex provider gateway:")
	failed := false

	if !cfg.Provider.Enabled {
		fmt.Println("INFO provider gateway not enabled (set provider.enabled=true; run 'harnessmesh integrate codex-provider' for setup help)")
		return false
	}

	listenAddr := cfg.Provider.Listen
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8789"
	}
	fmt.Printf("PASS provider gateway configured (listen=%s)\n", listenAddr)

	if cfg.Provider.Token == "" && os.Getenv("HARNESSMESH_PROVIDER_TOKEN") == "" {
		fmt.Println("FAIL no provider token configured (provider.token or HARNESSMESH_PROVIDER_TOKEN) - gateway will refuse to serve")
		failed = true
	} else {
		fmt.Println("PASS provider token configured")
	}

	mode := "strict (zero_api_billing_mode)"
	if !cfg.Provider.IsZeroCreditMode() {
		mode = "OFF - openai-api/codex backends are reachable if configured"
		fmt.Printf("INFO zero_api_billing_mode=%s\n", mode)
	} else {
		fmt.Println("PASS OpenAI API-key billing disabled")
		fmt.Println("PASS metered OpenAI API backends denied")
		fmt.Println("PASS Codex CLI inference backend denied")
		fmt.Println("INFO ChatGPT SIWC usage accounting is controlled by OpenAI")
		fmt.Println("INFO HarnessMesh cannot guarantee zero ChatGPT/Codex plan allowance usage")
	}

	if os.Getenv("OPENAI_API_KEY") != "" {
		fmt.Println("INFO OPENAI_API_KEY is set in this environment; the provider gateway never reads it for a non-openai-api backend, and zero-credit mode blocks the openai-api backend outright regardless")
	}

	if cfg.Provider.DefaultBackend == "" {
		fmt.Println("FAIL no default_backend configured")
		failed = true
	} else if bCfg, ok := cfg.Provider.Backends[cfg.Provider.DefaultBackend]; !ok {
		fmt.Printf("FAIL default_backend %q is not defined in provider.backends\n", cfg.Provider.DefaultBackend)
		failed = true
	} else {
		fmt.Printf("PASS default_backend %q (type=%q)\n", cfg.Provider.DefaultBackend, bCfg.Type)
		registry, err := provider.NewRegistry(cfg.Provider, listenAddr)
		if err != nil {
			fmt.Printf("FAIL building provider registry: %v\n", err)
			failed = true
		} else if backend, err := registry.Resolve(""); err != nil {
			fmt.Printf("FAIL resolving default backend: %v\n", err)
			failed = true
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := backend.Health(ctx); err != nil {
				fmt.Printf("FAIL default backend health check: %v\n", err)
				failed = true
			} else {
				fmt.Println("PASS default backend is healthy/reachable")
			}
			caps := backend.Capabilities()
			fmt.Printf("INFO default backend capabilities: streaming=%v tools=%v\n", caps.Streaming, caps.Tools)
		}
	}

	for name, bCfg := range cfg.Provider.Backends {
		if provider.IsMeteredBackendType(bCfg.Type) {
			if cfg.Provider.IsZeroCreditMode() {
				fmt.Printf("PASS backend %q (type=%q) is metered and correctly denied by zero-credit mode\n", name, bCfg.Type)
			} else {
				fmt.Printf("INFO backend %q (type=%q) is metered and reachable (zero-credit mode is off)\n", name, bCfg.Type)
			}
		}
		if strings.ToLower(bCfg.Type) == "chatgpt-subscription" {
			tokenPath := provider.DefaultSIWCTokenPath()
			if _, err := os.Stat(tokenPath); err != nil {
				fmt.Printf("INFO backend %q (type=chatgpt-subscription) has no stored credential yet - run 'harnessmesh provider auth chatgpt'\n", name)
			} else {
				fmt.Printf("INFO backend %q (type=chatgpt-subscription) has a stored credential at %s\n", name, tokenPath)
			}
			fmt.Println("INFO chatgpt-subscription is NOT metered OpenAI API billing and NEVER invokes the Codex CLI, but on plans where Codex is bundled it draws the SAME ChatGPT plan usage allowance Codex itself draws from - see docs/codex-provider.md")
		}
	}

	if cfg.Provider.Fallback.Enabled {
		fmt.Printf("INFO fallback enabled, order=%v (entries resolving to a metered type are skipped under zero-credit mode)\n", cfg.Provider.Fallback.Order)
	} else {
		fmt.Println("INFO fallback disabled (a failed default backend returns an error rather than trying another backend)")
	}

	return failed
}

// integrateCodexProvider generates/updates the Codex config.toml with a
// model_providers.harnessmesh entry pointing at this gateway. It preserves
// unrelated existing content by round-tripping through a generic TOML
// document rather than overwriting the whole file - the one known
// limitation of this approach is that comments in the existing file are
// not preserved (TOML has no structured comment-attachment on round trip
// without a format-preserving editor); a timestamped backup is written
// first so this is always reversible.
func integrateCodexProvider(args []string) error {
	fs := flag.NewFlagSet("integrate codex-provider", flag.ContinueOnError)
	scope := fs.String("scope", "user", "'user' (~/.codex/config.toml) or 'project' (./.codex/config.toml)")
	repo := fs.String("repo", ".", "repository path for project scope")
	listen := fs.String("listen", "127.0.0.1:8789", "HarnessMesh provider gateway listen address")
	model := fs.String("model", "harnessmesh-local", "model id to select (must be one your backend actually serves)")
	tokenEnvVar := fs.String("token-env-var", "HARNESSMESH_PROVIDER_TOKEN", "environment variable Codex will read the bearer token from (env_key)")
	dryRun := fs.Bool("dry-run", false, "print the resulting config.toml without writing it")
	check := fs.Bool("check", false, "exit non-zero if the file would change, without writing it")
	noBackup := fs.Bool("no-backup", false, "skip writing a timestamped backup of the existing file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path, err := codexConfigPath(*scope, *repo)
	if err != nil {
		return err
	}

	doc := map[string]any{}
	existing, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := toml.Unmarshal(existing, &doc); err != nil {
			return fmt.Errorf("parse existing %s: %w (fix or remove it before retrying)", path, err)
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read %s: %w", path, readErr)
	}

	baseURL := strings.TrimRight(fmt.Sprintf("http://%s/v1", *listen), "/")

	providers, _ := doc["model_providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	providers["harnessmesh"] = map[string]any{
		"name":     "HarnessMesh",
		"base_url": baseURL,
		"wire_api": "responses",
		"env_key":  *tokenEnvVar,
	}
	doc["model_providers"] = providers

	if *scope == "user" {
		// model_provider cannot be overridden in project-scoped config
		// files, per current Codex docs - only set the top-level selector
		// at user scope.
		doc["model_provider"] = "harnessmesh"
		doc["model"] = *model
	}

	out, err := toml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal config.toml: %w", err)
	}

	fmt.Println("Resulting model_providers.harnessmesh block:")
	fmt.Printf("\n[model_providers.harnessmesh]\nname = \"HarnessMesh\"\nbase_url = %q\nwire_api = \"responses\"\nenv_key = %q\n\n", baseURL, *tokenEnvVar)
	if *scope == "user" {
		fmt.Printf("model = %q\nmodel_provider = \"harnessmesh\"\n\n", *model)
	}
	fmt.Printf("Before starting Codex, set: export %s=<your provider gateway token>\n\n", *tokenEnvVar)

	if *dryRun {
		fmt.Printf("[dry-run] Would write %s (%d bytes)\n", path, len(out))
		return nil
	}

	changed := readErr != nil || !bytes.Equal(existing, out)
	if *check {
		if changed {
			return fmt.Errorf("%s would change", path)
		}
		fmt.Printf("%s is up to date\n", path)
		return nil
	}
	if !changed {
		fmt.Printf("%s already up to date; nothing to write\n", path)
		return nil
	}

	if readErr == nil && !*noBackup {
		backupPath := fmt.Sprintf("%s.bak-%d", path, time.Now().Unix())
		if err := os.WriteFile(backupPath, existing, 0600); err != nil {
			return fmt.Errorf("write backup %s: %w", backupPath, err)
		}
		fmt.Printf("Backed up existing config to %s\n", backupPath)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("Wrote %s\n", path)
	return nil
}

func codexConfigPath(scope, repo string) (string, error) {
	switch scope {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, ".codex", "config.toml"), nil
	case "project":
		absRepo, err := filepath.Abs(repo)
		if err != nil {
			return "", err
		}
		return filepath.Join(absRepo, ".codex", "config.toml"), nil
	default:
		return "", fmt.Errorf("unsupported scope %q (must be 'user' or 'project')", scope)
	}
}
