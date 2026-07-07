package identity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestXClaimRoutesFailClosedWhenDisabled guards C2: with no real X verifier
// (prod), the tweet-claim register/verify endpoints must refuse (503) and steer
// callers to email/password — never fall through to the dev auto-approve verifier.
func TestXClaimRoutesFailClosedWhenDisabled(t *testing.T) {
	h := &Handler{xClaim: false} // svc/authn unused: the guard returns before them
	cases := []struct {
		name string
		fn   http.HandlerFunc
	}{
		{"register", h.register},
		{"verify", h.verify},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/register", nil)
		tc.fn(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: code = %d, want 503 when X-claim disabled", tc.name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "x_onboarding_unavailable") {
			t.Errorf("%s: body should carry x_onboarding_unavailable, got %s", tc.name, rec.Body.String())
		}
	}
}

// When enabled, the guard must NOT block — register proceeds past it (and here
// fails at body decode with 400, proving the guard let it through, not 503).
func TestXClaimGuardPassesWhenEnabled(t *testing.T) {
	h := &Handler{xClaim: true}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/register", nil)
	h.register(rec, req)
	if rec.Code == http.StatusServiceUnavailable {
		t.Errorf("guard must not 503 when X-claim is enabled (got %d)", rec.Code)
	}
}
