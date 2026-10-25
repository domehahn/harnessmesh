package plugin

// This file contains the provider-neutral supply-chain primitives used by the
// marketplace. Object storage, scanners and key material are deliberately
// interfaces so local MinIO and production S3/Vault/KMS implementations use
// the same admission rules.

import (
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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Dependency struct {
	Name       string `json:"name"`
	Constraint string `json:"constraint,omitempty"`
}
type Conflict struct {
	Name       string `json:"name"`
	Constraint string `json:"constraint,omitempty"`
}

// RegistryEntry is the signed, immutable metadata for one artifact version.
type RegistryEntry struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	ArtifactKey  string       `json:"artifact_key"`
	SHA256       string       `json:"sha256"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	Conflicts    []Conflict   `json:"conflicts,omitempty"`
	SBOMKey      string       `json:"sbom_key,omitempty"`
	Scan         ScanReport   `json:"scan"`
}
type RegistryIndex struct {
	Version     int             `json:"version"`
	GeneratedAt time.Time       `json:"generated_at"`
	KeyID       string          `json:"key_id"`
	Entries     []RegistryEntry `json:"entries"`
	Signature   string          `json:"signature"`
}

// CodexMarketplace is the compatible subset of a Codex marketplace catalog.
// Unknown fields are ignored so newer catalogs cannot widen execution policy.
type CodexMarketplace struct {
	Name      string `json:"name"`
	Interface struct {
		DisplayName string `json:"displayName"`
	} `json:"interface"`
	Plugins []CodexMarketplacePlugin `json:"plugins"`
}
type CodexMarketplacePlugin struct {
	Name   string `json:"name"`
	Source struct {
		Kind string `json:"source"`
		Path string `json:"path"`
		URL  string `json:"url,omitempty"`
	} `json:"source"`
	Policy   map[string]any `json:"policy,omitempty"`
	Category string         `json:"category,omitempty"`
}

func ImportCodexMarketplace(path string) ([]CodexMarketplacePlugin, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var catalog CodexMarketplace
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("decode Codex marketplace: %w", err)
	}
	seen := map[string]bool{}
	out := make([]CodexMarketplacePlugin, 0, len(catalog.Plugins))
	for _, p := range catalog.Plugins {
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" || seen[p.Name] {
			continue
		}
		if p.Source.Kind != "" && p.Source.Kind != "local" && p.Source.Kind != "url" {
			return nil, fmt.Errorf("plugin %q uses unsupported source %q", p.Name, p.Source.Kind)
		}
		seen[p.Name] = true
		out = append(out, p)
	}
	return out, nil
}

type ScanReport struct {
	SBOM            string    `json:"sbom_status"`
	Vulnerabilities string    `json:"vulnerability_status"`
	Malware         string    `json:"malware_status"`
	License         string    `json:"license_status"`
	AISecurity      string    `json:"ai_security_status,omitempty"`
	Findings        []string  `json:"findings,omitempty"`
	CompletedAt     time.Time `json:"completed_at"`
}

type ArtifactStore interface {
	Put(context.Context, string, io.Reader) (string, error)
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	Exists(context.Context, string) (bool, error)
}

type S3ArtifactStore struct {
	Client         *s3.Client
	Bucket, Prefix string
}

func NewS3ArtifactStore(ctx context.Context, endpoint, region, bucket, accessKey, secretKey string) (*S3ArtifactStore, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("S3 bucket is required")
	}
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if accessKey != "" || secretKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	clientOptions := []func(*s3.Options){func(o *s3.Options) { o.UsePathStyle = true }}
	if endpoint != "" {
		clientOptions = append(clientOptions, func(o *s3.Options) { o.BaseEndpoint = aws.String(endpoint) })
	}
	return &S3ArtifactStore{Client: s3.NewFromConfig(cfg, clientOptions...), Bucket: bucket}, nil
}
func (s *S3ArtifactStore) key(key string) string {
	return strings.Trim(strings.TrimRight(s.Prefix, "/")+"/"+strings.TrimLeft(key, "/"), "/")
}
func (s *S3ArtifactStore) Put(ctx context.Context, key string, in io.Reader) (string, error) {
	data, err := io.ReadAll(in)
	if err != nil {
		return "", err
	}
	if _, err = s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(s.key(key)), Body: bytes.NewReader(data)}); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func (s *S3ArtifactStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(s.key(key))})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}
func (s *S3ArtifactStore) Delete(ctx context.Context, key string) error {
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(s.key(key))})
	return err
}
func (s *S3ArtifactStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(s.key(key))})
	if err != nil {
		return false, nil
	}
	return true, nil
}

// HTTPArtifactStore is the production gateway contract for an S3/MinIO/CDN
// endpoint. The gateway may be backed by presigned S3 URLs; HarnessMesh never
// needs provider-specific credentials in the plugin manifest.
type HTTPArtifactStore struct {
	BaseURL     string
	BearerToken string
	Client      *http.Client
}

func (s HTTPArtifactStore) request(ctx context.Context, method, key string, body io.Reader) (*http.Response, error) {
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		return nil, errors.New("artifact endpoint is not configured")
	}
	if strings.TrimSpace(key) == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return nil, errors.New("invalid artifact key")
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/"+url.PathEscape(key), body)
	if err != nil {
		return nil, err
	}
	if s.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.BearerToken)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}
func (s HTTPArtifactStore) Put(ctx context.Context, key string, in io.Reader) (string, error) {
	resp, err := s.request(ctx, http.MethodPut, key, in)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("artifact put returned %s", resp.Status)
	}
	return key, nil
}
func (s HTTPArtifactStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := s.request(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("artifact get returned %s", resp.Status)
	}
	return resp.Body, nil
}
func (s HTTPArtifactStore) Delete(ctx context.Context, key string) error {
	resp, err := s.request(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("artifact delete returned %s", resp.Status)
	}
	return nil
}
func (s HTTPArtifactStore) Exists(ctx context.Context, key string) (bool, error) {
	resp, err := s.request(ctx, http.MethodHead, key, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("artifact head returned %s", resp.Status)
	}
	return true, nil
}

// FileArtifactStore is the deterministic local/MinIO-volume implementation.
// Production S3/R2 adapters implement the same interface and preserve keys.
type FileArtifactStore struct{ Root string }

func (s FileArtifactStore) path(key string) (string, error) {
	if strings.TrimSpace(key) == "" || filepath.IsAbs(key) || filepath.Clean(key) != key || strings.HasPrefix(key, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid artifact key")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	// codeql[go/path-injection]
	p := filepath.Join(root, key)
	if !withinPath(root, p) {
		return "", errors.New("artifact key escapes store root")
	}
	return p, nil
}
func (s FileArtifactStore) Put(_ context.Context, key string, in io.Reader) (string, error) {
	p, err := s.path(key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".artifact-")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer os.Remove(name)
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(tmp, h), in); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	// codeql[go/path-injection]
	if err = os.Rename(name, p); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s FileArtifactStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}
func (s FileArtifactStore) Delete(_ context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	return os.Remove(p)
}
func (s FileArtifactStore) Exists(_ context.Context, key string) (bool, error) {
	p, err := s.path(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

type KeyRecord struct {
	ID        string     `json:"id"`
	PublicKey string     `json:"public_key"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}
type Keyring struct {
	Active string               `json:"active"`
	Keys   map[string]KeyRecord `json:"keys"`
}

func (k *Keyring) Verify(index RegistryIndex) error {
	rec, ok := k.Keys[index.KeyID]
	if !ok || rec.RevokedAt != nil {
		return errors.New("registry signing key is unknown or revoked")
	}
	pub, err := base64.StdEncoding.DecodeString(rec.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid registry public key")
	}
	sig, err := base64.StdEncoding.DecodeString(index.Signature)
	if err != nil {
		return errors.New("invalid registry signature")
	}
	unsigned := index
	unsigned.Signature = ""
	raw, _ := json.Marshal(unsigned)
	if !ed25519.Verify(ed25519.PublicKey(pub), raw, sig) {
		return errors.New("registry signature verification failed")
	}
	return nil
}
func SignIndex(index RegistryIndex, id string, private ed25519.PrivateKey) (RegistryIndex, error) {
	index.KeyID = id
	index.GeneratedAt = time.Now().UTC()
	index.Signature = ""
	raw, err := json.Marshal(index)
	if err != nil {
		return index, err
	}
	index.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))
	return index, nil
}
func (k *Keyring) Revoke(id string, at time.Time) error {
	rec, ok := k.Keys[id]
	if !ok {
		return fmt.Errorf("unknown key %q", id)
	}
	rec.RevokedAt = &at
	k.Keys[id] = rec
	if k.Active == id {
		return errors.New("cannot revoke active registry key before rotation")
	}
	return nil
}
func (k *Keyring) Rotate(id string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid public key")
	}
	if k.Keys == nil {
		k.Keys = map[string]KeyRecord{}
	}
	k.Keys[id] = KeyRecord{ID: id, PublicKey: base64.StdEncoding.EncodeToString(pub)}
	k.Active = id
	return nil
}

// ResolveDependencies returns a deterministic install order and rejects
// missing versions, incompatible constraints and explicit conflicts.
func ResolveDependencies(entries []RegistryEntry, requested []string) ([]RegistryEntry, error) {
	requestedSet := map[string]bool{}
	for _, name := range requested {
		requestedSet[name] = true
	}
	for _, entry := range entries {
		if !requestedSet[entry.Name] {
			continue
		}
		for _, conflict := range entry.Conflicts {
			if requestedSet[conflict.Name] {
				return nil, fmt.Errorf("plugin %q conflicts with %q", entry.Name, conflict.Name)
			}
		}
	}
	return resolveDependenciesInternal(entries, requested)
}

func resolveDependenciesInternal(entries []RegistryEntry, requested []string) ([]RegistryEntry, error) {
	byName := map[string][]RegistryEntry{}
	for _, e := range entries {
		byName[e.Name] = append(byName[e.Name], e)
	}
	for name := range byName {
		sort.Slice(byName[name], func(i, j int) bool { return compareVersion(byName[name][i].Version, byName[name][j].Version) > 0 })
	}
	seen := map[string]bool{}
	visiting := map[string]bool{}
	out := []RegistryEntry{}
	var visit func(string, string) error
	visit = func(name, constraint string) error {
		if seen[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("dependency cycle at %s", name)
		}
		var e RegistryEntry
		found := false
		for _, candidate := range byName[name] {
			if satisfiesConstraint(candidate.Version, constraint) {
				e = candidate
				found = true
				break
			}
		}
		if !found {
			return nilIfDependencyMissing(name, constraint)
		}
		visiting[name] = true
		for _, d := range e.Dependencies {
			if err := visit(d.Name, d.Constraint); err != nil {
				return err
			}
		}
		for _, c := range e.Conflicts {
			if seen[c.Name] {
				return fmt.Errorf("plugin %q conflicts with %q", name, c.Name)
			}
		}
		delete(visiting, name)
		seen[name] = true
		out = append(out, e)
		return nil
	}
	for _, name := range requested {
		if err := visit(name, ""); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func nilIfDependencyMissing(name, constraint string) error {
	if constraint == "" {
		return fmt.Errorf("dependency %q is not available", name)
	}
	return fmt.Errorf("dependency %q does not satisfy %q", name, constraint)
}
func satisfiesConstraint(version, constraint string) bool {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" || constraint == "*" {
		return true
	}
	if strings.HasPrefix(constraint, "^") {
		base := strings.TrimPrefix(constraint, "^")
		return compareVersion(version, base) >= 0 && strings.Split(strings.TrimPrefix(version, "v"), ".")[0] == strings.Split(strings.TrimPrefix(base, "v"), ".")[0]
	}
	for _, op := range []string{">=", "<=", "=", ">", "<"} {
		if strings.HasPrefix(constraint, op) {
			cmp := compareVersion(version, strings.TrimSpace(strings.TrimPrefix(constraint, op)))
			switch op {
			case ">=":
				return cmp >= 0
			case "<=":
				return cmp <= 0
			case "=":
				return cmp == 0
			case ">":
				return cmp > 0
			case "<":
				return cmp < 0
			}
		}
	}
	return compareVersion(version, constraint) == 0
}

type Scanner interface {
	Scan(context.Context, string) (ScanReport, error)
}
type CommandScanner struct {
	Syft, Grype, Trivy, ClamAV, Skil string
	RequireAll                       bool
}

func (s CommandScanner) Scan(ctx context.Context, path string) (ScanReport, error) {
	r := ScanReport{SBOM: "not_configured", Vulnerabilities: "not_configured", Malware: "not_configured", License: "not_configured", AISecurity: "not_configured", CompletedAt: time.Now().UTC()}
	checks := []struct {
		name string
		bin  string
		args []string
		dst  *string
	}{{"sbom", s.Syft, []string{"packages", "dir:" + path, "-o", "json"}, &r.SBOM}, {"vulnerability", s.Grype, []string{"dir:" + path, "-o", "json"}, &r.Vulnerabilities}, {"malware", s.ClamAV, []string{"--no-summary", "-r", path}, &r.Malware}, {"license", s.Trivy, []string{"fs", "--scanners", "license", "--exit-code", "1", path}, &r.License}}
	for _, c := range checks {
		if c.bin == "" {
			if s.RequireAll {
				return r, fmt.Errorf("%s scanner is not configured", c.name)
			}
			continue
		}
		cmd := exec.CommandContext(ctx, c.bin, c.args...)
		if err := cmd.Run(); err != nil {
			*c.dst = "failed"
			r.Findings = append(r.Findings, c.name+": scan failed")
			if s.RequireAll {
				return r, err
			}
		} else {
			*c.dst = "passed"
		}
	}
	if s.Skil == "" {
		if s.RequireAll {
			return r, errors.New("skil security scanner is not configured")
		}
	} else {
		cmd := exec.CommandContext(ctx, s.Skil, "scan", path, "--static-only", "--format", "json")
		if err := cmd.Run(); err != nil {
			r.AISecurity = "failed"
			r.Findings = append(r.Findings, "ai_security: skil scan failed")
			if s.RequireAll {
				return r, err
			}
		} else {
			r.AISecurity = "passed"
		}
	}
	return r, nil
}
