package middleware

import (
	"net/http"
	"strings"
)

// isPublicSpectate reports whether a request targets a PUBLIC, credential-less
// spectating/read endpoint that anyone may watch from any origin — the Live Arena
// surface (live match lists, live stats, and the SSE watch / replay / economy
// views for every game). These expose only public data and never read a cookie or
// bearer, so serving them cross-origin is safe and lets logged-out viewers watch
// live agent games from anywhere. Everything else stays on the strict allowlist.
func isPublicSpectate(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		return false
	}
	switch path {
	case "/v1/matches/live", "/v1/stats/live", "/v1/mafia/live", "/v1/monopoly/live":
		return true
	}
	// Per-match spectator views: /v1/{match,mafia,monopoly}/{id}/{watch,replay,economy}.
	return strings.HasSuffix(path, "/watch") ||
		strings.HasSuffix(path, "/replay") ||
		strings.HasSuffix(path, "/economy")
}

// CORS applies a strict origin allowlist for browser clients (the dashboard and
// public site). Non-allowlisted origins simply receive no CORS headers (the
// browser blocks them); same-origin and non-browser clients (agents) are
// unaffected. Public read APIs are served to any origin via "*" only if the
// allowlist explicitly contains it.
func CORS(allowed []string) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	allowAll := false
	for _, o := range allowed {
		if o == "*" {
			allowAll = true
		}
		set[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				_, ok := set[origin]
				// Public spectating endpoints are readable from ANY origin (see
				// isPublicSpectate) so the Live Arena works for logged-out viewers
				// regardless of where the client is served; all other endpoints use
				// the strict allowlist. Credential-less + public data, so echoing the
				// origin here leaks nothing (no Allow-Credentials is ever set).
				public := isPublicSpectate(r.Method, r.URL.Path)
				if allowAll || ok || public {
					allow := origin
					if allowAll {
						allow = "*"
					}
					h := w.Header()
					h.Set("Access-Control-Allow-Origin", allow)
					h.Add("Vary", "Origin")
					h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-ID")
					h.Set("Access-Control-Max-Age", "600")
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
