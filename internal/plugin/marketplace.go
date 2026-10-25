package plugin

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

// Marketplace is the policy-bound marketplace lifecycle. Local source
// directories are the admission input; promoted artifacts are published to
// the configured S3-compatible store and exposed through the signed registry.
type Marketplace struct {
	root      string
	state     string
	store     store.Store
	scanner   Scanner
	artifacts ArtifactStore
	mu        sync.Mutex
}

type marketplaceState struct {
	Sources   map[string]MarketplaceSource `json:"sources"`
	Installed map[string]InstalledPlugin   `json:"installed"`
	Audit     []MarketplaceAudit           `json:"audit"`
	Registry  RegistryIndex                `json:"registry"`
}

type MarketplaceSource struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type InstalledPlugin struct {
	Name            string                     `json:"name"`
	Version         string                     `json:"version"`
	SourceID        string                     `json:"source_id"`
	InstalledAt     time.Time                  `json:"installed_at"`
	ActiveTenants   map[string]bool            `json:"active_tenants"`
	ActiveProjects  map[string]map[string]bool `json:"active_projects,omitempty"`
	PreviousVersion *PluginRevision            `json:"previous_version,omitempty"`
	History         []PluginRevision           `json:"history,omitempty"`
	Scan            ScanReport                 `json:"scan,omitempty"`
	ArtifactKey     string                     `json:"artifact_key,omitempty"`
	ApprovalStatus  string                     `json:"approval_status"`
	ApprovalID      string                     `json:"approval_id,omitempty"`
	RequestedBy     string                     `json:"requested_by,omitempty"`
	RequestedAt     time.Time                  `json:"requested_at,omitempty"`
	ExpiresAt       *time.Time                 `json:"expires_at,omitempty"`
	DecidedBy       string                     `json:"decided_by,omitempty"`
}

type PluginRevision struct {
	Version string `json:"version"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
}

type MarketplaceAudit struct {
	ID        string         `json:"id"`
	Tenant    string         `json:"tenant"`
	Subject   string         `json:"subject"`
	Action    string         `json:"action"`
	Plugin    string         `json:"plugin,omitempty"`
	Version   string         `json:"version,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func NewMarketplace(root string, st store.Store) *Marketplace {
	if strings.TrimSpace(root) == "" {
		root = "plugins"
	}
	abs, _ := filepath.Abs(root)
	m := &Marketplace{root: abs, state: filepath.Join(abs, ".marketplace-state.json"), store: st, scanner: CommandScanner{Syft: os.Getenv("HARNESSMESH_MARKETPLACE_SYFT_BIN"), Grype: os.Getenv("HARNESSMESH_MARKETPLACE_GRYPE_BIN"), Trivy: os.Getenv("HARNESSMESH_MARKETPLACE_TRIVY_BIN"), ClamAV: os.Getenv("HARNESSMESH_MARKETPLACE_CLAMAV_BIN"), Skil: os.Getenv("HARNESSMESH_MARKETPLACE_SKIL_BIN"), RequireAll: os.Getenv("HARNESSMESH_MARKETPLACE_REQUIRE_SCANS") == "1"}}
	if endpoint, bucket := strings.TrimSpace(os.Getenv("HARNESSMESH_MARKETPLACE_ARTIFACT_ENDPOINT")), strings.TrimSpace(os.Getenv("HARNESSMESH_MARKETPLACE_BUCKET")); endpoint != "" && bucket != "" {
		if remote, err := NewS3ArtifactStore(context.Background(), endpoint, os.Getenv("HARNESSMESH_MARKETPLACE_REGION"), bucket, os.Getenv("HARNESSMESH_OBJECTSTORE_ACCESS_KEY"), os.Getenv("HARNESSMESH_OBJECTSTORE_SECRET_KEY")); err == nil {
			m.artifacts = remote
		}
	}
	if m.artifacts == nil {
		m.artifacts = FileArtifactStore{Root: filepath.Join(abs, ".marketplace-artifacts")}
	}
	return m
}

func (m *Marketplace) load(ctx context.Context) (marketplaceState, error) {
	s := marketplaceState{Sources: map[string]MarketplaceSource{}, Installed: map[string]InstalledPlugin{}, Registry: RegistryIndex{Version: 1, Entries: []RegistryEntry{}}}
	if persistent, ok := m.store.(store.MarketplaceStore); ok {
		raw, err := persistent.LoadMarketplaceState(ctx)
		if err != nil {
			return s, err
		}
		if len(raw) == 0 {
			return s, nil
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("decode marketplace state: %w", err)
		}
		if s.Sources == nil {
			s.Sources = map[string]MarketplaceSource{}
		}
		if s.Installed == nil {
			s.Installed = map[string]InstalledPlugin{}
		}
		if s.Registry.Entries == nil {
			s.Registry.Entries = []RegistryEntry{}
		}
		return s, nil
	}
	raw, err := os.ReadFile(m.state)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("decode marketplace state: %w", err)
	}
	if s.Sources == nil {
		s.Sources = map[string]MarketplaceSource{}
	}
	if s.Installed == nil {
		s.Installed = map[string]InstalledPlugin{}
	}
	if s.Registry.Entries == nil {
		s.Registry.Entries = []RegistryEntry{}
	}
	return s, nil
}

func (m *Marketplace) save(ctx context.Context, s marketplaceState) error {
	if err := os.MkdirAll(m.root, 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if persistent, ok := m.store.(store.MarketplaceStore); ok {
		return persistent.SaveMarketplaceState(ctx, raw, time.Now().UTC())
	}
	tmp, err := os.CreateTemp(m.root, ".marketplace-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, m.state)
}

func (m *Marketplace) audit(ctx context.Context, s *marketplaceState, tenant, subject, action, pluginName, version string, details map[string]any) {
	e := MarketplaceAudit{ID: fmt.Sprintf("%d", time.Now().UnixNano()), Tenant: tenant, Subject: subject, Action: action, Plugin: pluginName, Version: version, Details: details, CreatedAt: time.Now().UTC()}
	s.Audit = append(s.Audit, e)
	if len(s.Audit) > 1000 {
		s.Audit = s.Audit[len(s.Audit)-1000:]
	}
	if m.store != nil {
		_ = m.store.EmitEvent(ctx, "", "marketplace."+action, map[string]any{"tenant": tenant, "subject": subject, "plugin": pluginName, "version": version, "details": details})
	}
}

func (m *Marketplace) Snapshot(ctx context.Context, tenant, query string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.load(ctx)
	if err != nil {
		return nil, err
	}
	if len(s.Sources) == 0 {
		s.Sources["local"] = MarketplaceSource{ID: "local", Name: "Local plugins", Path: m.root, Enabled: true, CreatedAt: time.Now().UTC()}
	}
	options := Options{RequireHash: os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_HASH") == "1", RequireSignature: os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_SIGNATURE") == "1", HarnessMeshVersion: os.Getenv("HARNESSMESH_VERSION")}
	roots := []string{m.root}
	for _, source := range s.Sources {
		if source.Enabled && source.Path != m.root {
			roots = append(roots, source.Path)
		}
	}
	items := make([]Status, 0)
	seen := map[string]bool{}
	for _, root := range roots {
		discovered, discoverErr := Discover(root, options)
		if discoverErr != nil {
			return nil, discoverErr
		}
		for _, item := range discovered {
			if !seen[item.Manifest.Name] {
				items = append(items, item)
				seen[item.Manifest.Name] = true
			}
		}
	}
	q := strings.ToLower(strings.TrimSpace(query))
	plugins := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if q != "" && !strings.Contains(strings.ToLower(item.Manifest.Name+" "+item.Manifest.Description+" "+strings.Join(item.Manifest.Capabilities, " ")), q) {
			continue
		}
		installed := s.Installed[item.Manifest.Name]
		plugins = append(plugins, map[string]any{"name": item.Manifest.Name, "version": item.Manifest.Version, "description": item.Manifest.Description, "capabilities": item.Manifest.Capabilities, "risk": item.Manifest.Risk, "state": item.State, "health": item.Health, "verified": item.Verified, "signature_verified": item.SignatureVerified, "installed": installed.Version != "", "active": installed.ActiveTenants[tenant], "approval_status": installed.ApprovalStatus, "approval_id": installed.ApprovalID, "requested_by": installed.RequestedBy, "requested_at": installed.RequestedAt, "expires_at": installed.ExpiresAt, "decided_by": installed.DecidedBy, "scan": installed.Scan, "history": installed.History, "artifact_key": installed.ArtifactKey})
	}
	sources := make([]MarketplaceSource, 0, len(s.Sources))
	for _, source := range s.Sources {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	audit := s.Audit
	if len(audit) > 100 {
		audit = audit[len(audit)-100:]
	}
	return map[string]any{"plugins": plugins, "sources": sources, "audit": audit, "registry": s.Registry, "tenant": tenant}, nil
}

// Registry returns the immutable metadata projection used by remote clients.
func (m *Marketplace) Registry(ctx context.Context) (RegistryIndex, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.load(ctx)
	return s.Registry, err
}

func (m *Marketplace) RegistrySnapshot(ctx context.Context) (map[string]any, error) {
	index, err := m.Registry(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": index.Version, "generated_at": index.GeneratedAt, "key_id": index.KeyID, "entries": index.Entries, "signature": index.Signature}, nil
}

func (m *Marketplace) GetArtifact(ctx context.Context, key string) (io.ReadCloser, error) {
	if strings.TrimSpace(key) == "" || strings.Contains(key, "..") || filepath.IsAbs(key) {
		return nil, errors.New("invalid artifact key")
	}
	return m.artifacts.Get(ctx, filepath.ToSlash(key))
}

// IsActive returns (known, active). Unknown plugins retain legacy config
// behavior; once a plugin is managed by the marketplace, tenant activation is
// enforced by the collaboration engine before invocation.
func (m *Marketplace) IsActive(ctx context.Context, tenant, project, name string) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.load(ctx)
	if err != nil {
		return false, false, err
	}
	item, known := s.Installed[name]
	if !known {
		return false, false, nil
	}
	active := item.ActiveTenants[tenant]
	if project != "" && item.ActiveProjects != nil {
		active = active && item.ActiveProjects[tenant][project]
	}
	if item.ExpiresAt != nil && time.Now().UTC().After(*item.ExpiresAt) {
		return true, false, nil
	}
	return true, active && item.ApprovalStatus == "approved", nil
}

func (m *Marketplace) Mutate(ctx context.Context, tenant, subject, action string, payload map[string]any) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.load(ctx)
	if err != nil {
		return nil, err
	}
	if len(s.Sources) == 0 {
		s.Sources["local"] = MarketplaceSource{ID: "local", Name: "Local plugins", Path: m.root, Enabled: true, CreatedAt: time.Now().UTC()}
	}
	name, _ := payload["plugin"].(string)
	name = strings.TrimSpace(name)
	sourceID, _ := payload["source_id"].(string)
	sourceID = strings.TrimSpace(sourceID)
	if m.store != nil && name != "" && (action == "install" || action == "update" || action == "rollback" || action == "uninstall") {
		leaseName := "marketplace:plugin:" + name
		acquired, leaseErr := m.store.AcquireLease(ctx, leaseName, subject, 10*time.Minute)
		if leaseErr != nil {
			return nil, fmt.Errorf("marketplace lock: %w", leaseErr)
		}
		if !acquired {
			return nil, errors.New("plugin lifecycle operation is already running")
		}
		defer func() { _ = m.store.ReleaseLease(context.Background(), leaseName, subject) }()
	}
	switch action {
	case "source_register":
		id, _ := payload["id"].(string)
		path, _ := payload["path"].(string)
		id, path = strings.TrimSpace(id), strings.TrimSpace(path)
		if id == "" || path == "" {
			return nil, errors.New("source id and path are required")
		}
		allowedRoot := strings.TrimSpace(os.Getenv("HARNESSMESH_MARKETPLACE_SOURCE_ROOT"))
		if allowedRoot == "" {
			allowedRoot = m.root
		}
		allowedRoot, err = filepath.Abs(allowedRoot)
		if err != nil {
			return nil, err
		}
		if filepath.IsAbs(path) {
			return nil, errors.New("source path must be relative to the configured marketplace source root")
		}
		abs := filepath.Join(allowedRoot, path)
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, errors.New("source path must be an existing directory")
		}
		if !withinPath(allowedRoot, abs) {
			return nil, errors.New("source path is outside the configured marketplace source root")
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return nil, errors.New("source path must be an existing directory")
		}
		s.Sources[id] = MarketplaceSource{ID: id, Name: fmt.Sprint(payload["name"]), Path: abs, Enabled: true, CreatedAt: time.Now().UTC()}
		m.audit(ctx, &s, tenant, subject, action, "", "", map[string]any{"source_id": id})
		if err := m.save(ctx, s); err != nil {
			return nil, err
		}
		return map[string]any{"source": s.Sources[id]}, nil
	case "source_remove":
		if sourceID == "" {
			return nil, errors.New("source_id is required")
		}
		delete(s.Sources, sourceID)
		m.audit(ctx, &s, tenant, subject, action, "", "", map[string]any{"source_id": sourceID})
		if err := m.save(ctx, s); err != nil {
			return nil, err
		}
		return map[string]any{"removed": sourceID}, nil
	case "activate", "deactivate", "approve":
		if name == "" {
			return nil, errors.New("plugin is required")
		}
		item := s.Installed[name]
		if item.Version == "" {
			return nil, errors.New("plugin is not installed")
		}
		projectID, _ := payload["project_id"].(string)
		if action == "activate" && item.ApprovalStatus != "approved" {
			return nil, errors.New("plugin activation requires an approved marketplace request")
		}
		if action == "activate" && item.ExpiresAt != nil && time.Now().UTC().After(*item.ExpiresAt) {
			return nil, errors.New("marketplace approval has expired")
		}
		if action == "activate" && projectID == "" {
			if limit, convErr := strconv.Atoi(strings.TrimSpace(os.Getenv("HARNESSMESH_MARKETPLACE_MAX_ACTIVE_PER_TENANT"))); convErr == nil && limit > 0 && !item.ActiveTenants[tenant] {
				active := 0
				for _, installed := range s.Installed {
					if installed.ActiveTenants[tenant] {
						active++
					}
				}
				if active >= limit {
					return nil, fmt.Errorf("tenant marketplace activation quota exceeded (%d)", limit)
				}
			}
		}
		if item.ActiveTenants == nil {
			item.ActiveTenants = map[string]bool{}
		}
		if item.ActiveProjects == nil {
			item.ActiveProjects = map[string]map[string]bool{}
		}
		if action == "activate" {
			if projectID == "" {
				item.ActiveTenants[tenant] = true
			} else {
				if item.ActiveProjects[tenant] == nil {
					item.ActiveProjects[tenant] = map[string]bool{}
				}
				item.ActiveProjects[tenant][projectID] = true
			}
		}
		if action == "deactivate" {
			if projectID == "" {
				delete(item.ActiveTenants, tenant)
			} else if item.ActiveProjects[tenant] != nil {
				delete(item.ActiveProjects[tenant], projectID)
			}
		}
		if action == "approve" {
			if item.ApprovalStatus != "pending" {
				return nil, fmt.Errorf("plugin approval is not pending: %s", item.ApprovalStatus)
			}
			if item.RequestedBy != "" && item.RequestedBy == subject {
				return nil, errors.New("four-eyes approval requires a different approver")
			}
			if item.ExpiresAt != nil && time.Now().UTC().After(*item.ExpiresAt) {
				return nil, errors.New("marketplace approval has expired")
			}
			item.ApprovalStatus = "approved"
			item.DecidedBy = subject
			if persistent, ok := m.store.(interface {
				SaveApproval(context.Context, *protocol.ApprovalRequest) error
			}); ok && item.ApprovalID != "" {
				now := time.Now().UTC()
				if err := persistent.SaveApproval(ctx, &protocol.ApprovalRequest{ID: item.ApprovalID, Agent: name, Status: protocol.ApprovalApproved, RequestedBy: item.RequestedBy, DecidedBy: subject, CreatedAt: item.RequestedAt, DecidedAt: &now, Reason: "marketplace plugin activation"}); err != nil {
					return nil, err
				}
			}
		}
		s.Installed[name] = item
		m.audit(ctx, &s, tenant, subject, action, name, item.Version, nil)
		if err := m.save(ctx, s); err != nil {
			return nil, err
		}
		return map[string]any{"plugin": item}, nil
	case "install", "update":
		if name == "" {
			return nil, errors.New("plugin is required")
		}
		m.progress(ctx, tenant, subject, name, "started", 0, "lifecycle operation accepted")
		if sourceID == "" {
			sourceID = "local"
		}
		src, ok := s.Sources[sourceID]
		if !ok {
			return nil, errors.New("source is not registered")
		}
		srcDir := filepath.Join(src.Path, name)
		if _, err := os.Stat(filepath.Join(srcDir, "plugin.json")); err != nil {
			return nil, errors.New("source plugin manifest not found")
		}
		manifest, err := readManifest(filepath.Join(srcDir, "plugin.json"))
		if err != nil {
			return nil, err
		}
		if manifest.Name != name {
			return nil, errors.New("manifest name does not match plugin")
		}
		trustedKeys, trustedErr := TrustedKeysFromEnvironment()
		if trustedErr != nil {
			return nil, trustedErr
		}
		if _, _, _, err := manifest.validate(srcDir, Options{
			RequireHash:        os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_HASH") == "1" || os.Getenv("HARNESSMESH_ENV") == "production",
			RequireSignature:   os.Getenv("HARNESSMESH_PLUGIN_REQUIRE_SIGNATURE") == "1",
			HarnessMeshVersion: os.Getenv("HARNESSMESH_VERSION"),
			TrustedPublicKeys:  trustedKeys,
		}); err != nil {
			return nil, fmt.Errorf("plugin admission rejected: %w", err)
		}
		available, discoverErr := Discover(src.Path, Options{})
		if discoverErr != nil {
			return nil, discoverErr
		}
		entries := make([]RegistryEntry, 0, len(available))
		for _, candidate := range available {
			entries = append(entries, RegistryEntry{Name: candidate.Manifest.Name, Version: candidate.Manifest.Version, Dependencies: candidate.Manifest.Dependencies, Conflicts: candidate.Manifest.Conflicts})
		}
		if _, resolveErr := ResolveDependencies(entries, []string{name}); resolveErr != nil {
			return nil, fmt.Errorf("dependency admission rejected: %w", resolveErr)
		}
		dst := filepath.Join(m.root, name)
		if old, ok := s.Installed[name]; ok && old.Version != "" {
			rev := PluginRevision{Version: old.Version, Path: dst}
			if oldStatus, err := Discover(dst, Options{}); err == nil && len(oldStatus) > 0 {
				rev.SHA256 = oldStatus[0].Manifest.SHA256
			}
			backup := filepath.Join(m.root, ".marketplace-backups", name, old.Version)
			if err := copyDir(dst, backup); err != nil {
				return nil, err
			}
			old.PreviousVersion = &rev
			old.History = append(old.History, rev)
			s.Installed[name] = old
		}
		tmp := filepath.Join(m.root, ".marketplace-staging", name)
		_ = os.RemoveAll(tmp)
		if err := copyDir(srcDir, tmp); err != nil {
			m.progress(ctx, tenant, subject, name, "failed", 20, err.Error())
			return nil, err
		}
		m.progress(ctx, tenant, subject, name, "staged", 40, "plugin staged for admission")
		var scan ScanReport
		if m.scanner != nil {
			var scanErr error
			scan, scanErr = m.scanner.Scan(ctx, tmp)
			if scanErr != nil {
				_ = os.RemoveAll(tmp)
				m.progress(ctx, tenant, subject, name, "failed", 60, scanErr.Error())
				return nil, fmt.Errorf("supply-chain scan rejected plugin: %w", scanErr)
			}
		}
		m.progress(ctx, tenant, subject, name, "scanned", 70, "supply-chain admission completed")
		_ = os.RemoveAll(dst)
		if err := os.Rename(tmp, dst); err != nil {
			return nil, err
		}
		item := s.Installed[name]
		item.Name = name
		item.Version = manifest.Version
		item.SourceID = sourceID
		item.InstalledAt = time.Now().UTC()
		item.ApprovalStatus = "pending"
		item.ApprovalID = fmt.Sprintf("marketplace-%d", time.Now().UTC().UnixNano())
		item.RequestedBy = subject
		item.RequestedAt = time.Now().UTC()
		expires := item.RequestedAt.Add(24 * time.Hour)
		item.ExpiresAt = &expires
		item.DecidedBy = ""
		item.Scan = scan
		item.ArtifactKey = filepath.ToSlash(filepath.Join(name, manifest.Version, "plugin.tar"))
		artifact, archiveErr := tarDirectory(dst)
		if archiveErr != nil {
			return nil, fmt.Errorf("create plugin artifact: %w", archiveErr)
		}
		artifactSHA, uploadErr := m.artifacts.Put(ctx, item.ArtifactKey, bytes.NewReader(artifact))
		if uploadErr != nil {
			return nil, fmt.Errorf("publish plugin artifact: %w", uploadErr)
		}
		if artifactSHA == "" {
			artifactSHA = sha256Hex(artifact)
		}
		entry := RegistryEntry{Name: name, Version: manifest.Version, ArtifactKey: item.ArtifactKey, SHA256: artifactSHA, Dependencies: manifest.Dependencies, Conflicts: manifest.Conflicts, Scan: scan}
		filtered := s.Registry.Entries[:0]
		for _, existing := range s.Registry.Entries {
			if existing.Name != name || existing.Version != manifest.Version {
				filtered = append(filtered, existing)
			}
		}
		s.Registry.Entries = append(filtered, entry)
		s.Registry.GeneratedAt = time.Now().UTC()
		if err := signConfiguredRegistry(&s.Registry); err != nil {
			return nil, err
		}
		if item.ActiveTenants == nil {
			item.ActiveTenants = map[string]bool{}
		}
		for activeTenant := range item.ActiveTenants {
			delete(item.ActiveTenants, activeTenant)
		}
		s.Installed[name] = item
		if persistent, ok := m.store.(interface {
			SaveApproval(context.Context, *protocol.ApprovalRequest) error
		}); ok {
			if err := persistent.SaveApproval(ctx, &protocol.ApprovalRequest{ID: item.ApprovalID, Agent: name, Status: protocol.ApprovalPending, RequestedBy: subject, CreatedAt: item.RequestedAt, Reason: "marketplace plugin activation"}); err != nil {
				return nil, err
			}
		}
		m.audit(ctx, &s, tenant, subject, action, name, manifest.Version, nil)
		if err := m.save(ctx, s); err != nil {
			return nil, err
		}
		m.progress(ctx, tenant, subject, name, "completed", 100, "artifact published and approval requested")
		return map[string]any{"plugin": item}, nil
	case "rollback":
		if name == "" {
			return nil, errors.New("plugin is required")
		}
		item, ok := s.Installed[name]
		if !ok || len(item.History) == 0 {
			return nil, errors.New("no rollback version available")
		}
		targetVersion, _ := payload["version"].(string)
		var target *PluginRevision
		for i := len(item.History) - 1; i >= 0; i-- {
			if targetVersion == "" || item.History[i].Version == targetVersion {
				copy := item.History[i]
				target = &copy
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("rollback version %q is not available", targetVersion)
		}
		backup := filepath.Join(m.root, ".marketplace-backups", name, target.Version)
		dst := filepath.Join(m.root, name)
		tmp := filepath.Join(m.root, ".marketplace-staging", name)
		_ = os.RemoveAll(tmp)
		if err := copyDir(backup, tmp); err != nil {
			return nil, err
		}
		_ = os.RemoveAll(dst)
		if err := os.Rename(tmp, dst); err != nil {
			return nil, err
		}
		current := PluginRevision{Version: item.Version, Path: dst}
		item.History = append(item.History, current)
		item.Version = target.Version
		item.PreviousVersion = target
		s.Installed[name] = item
		m.audit(ctx, &s, tenant, subject, action, name, item.Version, nil)
		if err := m.save(ctx, s); err != nil {
			return nil, err
		}
		return map[string]any{"plugin": item}, nil
	case "uninstall":
		if name == "" {
			return nil, errors.New("plugin is required")
		}
		item, ok := s.Installed[name]
		if !ok {
			return nil, errors.New("plugin is not installed")
		}
		_ = os.RemoveAll(filepath.Join(m.root, name))
		delete(s.Installed, name)
		m.audit(ctx, &s, tenant, subject, action, name, item.Version, nil)
		if err := m.save(ctx, s); err != nil {
			return nil, err
		}
		return map[string]any{"removed": name}, nil
	default:
		return nil, fmt.Errorf("unsupported marketplace action %q", action)
	}
}

func (m *Marketplace) progress(ctx context.Context, tenant, subject, pluginName, stage string, percent int, message string) {
	if m.store == nil {
		return
	}
	_ = m.store.EmitEvent(ctx, "", "marketplace.progress", map[string]any{"tenant": tenant, "subject": subject, "plugin": pluginName, "stage": stage, "percent": percent, "message": message, "created_at": time.Now().UTC()})
}

func signConfiguredRegistry(index *RegistryIndex) error {
	encoded := strings.TrimSpace(os.Getenv("HARNESSMESH_MARKETPLACE_SIGNING_PRIVATE_KEY"))
	if encoded == "" {
		if os.Getenv("HARNESSMESH_MARKETPLACE_REQUIRE_INDEX_SIGNATURE") == "1" {
			return errors.New("registry signing key is required")
		}
		return nil
	}
	private, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return errors.New("invalid marketplace registry signing key")
	}
	keyID := strings.TrimSpace(os.Getenv("HARNESSMESH_MARKETPLACE_SIGNING_KEY_ID"))
	if keyID == "" {
		return errors.New("marketplace signing key id is required")
	}
	signed, err := SignIndex(*index, keyID, ed25519.PrivateKey(private))
	if err != nil {
		return err
	}
	*index = signed
	return nil
}

func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func tarDirectory(root string) ([]byte, error) {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})
	if err != nil {
		_ = tw.Close()
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func withinPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
