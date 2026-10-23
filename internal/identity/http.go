package identity

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/tenant"
)

// Handler exposes the minimal tenant administration API. Authentication is
// always performed against the API-key directory; no tenant path segment is
// trusted without checking the authenticated identity.
type Handler struct{ Service Service }

func NewHandler(s store.IdentityStore) http.Handler { return Handler{Service: Service{Store: s}} }

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	identity, err := h.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r = r.WithContext(tenant.WithIdentity(r.Context(), identity))
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/identity")
	if path == r.URL.Path {
		// Keep compatibility for callers mounting this handler below /v1.
		path = strings.TrimPrefix(r.URL.Path, "/v1/identity")
	}
	path = strings.Trim(path, "/")
	if path == "me" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, identity)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[0] == "tenants" && parts[1] != "" {
		tenantID := parts[1]
		if err := tenant.Authorize(r.Context(), tenantID, "owner", ""); err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Subject string `json:"subject"`
				Role    string `json:"role"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || strings.TrimSpace(req.Subject) == "" {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if req.Role == "" {
				req.Role = "member"
			}
			if err := h.Service.Store.UpsertPrincipal(r.Context(), &store.PrincipalRecord{Subject: req.Subject}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			membership := &store.MembershipRecord{TenantID: tenantID, Subject: req.Subject, Role: req.Role}
			if err := h.Service.Store.SetMembership(r.Context(), membership); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusCreated, membership)
			return
		}
	}
	if len(parts) == 3 && parts[0] == "tenants" && parts[2] == "keys" && r.Method == http.MethodPost {
		tenantID := parts[1]
		if err := tenant.Authorize(r.Context(), tenantID, "owner", ""); err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req struct {
			Subject string `json:"subject"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || strings.TrimSpace(req.Subject) == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		secret, record, err := h.Service.IssueAPIKey(r.Context(), tenantID, req.Subject)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"api_key": secret, "record": record})
		return
	}
	http.NotFound(w, r)
}

func (h Handler) authenticate(r *http.Request) (tenant.Identity, error) {
	key := strings.TrimSpace(r.Header.Get("X-HarnessMesh-API-Key"))
	if key == "" {
		return tenant.Identity{}, tenant.ErrUnauthenticated
	}
	identity, err := h.Service.AuthenticateAPIKey(r.Context(), key)
	if err != nil {
		return tenant.Identity{}, err
	}
	return identity, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
