package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/verification"
	"github.com/go-chi/chi/v5"
)

// VerificationReviewer performs the review that "Agent flagged for review" names.
//
// A narrow port, satisfied by *verification.Service, so adminapi depends on the action and
// not on the detector.
type VerificationReviewer interface {
	Review(ctx context.Context, agentPublicID, reviewedBy, reason string) error
}

// SetVerificationReviewer wires the review action. Optional: without it the route answers
// 503 rather than 404, so "this deployment cannot review" and "no such agent" stay
// distinguishable — the same rule the per-user agent view follows.
func (h *Handler) SetVerificationReviewer(v VerificationReviewer) { h.reviewer = v }

func (h *Handler) registerVerification(r chi.Router, guard func(http.Handler) http.Handler) {
	r.With(guard).Post("/v1/admin/agents/{id}/verification-review", h.reviewAgentVerification)
}

// reviewAgentVerification clears one agent's timing slate.
//
// # Why this route has to exist
//
// The timing detector refuses an agent with "Agent flagged for review:
// high_human_likelihood", and before this there was no reviewer: no endpoint, no command,
// and nothing anywhere that deleted a timing sample. The refusal was therefore permanent,
// and permanent in a way the agent could not work off — the verdict is computed from its
// own recent samples, samples come only from playing, and a flagged agent may not play.
// Not ranked, not a private room, and not sandbox either, since CreateSandbox consults the
// same gate. The documented alternative, a completion-binding proof outranking the timing
// guess, needs 90% of the agent's WHOLE history bound, which an agent that already has
// unbound history cannot reach.
//
// So a false positive ended the agent, and false positives are cheap: a provider outage, a
// rate limit or an unset API key makes an honest agent answer slowly and erratically for
// twenty turns, which is the exact shape the detector looks for.
//
// A review is NOT an exemption. It marks an instant; the detector then judges the agent
// only on what it does afterwards, by the same rule as everyone else. A human really
// playing by hand is flagged again within twenty moves, and there is no per-agent bypass
// here for a later change to widen.
func (h *Handler) reviewAgentVerification(w http.ResponseWriter, r *http.Request) {
	if h.reviewer == nil {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "verification_review_unavailable",
			"Verification review is not wired in this deployment."))
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	// A body is optional in shape but not in content: decode failures fall through to the
	// empty-reason check below rather than 400-ing on a missing body, so the one error an
	// operator can actually act on is the one they see.
	_ = json.NewDecoder(r.Body).Decode(&in)
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "reason_required",
			"Say why this agent is being cleared — a review with no stated reason is indistinguishable from an accident later."))
		return
	}

	// Who decided. The platform Ed25519 token carries no user, so it is named as itself
	// rather than recorded as an empty string that would read as "unknown operator".
	//
	// PrincipalFromContext returns a POINTER and nil when the request carried no principal.
	// Dereferencing it directly panicked the handler — a 500 on an audited route that
	// relaxes a fraud control, which is the worst place to learn about a nil.
	reviewedBy := "platform-token"
	if p := auth.PrincipalFromContext(r.Context()); p != nil && p.UserPublicID != "" {
		reviewedBy = p.UserPublicID
	}

	id := chi.URLParam(r, "id")
	if err := h.reviewer.Review(r.Context(), id, reviewedBy, reason); err != nil {
		if errors.Is(err, verification.ErrAgentNotFound) {
			httpx.Error(w, httpx.ErrNotFound)
			return
		}
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"agent": id, "reviewed_by": reviewedBy, "reason": reason,
	})
}
