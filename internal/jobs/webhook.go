package jobs

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/store"
)

// WebhookHandler converts authenticated external events into durable jobs.
// It never executes a job inline. Signature format: sha256=<hex HMAC>.
type WebhookHandler struct {
	Runtime Runtime
	Secret  []byte
	MaxBody int64
}

func (h WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(h.Secret) == 0 {
		http.Error(w, "webhook disabled", http.StatusNotFound)
		return
	}
	bodyLimit := h.MaxBody
	if bodyLimit <= 0 {
		bodyLimit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil || int64(len(body)) > bodyLimit {
		http.Error(w, "invalid body", http.StatusRequestEntityTooLarge)
		return
	}
	if !verifySignature(body, r.Header.Get("X-HarnessMesh-Signature"), h.Secret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	tenant := strings.TrimSpace(r.Header.Get("X-HarnessMesh-Tenant"))
	kind := strings.Trim(strings.TrimSpace(r.URL.Path), "/")
	if tenant == "" || kind == "" {
		http.Error(w, "tenant and event kind are required", http.StatusBadRequest)
		return
	}
	job := &store.DurableJob{ID: fmt.Sprintf("webhook_%d", nowUnixNano()), Tenant: tenant, Kind: kind, Payload: body}
	if err := h.Runtime.Submit(r.Context(), job); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "job_id": job.ID})
}

var nowUnixNano = func() int64 { return timeNow().UnixNano() }

func verifySignature(body []byte, header string, secret []byte) bool {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(want) != sha256.Size {
		return false
	}
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write(body)
	got := h.Sum(nil)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Small indirection keeps webhook ID generation replaceable in tests.
var timeNow = func() time.Time { return time.Now().UTC() }
