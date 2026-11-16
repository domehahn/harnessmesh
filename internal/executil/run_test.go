package executil

import (
	"context"
	"strings"
	"testing"
)

func TestSandboxPolicyRejectsCommandsAndPaths(t *testing.T) {
	policy := SandboxPolicy{AllowedCommands: []string{"git"}, AllowedPaths: []string{"/tmp/project"}}
	if err := policy.Validate("/tmp/project/subdir", "sh"); err == nil {
		t.Fatal("expected command policy rejection")
	}
	if err := policy.Validate("/tmp/other", "git"); err == nil {
		t.Fatal("expected path policy rejection")
	}
	if _, err := RunWithPolicy(context.Background(), "/tmp/other", nil, "", policy, "git", "status"); err == nil {
		t.Fatal("expected RunWithPolicy rejection")
	}
}

func TestSandboxPolicyAllowsConfiguredCommand(t *testing.T) {
	policy := SandboxPolicy{AllowedCommands: []string{"/usr/bin/git"}, AllowedPaths: []string{"/tmp/project"}}
	if err := policy.Validate("/tmp/project/subdir", "git"); err != nil {
		t.Fatalf("expected basename-equivalent command to be allowed: %v", err)
	}
}

func TestSandboxPolicyRejectsRelativePathBypass(t *testing.T) {
	policy := SandboxPolicy{AllowedCommands: []string{"/usr/bin/git"}, AllowedPaths: []string{"/tmp/project"}}
	// A relative or absolute command containing separators must not bypass by merely having basename "git"
	if err := policy.Validate("/tmp/project/subdir", "./evil/git"); err == nil {
		t.Fatal("expected ./evil/git to be rejected")
	}
	if err := policy.Validate("/tmp/project/subdir", "/tmp/evil/git"); err == nil {
		t.Fatal("expected /tmp/evil/git to be rejected")
	}
}

func TestRunWithPolicyScrubsHostSecrets(t *testing.T) {
	t.Setenv("HARNESSMESH_ADMIN_TOKEN", "super-secret-admin-token")
	t.Setenv("VAULT_TOKEN", "vault-secret-token")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
	t.Setenv("CUSTOM_SAFE_PASSTHROUGH", "passthrough-val")

	policy := SandboxPolicy{
		AllowedCommands: []string{"sh"},
		PassEnv:         []string{"CUSTOM_SAFE_PASSTHROUGH"},
	}
	res, err := RunWithPolicy(context.Background(), ".", map[string]string{"AGENT_CONFIGURED_ENV": "agent-val"}, "", policy, "sh", "-c", "env")
	if err != nil {
		t.Fatalf("RunWithPolicy failed: %v", err)
	}
	if strings.Contains(res.Stdout, "HARNESSMESH_ADMIN_TOKEN") {
		t.Errorf("expected HARNESSMESH_ADMIN_TOKEN to be scrubbed, but found in stdout")
	}
	if strings.Contains(res.Stdout, "VAULT_TOKEN") {
		t.Errorf("expected VAULT_TOKEN to be scrubbed, but found in stdout")
	}
	if strings.Contains(res.Stdout, "DATABASE_URL") {
		t.Errorf("expected DATABASE_URL to be scrubbed, but found in stdout")
	}
	if !strings.Contains(res.Stdout, "AGENT_CONFIGURED_ENV=agent-val") {
		t.Errorf("expected AGENT_CONFIGURED_ENV to be present")
	}
	if !strings.Contains(res.Stdout, "CUSTOM_SAFE_PASSTHROUGH=passthrough-val") {
		t.Errorf("expected CUSTOM_SAFE_PASSTHROUGH to be present")
	}
}

func TestRunScrubsControlPlaneSecrets(t *testing.T) {
	t.Setenv("HARNESSMESH_ADMIN_TOKEN", "admin-token-1234")
	t.Setenv("VAULT_TOKEN", "vault-token-5678")

	res, err := Run(context.Background(), ".", map[string]string{"EXPLICIT": "yes"}, "", "sh", "-c", "env")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if strings.Contains(res.Stdout, "HARNESSMESH_ADMIN_TOKEN") {
		t.Errorf("expected HARNESSMESH_ADMIN_TOKEN to be scrubbed from Run")
	}
	if strings.Contains(res.Stdout, "VAULT_TOKEN") {
		t.Errorf("expected VAULT_TOKEN to be scrubbed from Run")
	}
	if !strings.Contains(res.Stdout, "EXPLICIT=yes") {
		t.Errorf("expected EXPLICIT to be present")
	}
}

func TestBuildReceiptProducesDeterministicDigest(t *testing.T) {
	policy := SandboxPolicy{AllowedCommands: []string{"go"}, NetworkMode: "none"}
	res := Result{Stdout: "ok  \tpkg\t0.01s\n", ExitCode: 0, DurationMS: 12}
	r1 := BuildReceipt(res, policy, "go", "test", "./...")
	r2 := BuildReceipt(res, policy, "go", "test", "./...")
	if r1.ReceiptDigest == "" || r1.ReceiptDigest != r2.ReceiptDigest {
		t.Fatalf("expected deterministic receipt digest, got %+v vs %+v", r1, r2)
	}
	if !r1.NetworkIsolated || r1.SandboxDriver != "os-isolated" {
		t.Fatalf("expected os-isolated receipt metadata, got %+v", r1)
	}
}

func TestEgressProxyAndReadOnlyCredentialScoping(t *testing.T) {
	policy := SandboxPolicy{
		AllowedCommands: []string{"sh"},
		EgressProxy:     "http://127.0.0.1:8888",
		CredentialScope: "read-only",
	}
	env := map[string]string{
		"GITHUB_TOKEN":          "ghp_write_token",
		"AWS_SECRET_ACCESS_KEY": "aws_secret",
		"SAFE_READ_VAR":         "allowed",
	}
	res, err := RunWithPolicy(context.Background(), ".", env, "", policy, "sh", "-c", "env")
	if err != nil {
		t.Fatalf("RunWithPolicy failed: %v", err)
	}
	if strings.Contains(res.Stdout, "GITHUB_TOKEN=") || strings.Contains(res.Stdout, "AWS_SECRET_ACCESS_KEY=") {
		t.Fatalf("expected mutating credentials to be stripped in read-only scope: %s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "HTTPS_PROXY=http://127.0.0.1:8888") || !strings.Contains(res.Stdout, "SAFE_READ_VAR=allowed") {
		t.Fatalf("expected egress proxy and safe variable in env: %s", res.Stdout)
	}
}
