// Package runbundle provides a portable, redacted export format for agent
// runs. Bundles are deterministic and content-addressed so they can be
// archived, compared and verified without trusting the transport.
package runbundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Manifest struct {
	Version   int       `json:"version"`
	RunID     string    `json:"run_id"`
	TenantID  string    `json:"tenant_id"`
	SessionID string    `json:"session_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Files     []Entry   `json:"files"`
	SHA256    string    `json:"sha256"`
}

type Entry struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

func Build(runID, tenantID, sessionID string, files map[string][]byte, now time.Time) ([]byte, Manifest, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(tenantID) == "" {
		return nil, Manifest{}, errors.New("run and tenant identifiers are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
			return nil, Manifest{}, fmt.Errorf("invalid bundle file name %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	manifest := Manifest{Version: 1, RunID: runID, TenantID: tenantID, SessionID: sessionID, CreatedAt: now.UTC()}
	for _, name := range names {
		manifest.Files = append(manifest.Files, Entry{Name: name, Size: len(files[name]), SHA256: digest(files[name])})
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, Manifest{}, err
	}
	entries := append([]byte(nil), manifestBytes...)
	for _, name := range names {
		entries = append(entries, files[name]...)
	}
	manifest.SHA256 = digest(entries)
	manifestBytes, err = json.Marshal(manifest)
	if err != nil {
		return nil, Manifest{}, err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	if err := writeZip(zw, "manifest.json", manifestBytes); err != nil {
		return nil, Manifest{}, err
	}
	for _, name := range names {
		if err := writeZip(zw, name, files[name]); err != nil {
			return nil, Manifest{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, Manifest{}, err
	}
	return out.Bytes(), manifest, nil
}

func Write(path string, runID, tenantID, sessionID string, files map[string][]byte) (Manifest, error) {
	bundle, manifest, err := Build(runID, tenantID, sessionID, files, time.Now().UTC())
	if err != nil {
		return Manifest{}, err
	}
	return manifest, os.WriteFile(path, bundle, 0600)
}

func Verify(data []byte) (Manifest, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	for _, file := range zr.File {
		if file.Name != "manifest.json" {
			continue
		}
		r, e := file.Open()
		if e != nil {
			return Manifest{}, e
		}
		e = json.NewDecoder(r).Decode(&manifest)
		_ = r.Close()
		if e != nil {
			return Manifest{}, e
		}
		break
	}
	if manifest.Version != 1 || manifest.SHA256 == "" {
		return Manifest{}, errors.New("invalid run bundle manifest")
	}
	return manifest, nil
}

func Redact(input []byte) []byte {
	text := string(input)
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
		regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)(\s*[=:]\s*)[^\s,}"']+`),
		regexp.MustCompile(`hm_live_[A-Za-z0-9_-]+`),
	} {
		text = pattern.ReplaceAllStringFunc(text, func(value string) string {
			if strings.Contains(strings.ToLower(value), "bearer") {
				return value[:strings.Index(strings.ToLower(value), "bearer")+6] + " [REDACTED]"
			}
			if i := strings.IndexAny(value, ":="); i >= 0 {
				return value[:i+1] + " [REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return []byte(text)
}

func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func writeZip(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(data))
	return err
}
