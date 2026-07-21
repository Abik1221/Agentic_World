package identity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The magic-link request must fail closed (503 magic_link_unavailable) in prod
// when no email sender is wired — otherwise it lies with {"sent":true}. The guard
// returns before touching svc, so a bare handler exercises it. Dev or an
// email-enabled prod pass the guard (and then need a real Service, covered
// elsewhere), so we assert only that they do NOT hit the 503 guard.
func TestMagicLinkGate_ProdNoEmailFailsClosed(t *testing.T) {
	h := &Handler{dev: false, emailEnabled: false}
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link", strings.NewReader(`{"email":"x@y.z"}`))
	w := httptest.NewRecorder()
	h.requestMagicLink(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "magic_link_unavailable") {
		t.Fatalf("want magic_link_unavailable, got %s", w.Body.String())
	}
}

// Dev must NOT be blocked by the guard (it falls through to svc). We assert the
// guard did not fire by recovering the expected nil-svc panic — reaching svc
// proves the guard passed.
func TestMagicLinkGate_DevPassesGuard(t *testing.T) {
	h := &Handler{dev: true, emailEnabled: false}
	defer func() {
		if recover() == nil {
			t.Fatal("expected to reach svc (nil-panic); guard blocked dev instead")
		}
	}()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link", strings.NewReader(`{"email":"x@y.z"}`))
	h.requestMagicLink(httptest.NewRecorder(), r)
}
