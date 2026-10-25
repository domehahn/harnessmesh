package plugin

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
)

var (
	mu      sync.RWMutex
	catalog []Status
)

// Discover scans only plugin.json files. It never executes a plugin.
func Discover(root string, opts Options) ([]Status, error) {
	// codeql[go/path-injection]
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []Status{}, nil
		}
		return nil, err
	}
	result := make([]Status, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		manifestPath := filepath.Join(dir, "plugin.json")
		m, readErr := readManifest(manifestPath)
		if readErr != nil {
			continue
		}
		status := Status{Manifest: m, Directory: dir, State: "catalog-only", Health: "not-applicable"}
		binary, verified, signatureVerified, validationErr := m.validate(dir, opts)
		status.Binary, status.Verified, status.SignatureVerified = binary, verified, signatureVerified
		if validationErr != nil {
			status.State, status.Health, status.Error = "invalid", "unhealthy", validationErr.Error()
		}
		if binary != "" && validationErr == nil {
			if !m.Enabled {
				status.State, status.Health = "disabled", "disabled"
			} else if opts.RequireHash && !verified {
				status.State, status.Health = "unverified", "unhealthy"
			} else if opts.RequireSignature && !signatureVerified {
				status.State, status.Health = "unverified", "unhealthy"
			} else {
				status.State, status.Health = "ready", "unknown"
			}
		}
		result = append(result, status)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Manifest.Name < result[j].Manifest.Name })
	mu.Lock()
	catalog = append([]Status(nil), result...)
	mu.Unlock()
	return result, nil
}

func Catalog() []Status { mu.RLock(); defer mu.RUnlock(); return append([]Status(nil), catalog...) }

// Probe performs a bounded handshake against each enabled runtime plugin.
// The process is short-lived; session invocations use their own persistent
// stdio process and are not affected by this dashboard health check.
func Probe(ctx context.Context) []Status {
	items := Catalog()
	for i := range items {
		if items[i].State != "ready" {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		h, err := NewHarness(items[i].Manifest.Name, items[i].Manifest, items[i].Directory, items[i].Binary)
		if err == nil {
			err = h.Health(probeCtx)
			if closer, ok := h.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
		cancel()
		if err != nil {
			items[i].Health, items[i].Error = "unhealthy", err.Error()
		} else {
			items[i].Health, items[i].Error = "healthy", ""
		}
	}
	mu.Lock()
	catalog = append([]Status(nil), items...)
	mu.Unlock()
	return items
}

// RegisterDirectory activates enabled, verified external agent plugins. A
// plugin is registered in the normal agent adapter catalog, so config entries
// can use its manifest name without a source-code import.
func RegisterDirectory(root string, opts Options) ([]Status, error) {
	items, err := Discover(root, opts)
	if err != nil {
		return nil, err
	}
	for _, status := range items {
		m := status.Manifest
		if status.State != "ready" || !m.Enabled || status.Binary == "" {
			continue
		}
		dir, binary := status.Directory, status.Binary
		factory := func(id string, cfg config.AgentConfig, _ config.SwitchyardConfig) (agent.Harness, error) {
			return NewHarness(id, m, dir, binary)
		}
		agent.RegisterAdapterWithDescriptor(m.Name, factory, descriptor(m))
		for _, alias := range m.Aliases {
			if strings.TrimSpace(alias) != "" {
				agent.RegisterAdapterWithDescriptor(alias, factory, descriptor(m))
			}
		}
	}
	return items, nil
}

func RegisterFromEnvironment() error {
	root := strings.TrimSpace(os.Getenv("HARNESSMESH_PLUGIN_DIR"))
	if root == "" {
		root = "plugins"
	}
	version := strings.TrimSpace(os.Getenv("HARNESSMESH_VERSION"))
	if version == "" {
		version = "0.1.0"
	}
	trusted, err := TrustedKeysFromEnvironment()
	if err != nil {
		return err
	}
	_, err = RegisterDirectory(root, Options{RequireHash: os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_HASH") == "1" || os.Getenv("HARNESSMESH_ENV") == "production", RequireSignature: os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_SIGNATURE") == "1", HarnessMeshVersion: version, TrustedPublicKeys: trusted})
	return err
}

// RefreshFromEnvironment re-scans the configured plugin directory and
// updates the catalog/adapter registry. Existing harness instances are not
// rebound; new sessions see the refreshed adapter set safely.
func RefreshFromEnvironment() error {
	root := strings.TrimSpace(os.Getenv("HARNESSMESH_PLUGIN_DIR"))
	if root == "" {
		root = "plugins"
	}
	version := strings.TrimSpace(os.Getenv("HARNESSMESH_VERSION"))
	if version == "" {
		version = "0.1.0"
	}
	trusted, err := TrustedKeysFromEnvironment()
	if err != nil {
		return err
	}
	_, err = RegisterDirectory(root, Options{RequireHash: os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_HASH") == "1" || os.Getenv("HARNESSMESH_ENV") == "production", RequireSignature: os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_SIGNATURE") == "1", HarnessMeshVersion: version, TrustedPublicKeys: trusted})
	return err
}

func TrustedKeysFromEnvironment() ([]ed25519.PublicKey, error) {
	raw := strings.TrimSpace(os.Getenv("HARNESSMESH_PLUGIN_TRUSTED_KEYS"))
	if raw == "" {
		return nil, nil
	}
	keys := make([]ed25519.PublicKey, 0)
	for _, item := range strings.Split(raw, ",") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item))
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid HARNESSMESH_PLUGIN_TRUSTED_KEYS entry")
		}
		keys = append(keys, ed25519.PublicKey(decoded))
	}
	return keys, nil
}

func RequireReady(name string) error {
	for _, item := range Catalog() {
		if item.Manifest.Name == name {
			if item.State == "ready" {
				return nil
			}
			return fmt.Errorf("plugin %q is %s: %s", name, item.State, item.Error)
		}
	}
	return fmt.Errorf("plugin %q is not installed", name)
}
