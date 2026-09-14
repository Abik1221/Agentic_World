package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/verification"
	"github.com/go-chi/chi/v5"
)

// The operator-facing contract of the review route.
//
// This route is what an operator reaches for when a developer says "my agent stopped
// working and the site says it is flagged for review". Before it existed there was no
// review to perform, so the answer was "nothing can be done" — and because a flagged agent
// cannot play at all, not even sandbox, it could never work the flag off either.

type recordingReviewer struct {
	agent, by, reason string
	err               error
	calls             int
}

func (r *recordingReviewer) Review(_ context.Context, agent, by, reason string) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	r.agent, r.by, r.reason = agent, by, reason
	return nil
}

// serve runs one request through the route alone, with no auth middleware — the guard is
// Register's job and is exercised where it is applied. What matters here is the handler's
// own behaviour.
func serve(h *Handler, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/v1/admin/agents/{id}/verification-review", h.reviewAgentVerification)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/agents/agt_1/verification-review",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAReviewNamesTheAgentTheReviewerAndTheReason(t *testing.T) {
	rev := &recordingReviewer{}
	h := &Handler{reviewer: rev}
	w := serve(h, `{"reason":"provider outage, API key was unset"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if rev.agent != "agt_1" {
		t.Fatalf("agent = %q, want agt_1", rev.agent)
	}
	if rev.reason != "provider outage, API key was unset" {
		t.Fatalf("reason = %q — the stated reason must reach the record", rev.reason)
	}
	// No user on the request, so the platform token is named as itself. Recording an empty
	// string would read months later as "unknown operator", which is a different claim.
	if rev.by != "platform-token" {
		t.Fatalf("reviewed_by = %q, want platform-token", rev.by)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["agent"] != "agt_1" {
		t.Fatalf("response did not echo the agent: %v", out)
	}
}

func TestAReviewWithoutAReasonIsRefused(t *testing.T) {
	// A cleared agent with no stated reason is indistinguishable from an accident when
	// someone reads the table later — and this route relaxes a fraud control, so the
	// record is the only thing that makes it accountable.
	for _, body := range []string{`{}`, `{"reason":""}`, `{"reason":"   "}`, ``} {
		rev := &recordingReviewer{}
		w := serve(&Handler{reviewer: rev}, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %q gave status %d, want 400", body, w.Code)
		}
		if rev.calls != 0 {
			t.Fatalf("body %q cleared the agent anyway", body)
		}
	}
}

func TestAnUnknownAgentIsNotFoundRatherThanCleared(t *testing.T) {
	// Silence here would tell an operator a flagged agent had been released when they had
	// simply mistyped the id, and they would stop looking for the real problem.
	rev := &recordingReviewer{err: verification.ErrAgentNotFound}
	w := serve(&Handler{reviewer: rev}, `{"reason":"typo test"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body.String())
	}
}

func TestAnUnwiredDeploymentSaysSoRatherThan404(t *testing.T) {
	// 404 would read as "no such agent" and send the operator hunting for a bad id; this
	// is "this deployment cannot review". Same rule the per-user agent view follows.
	w := serve(&Handler{}, `{"reason":"anything"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", w.Code, w.Body.String())
	}
}

func TestAFailingReviewIsReportedNotSwallowed(t *testing.T) {
	rev := &recordingReviewer{err: errors.New("database is down")}
	w := serve(&Handler{reviewer: rev}, `{"reason":"real attempt"}`)
	if w.Code == http.StatusOK {
		t.Fatal("a failed review answered 200 — the operator would believe the agent was cleared")
	}
}
