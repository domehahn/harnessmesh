package plugin

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/executil"
)

const ProtocolVersion = "harnessmesh-agent-v1"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

type Manifest struct {
	Name                  string        `json:"name"`
	Version               string        `json:"version"`
	Description           string        `json:"description,omitempty"`
	Protocol              string        `json:"protocol,omitempty"`
	Transport             string        `json:"transport,omitempty"` // stdio or unix
	Socket                string        `json:"socket,omitempty"`
	Binary                string        `json:"binary,omitempty"`
	Aliases               []string      `json:"aliases,omitempty"`
	Capabilities          []string      `json:"capabilities,omitempty"`
	Dependencies          []Dependency  `json:"dependencies,omitempty"`
	Conflicts             []Conflict    `json:"conflicts,omitempty"`
	Risk                  string        `json:"risk,omitempty"`
	Enabled               bool          `json:"enabled"`
	SHA256                string        `json:"sha256,omitempty"`
	Signature             string        `json:"signature,omitempty"`
	PublicKey             string        `json:"public_key,omitempty"`
	MinHarnessMeshVersion string        `json:"min_harnessmesh_version,omitempty"`
	MaxHarnessMeshVersion string        `json:"max_harnessmesh_version,omitempty"`
	Sandbox               SandboxConfig `json:"sandbox,omitempty"`
}

type SandboxConfig struct {
	Network         string   `json:"network,omitempty"`
	AllowedPaths    []string `json:"allowed_paths,omitempty"`
	DeniedPaths     []string `json:"denied_paths,omitempty"`
	MaxOutputBytes  int      `json:"max_output_bytes,omitempty"`
	MaxMessageBytes int      `json:"max_message_bytes,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
	MaxProcesses    int      `json:"max_processes,omitempty"`
	MemoryMB        int      `json:"memory_mb,omitempty"`
}

type Status struct {
	Manifest          Manifest `json:"manifest"`
	Directory         string   `json:"directory"`
	Binary            string   `json:"binary,omitempty"`
	State             string   `json:"state"`
	Health            string   `json:"health"`
	Verified          bool     `json:"verified"`
	SignatureVerified bool     `json:"signature_verified"`
	Error             string   `json:"error,omitempty"`
}

type Options struct {
	RequireSignature   bool
	RequireHash        bool
	HarnessMeshVersion string
	TrustedPublicKeys  []ed25519.PublicKey
}

func (m Manifest) protocol() string {
	if strings.TrimSpace(m.Protocol) == "" {
		return ProtocolVersion
	}
	return strings.TrimSpace(m.Protocol)
}

func (m Manifest) sandbox(dir string) executil.SandboxPolicy {
	network := strings.ToLower(strings.TrimSpace(m.Sandbox.Network))
	if network == "" {
		network = "none"
	}
	return executil.SandboxPolicy{
		AllowedCommands: []string{filepath.Join(dir, m.Binary)},
		AllowedPaths:    m.Sandbox.AllowedPaths,
		DeniedPaths:     m.Sandbox.DeniedPaths,
		MaxOutputBytes:  m.Sandbox.MaxOutputBytes,
		NetworkMode:     network,
	}
}

func (m Manifest) validate(dir string, opts Options) (string, bool, bool, error) {
	if !namePattern.MatchString(strings.TrimSpace(m.Name)) {
		return "", false, false, fmt.Errorf("invalid plugin name %q", m.Name)
	}
	if strings.TrimSpace(m.Version) == "" {
		return "", false, false, fmt.Errorf("plugin %q has no version", m.Name)
	}
	if opts.HarnessMeshVersion != "" {
		if m.MinHarnessMeshVersion != "" && compareVersion(opts.HarnessMeshVersion, m.MinHarnessMeshVersion) < 0 {
			return "", false, false, fmt.Errorf("plugin %q requires HarnessMesh >= %s", m.Name, m.MinHarnessMeshVersion)
		}
		if m.MaxHarnessMeshVersion != "" && compareVersion(opts.HarnessMeshVersion, m.MaxHarnessMeshVersion) > 0 {
			return "", false, false, fmt.Errorf("plugin %q supports HarnessMesh <= %s", m.Name, m.MaxHarnessMeshVersion)
		}
	}
	if m.protocol() != ProtocolVersion {
		return "", false, false, fmt.Errorf("plugin %q uses unsupported protocol %q", m.Name, m.protocol())
	}
	transport := strings.ToLower(strings.TrimSpace(m.Transport))
	if transport == "" {
		transport = "stdio"
	}
	if transport != "stdio" && transport != "unix" {
		return "", false, false, fmt.Errorf("plugin %q uses unsupported transport %q", m.Name, m.Transport)
	}
	if transport == "unix" && m.Socket != "" && (filepath.IsAbs(m.Socket) || filepath.Clean(m.Socket) != m.Socket || strings.HasPrefix(m.Socket, ".."+string(filepath.Separator))) {
		return "", false, false, fmt.Errorf("plugin %q socket must stay inside its plugin directory", m.Name)
	}
	if strings.TrimSpace(m.Binary) == "" {
		return "", false, false, nil // catalog-only marketplace entry
	}
	if m.Risk == "" {
		m.Risk = "high"
	}
	switch strings.ToLower(m.Risk) {
	case "low", "medium", "high", "critical":
	default:
		return "", false, false, fmt.Errorf("plugin %q has invalid risk %q", m.Name, m.Risk)
	}
	if len(m.Capabilities) == 0 {
		return "", false, false, fmt.Errorf("plugin %q must declare capabilities", m.Name)
	}
	if m.Sandbox.MaxProcesses < 0 || m.Sandbox.MemoryMB < 0 || m.Sandbox.MaxMessageBytes < 0 || m.Sandbox.MaxOutputBytes < 0 || m.Sandbox.TimeoutSeconds < 0 {
		return "", false, false, fmt.Errorf("plugin %q has negative sandbox resource limits", m.Name)
	}
	if filepath.IsAbs(m.Binary) || filepath.Clean(m.Binary) != m.Binary || strings.HasPrefix(m.Binary, ".."+string(filepath.Separator)) {
		return "", false, false, fmt.Errorf("plugin %q binary must stay inside its plugin directory", m.Name)
	}
	binaryPath := filepath.Join(dir, m.Binary)
	// codeql[go/path-injection]
	info, err := os.Stat(binaryPath)
	if err != nil {
		return "", false, false, fmt.Errorf("plugin %q binary: %w", m.Name, err)
	}
	if info.IsDir() || info.Mode().Perm()&0111 == 0 {
		return "", false, false, fmt.Errorf("plugin %q binary is not executable", m.Name)
	}
	if m.Sandbox.TimeoutSeconds <= 0 {
		m.Sandbox.TimeoutSeconds = 120
	}
	if m.Sandbox.MaxMessageBytes <= 0 {
		m.Sandbox.MaxMessageBytes = 1 << 20
	}
	if m.Sandbox.MaxOutputBytes <= 0 {
		m.Sandbox.MaxOutputBytes = 4 << 20
	}
	if err := m.sandbox(dir).Validate(dir, binaryPath); err != nil {
		return "", false, false, err
	}
	digest, err := fileDigest(binaryPath)
	if err != nil {
		return "", false, false, err
	}
	verified := strings.EqualFold(strings.TrimSpace(m.SHA256), digest)
	if opts.RequireHash && !verified {
		return "", false, false, fmt.Errorf("plugin %q failed sha256 verification", m.Name)
	}
	signatureVerified := false
	if m.Signature != "" || m.PublicKey != "" {
		pub, pubErr := base64.StdEncoding.DecodeString(m.PublicKey)
		sig, sigErr := base64.StdEncoding.DecodeString(m.Signature)
		if pubErr != nil || sigErr != nil || len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
			return "", false, false, fmt.Errorf("plugin %q has invalid ed25519 signature encoding", m.Name)
		}
		if len(opts.TrustedPublicKeys) > 0 {
			trusted := false
			for _, key := range opts.TrustedPublicKeys {
				if string(key) == string(pub) {
					trusted = true
					break
				}
			}
			if !trusted {
				return "", false, false, fmt.Errorf("plugin %q signer is not trusted", m.Name)
			}
		}
		signatureVerified = ed25519.Verify(ed25519.PublicKey(pub), []byte(digest), sig)
		if opts.RequireSignature && !signatureVerified {
			return "", false, false, fmt.Errorf("plugin %q failed signature verification", m.Name)
		}
	} else if opts.RequireSignature {
		return "", false, false, fmt.Errorf("plugin %q is unsigned", m.Name)
	}
	if !m.Enabled {
		return binaryPath, verified, signatureVerified, nil
	}
	return binaryPath, verified, signatureVerified, nil
}

func compareVersion(a, b string) int {
	parse := func(value string) [3]int {
		var out [3]int
		parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), "v"), ".")
		for i := 0; i < len(parts) && i < 3; i++ {
			var n int
			for _, r := range parts[i] {
				if r < '0' || r > '9' {
					break
				}
				n = n*10 + int(r-'0')
			}
			out[i] = n
		}
		return out
	}
	x, y := parse(a), parse(b)
	for i := 0; i < 3; i++ {
		if x[i] < y[i] {
			return -1
		}
		if x[i] > y[i] {
			return 1
		}
	}
	return 0
}

func fileDigest(path string) (string, error) {
	// codeql[go/path-injection]
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read plugin binary: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func readManifest(path string) (Manifest, error) {
	// codeql[go/path-injection]
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return m, nil
}

func agentConfig(m Manifest, dir string) config.AgentConfig {
	read := contains(m.Capabilities, "read_repository") || contains(m.Capabilities, "read")
	write := contains(m.Capabilities, "write_repository") || contains(m.Capabilities, "write")
	return config.AgentConfig{Kind: m.Name, Adapter: m.Name, Binary: filepath.Join(dir, m.Binary), WorkingDir: dir, Writable: write, ExecutionMode: "managed", NetworkMode: m.Sandbox.Network, Capabilities: config.AgentCapabilities{ReadRepository: read, WriteRepository: write, Review: contains(m.Capabilities, "review")}}
}

func contains(items []string, needle string) bool {
	for _, item := range items {
		if strings.EqualFold(item, needle) {
			return true
		}
	}
	return false
}

func descriptor(m Manifest) agent.AdapterDescriptor {
	return agent.AdapterDescriptor{Name: m.Name, Aliases: append([]string{}, m.Aliases...), Version: m.Version, Managed: true, External: true, Capabilities: append([]string{}, m.Capabilities...)}
}
