package jobs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/store"
)

func TestWebhookEnqueuesOnlySignedTenantJob(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "webhook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret := []byte("webhook-secret")
	h := WebhookHandler{Runtime: Runtime{Store: st, MaxAttempts: 2}, Secret: secret}
	body := []byte(`{"event":"deploy.failed"}`)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/deploy.failed", strings.NewReader(string(body)))
	req.Header.Set("X-HarnessMesh-Tenant", "acme")
	req.Header.Set("X-HarnessMesh-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	jobs, err := st.ListJobs(req.Context(), "acme", "pending", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Kind != "deploy.failed" {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
	bad := httptest.NewRequest(http.MethodPost, "/deploy.failed", strings.NewReader("bad"))
	bad.Header.Set("X-HarnessMesh-Tenant", "acme")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", badRec.Code)
	}
}
