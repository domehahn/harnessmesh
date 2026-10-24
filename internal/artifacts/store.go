// Package artifacts provides a tenant-scoped storage boundary for evidence,
// reports and other large immutable payloads. The interface is deliberately
// compatible with filesystem, S3 and R2 implementations.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Object struct {
	TenantID    string
	Key         string
	Size        int64
	SHA256      string
	ContentType string
	CreatedAt   time.Time
}

type Store interface {
	Put(context.Context, string, string, io.Reader) (Object, error)
	Get(context.Context, string, string) (io.ReadCloser, Object, error)
	Delete(context.Context, string, string) error
}

// LocalStore is a secure development/single-node backend. Objects are stored
// below tenant directories and written atomically, so readers never observe a
// partially written artifact. A production S3/R2 adapter can implement Store
// without changing callers.
type LocalStore struct{ root string }

func NewLocalStore(root string) (*LocalStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("artifact root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &LocalStore{root: root}, nil
}
func safePart(value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
		return errors.New("invalid artifact path component")
	}
	return nil
}
func (s *LocalStore) path(tenant, key string) (string, error) {
	if s == nil {
		return "", errors.New("artifact store is nil")
	}
	if err := safePart(tenant); err != nil {
		return "", err
	}
	if key == "" || filepath.IsAbs(key) || strings.Contains(key, "..") || strings.ContainsAny(key, "\\") {
		return "", errors.New("invalid artifact key")
	}
	p := filepath.Join(s.root, tenant, key)
	rel, err := filepath.Rel(s.root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact path escapes root")
	}
	return p, nil
}
func (s *LocalStore) Put(ctx context.Context, tenant, key string, r io.Reader) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	p, err := s.path(tenant, key)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return Object{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".artifact-")
	if err != nil {
		return Object{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return Object{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Object{}, err
	}
	if err := os.Rename(tmpName, p); err != nil {
		return Object{}, err
	}
	return Object{TenantID: tenant, Key: key, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), CreatedAt: time.Now().UTC()}, nil
}
func (s *LocalStore) Get(ctx context.Context, tenant, key string) (io.ReadCloser, Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, Object{}, err
	}
	p, err := s.path(tenant, key)
	if err != nil {
		return nil, Object{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, Object{}, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, Object{}, err
	}
	return f, Object{TenantID: tenant, Key: key, Size: st.Size(), CreatedAt: st.ModTime().UTC()}, nil
}
func (s *LocalStore) Delete(ctx context.Context, tenant, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := s.path(tenant, key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func Verify(r io.Reader, expected string) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if expected != "" && !strings.EqualFold(got, expected) {
		return "", fmt.Errorf("artifact checksum mismatch: got %s", got)
	}
	return got, nil
}
