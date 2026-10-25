package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/store"
)

type Item struct {
	ID        string            `json:"id"`
	TenantID  string            `json:"tenant_id"`
	Name      string            `json:"name"`
	Backend   string            `json:"backend"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}
type Manager interface {
	List(context.Context, string) ([]Item, error)
	Put(context.Context, string, string, string, map[string]string) (Item, error)
	Delete(context.Context, string, string) error
}

// Resolver is intentionally separate from Manager. Runtime adapters may
// resolve a secret for an authenticated tenant, while the dashboard can only
// list metadata and mutate values.
type Resolver interface {
	Resolve(context.Context, string, string) (string, error)
}

type Local struct {
	Store store.SecretStore
	key   []byte
}

func NewLocal(st store.SecretStore, master string) (*Local, error) {
	if st == nil {
		return nil, errors.New("secret store is required")
	}
	master = strings.TrimSpace(master)
	if master == "" {
		return nil, errors.New("HARNESSMESH_SECRET_KEY is required for local secret storage")
	}
	sum := sha256.Sum256([]byte(master))
	return &Local{Store: st, key: sum[:]}, nil
}
func (l *Local) cipher() (cipher.AEAD, error) {
	b, err := aes.NewCipher(l.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}
func (l *Local) List(ctx context.Context, tenant string) ([]Item, error) {
	rows, err := l.Store.ListSecrets(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		var meta map[string]string
		_ = json.Unmarshal([]byte(r.Metadata), &meta)
		out = append(out, Item{ID: r.ID, TenantID: r.TenantID, Name: r.Name, Backend: "local-encrypted", Metadata: meta, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}
func (l *Local) Put(ctx context.Context, tenant, name, value string, meta map[string]string) (Item, error) {
	a, err := l.cipher()
	if err != nil {
		return Item{}, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return Item{}, err
	}
	sealed := a.Seal(nil, nonce, []byte(value), []byte(tenant+"/"+name))
	raw, _ := json.Marshal(meta)
	now := time.Now().UTC()
	r := &store.SecretRecord{ID: "sec_" + base64.RawURLEncoding.EncodeToString(nonce), TenantID: tenant, Name: name, Ciphertext: sealed, Nonce: nonce, Metadata: string(raw), CreatedAt: now, UpdatedAt: now}
	if err = l.Store.PutSecret(ctx, r); err != nil {
		return Item{}, err
	}
	return Item{ID: r.ID, TenantID: tenant, Name: name, Backend: "local-encrypted", Metadata: meta, CreatedAt: now, UpdatedAt: now}, nil
}
func (l *Local) Delete(ctx context.Context, tenant, name string) error {
	return l.Store.DeleteSecret(ctx, tenant, name)
}
func (l *Local) Resolve(ctx context.Context, tenant, name string) (string, error) {
	r, err := l.Store.GetSecret(ctx, tenant, name)
	if err != nil {
		return "", err
	}
	if r == nil {
		return "", fmt.Errorf("secret %q not found", name)
	}
	a, err := l.cipher()
	if err != nil {
		return "", err
	}
	value, err := a.Open(nil, r.Nonce, r.Ciphertext, []byte(tenant+"/"+name))
	if err != nil {
		return "", errors.New("secret decryption failed")
	}
	return string(value), nil
}

// Vault stores values under secret/data/harnessmesh/<tenant>/<name>. The
// manager never returns the value and only requires VAULT_ADDR and
// VAULT_TOKEN at runtime; credentials are deliberately not persisted by the app.
type Vault struct {
	Address, Token, Mount string
	Client                *http.Client
}

func NewVaultFromEnv() (*Vault, error) {
	addr := strings.TrimRight(strings.TrimSpace(os.Getenv("VAULT_ADDR")), "/")
	tok := strings.TrimSpace(os.Getenv("VAULT_TOKEN"))
	if addr == "" || tok == "" {
		return nil, errors.New("VAULT_ADDR and VAULT_TOKEN are required")
	}

	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid VAULT_ADDR %q: %w", addr, err)
	}

	isProduction := os.Getenv("HARNESSMESH_ENV") == "production"
	if isProduction && u.Scheme != "https" {
		host := u.Hostname()
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return nil, errors.New("production environment requires https:// for VAULT_ADDR")
		}
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	caCertPath := os.Getenv("VAULT_CACERT")
	if caCertPath == "" {
		caCertPath = os.Getenv("HARNESSMESH_VAULT_CACERT")
	}
	if caCertPath != "" {
		caData, err := os.ReadFile(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("read VAULT_CACERT file %q: %w", caCertPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, fmt.Errorf("no valid certificates found in VAULT_CACERT %q", caCertPath)
		}
		tlsConfig.RootCAs = pool
	}

	transport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	mount := os.Getenv("HARNESSMESH_VAULT_MOUNT")
	if mount == "" {
		mount = "secret"
	}
	return &Vault{
		Address: addr,
		Token:   tok,
		Mount:   mount,
		Client: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
	}, nil
}
func (v *Vault) path(tenant, name string) string {
	return fmt.Sprintf("%s/v1/%s/data/harnessmesh/%s/%s", v.Address, v.Mount, url.PathEscape(tenant), url.PathEscape(name))
}
func (v *Vault) do(ctx context.Context, method, url string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", v.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vault returned HTTP %d", resp.StatusCode)
	}
	return out, nil
}
func (v *Vault) List(ctx context.Context, tenant string) ([]Item, error) {
	out, err := v.do(ctx, "LIST", fmt.Sprintf("%s/v1/%s/metadata/harnessmesh/%s", v.Address, v.Mount, url.PathEscape(tenant)), nil)
	if err != nil {
		return nil, err
	}
	data, _ := out["data"].(map[string]any)
	keys, _ := data["keys"].([]any)
	items := make([]Item, 0, len(keys))
	for _, key := range keys {
		name, _ := key.(string)
		items = append(items, Item{Name: name, TenantID: tenant, Backend: "vault"})
	}
	return items, nil
}
func (v *Vault) Put(ctx context.Context, tenant, name, value string, meta map[string]string) (Item, error) {
	now := time.Now().UTC()
	_, err := v.do(ctx, http.MethodPost, v.path(tenant, name), map[string]any{"data": map[string]any{"value": value, "metadata": meta}})
	if err != nil {
		return Item{}, err
	}
	return Item{TenantID: tenant, Name: name, Backend: "vault", Metadata: meta, CreatedAt: now, UpdatedAt: now}, nil
}
func (v *Vault) Delete(ctx context.Context, tenant, name string) error {
	_, err := v.do(ctx, http.MethodDelete, v.path(tenant, name), nil)
	return err
}
func (v *Vault) Resolve(ctx context.Context, tenant, name string) (string, error) {
	out, err := v.do(ctx, http.MethodGet, v.path(tenant, name), nil)
	if err != nil {
		return "", err
	}
	data, _ := out["data"].(map[string]any)
	nested, _ := data["data"].(map[string]any)
	value, _ := nested["value"].(string)
	if value == "" {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return value, nil
}
