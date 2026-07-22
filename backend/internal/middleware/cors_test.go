package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// corsResp runs one request through the CORS middleware and returns the response
// headers (the only thing these tests assert). It closes the response body so
// callers don't have to.
func corsResp(t *testing.T, allowed []string, method, path, origin string) http.Header {
	t.Helper()
	h := CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	_ = res.Body.Close()
	return res.Header
}

// Public Live Arena spectating must be readable from ANY origin (logged-out
// viewers watching from anywhere), even when the origin is not on the allowlist.
func TestCORS_PublicSpectateAnyOrigin(t *testing.T) {
	allow := []string{"http://localhost:3000"}
	for _, path := range []string{
		"/v1/matches/live", "/v1/stats/live", "/v1/mafia/live", "/v1/monopoly/live",
		"/v1/match/m_1/watch", "/v1/mafia/m_1/watch", "/v1/monopoly/m_1/watch",
		"/v1/mafia/m_1/economy", "/v1/monopoly/m_1/replay",
	} {
		hdr := corsResp(t, allow, http.MethodGet, path, "https://some-other-site.example")
		if got := hdr.Get("Access-Control-Allow-Origin"); got != "https://some-other-site.example" {
			t.Errorf("%s: ACAO = %q, want the request origin (public spectating is open)", path, got)
		}
	}
}

// Credentialed / non-spectating endpoints stay on the strict allowlist: a
// non-allowlisted origin gets no CORS headers.
func TestCORS_NonPublicStrictAllowlist(t *testing.T) {
	allow := []string{"http://localhost:3000"}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/wallet"},
		{http.MethodPost, "/v1/withdrawals"},
		{http.MethodGet, "/v1/match/m_1/state"}, // agent-scoped, not a spectator view
	} {
		hdr := corsResp(t, allow, tc.method, tc.path, "https://evil.example")
		if got := hdr.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s %s: ACAO = %q, want empty (not allowlisted)", tc.method, tc.path, got)
		}
	}
	// An allowlisted origin is always honored.
	hdr := corsResp(t, allow, http.MethodGet, "/v1/wallet", "http://localhost:3000")
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("allowlisted origin: ACAO = %q, want it echoed", got)
	}
}
