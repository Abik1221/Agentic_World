package middleware

import (
	"net/http"
)

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
				if allowAll || ok {
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
