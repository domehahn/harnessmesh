package webui

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed index.html app.js styles.css
var assets embed.FS

// Handler serves the authenticated SaaS control-plane shell. Data remains
// server-authoritative and is fetched by the browser from existing APIs.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/dashboard/assets/") {
			name := strings.TrimPrefix(r.URL.Path, "/dashboard/assets/")
			data, err := assets.ReadFile(name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if strings.HasSuffix(name, ".js") {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			} else {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			}
			w.Header().Set("Cache-Control", "public, max-age=300")
			_, _ = w.Write(data)
			return
		}
		if r.URL.Path != "/dashboard" && r.URL.Path != "/dashboard/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		data, err := assets.ReadFile("index.html")
		if err != nil {
			http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(data)
	})
}

// AssetHandler serves only embedded dashboard assets.
func AssetHandler() http.Handler { return http.FileServer(http.FS(assets)) }
