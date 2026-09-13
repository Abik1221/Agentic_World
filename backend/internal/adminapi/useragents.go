package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// The operator's view of ONE developer's agents AND THE GUARDRAILS THEY RUN UNDER.
//
// Nothing showed these. UserDetail counts agents and totals their money; the wallet view
// shows where the coins sit. But the limits — how much one match may risk, the daily and
// session loss stops, how many matches may run at once, the cooldown after a losing run —
// existed only on the agents row and were readable only by the owner.
//
// That gap turned every support conversation about behaviour into guesswork. "Why did my
// agent stop playing?" is almost always a guardrail doing its job (a daily loss stop hit,
// a cooldown running, no coins left to cover a stake), and an operator with no way to see
// the limits cannot say so. Worse, the answer they CAN see — a balance, sitting there
// unspent — points the wrong way entirely and invites them to look for a fault that does
// not exist. (min_wallet_balance is a soft UI floor only — sit needs balance ≥ stake.)
//
// READ ONLY, deliberately. An operator seeing a developer's risk settings is support; an
// operator CHANGING them is deciding how much of someone else's money to stake. The Super
// Admin already has the levers it should have (suspend, freeze, adjust), and each of those
// is audited. Nothing here writes.

// AgentGuardrails is one agent, its status, and the limits it plays under.
type AgentGuardrails struct {
	Agent  string `json:"agent"`
	Name   string `json:"name,omitempty"`
	Slug   string `json:"slug,omitempty"`
	Status string `json:"status"`
	// VerificationLevel is how far the agent got through certification.
	VerificationLevel string    `json:"verification_level,omitempty"`
	Framework         string    `json:"framework,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	// LastSeenAt is the last time this agent was connected, when known. An agent that has
	// never connected is the other common answer to "why is nothing happening", and it is
	// indistinguishable from a guardrail block without this.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`

	// The guardrails themselves, all in coins except the counts and seconds. Named exactly
	// as the developer sees them on /strategy, so an operator reading a ticket and the
	// developer who filed it are looking at the same words.
	CoinLimitPerMatch    int64 `json:"coin_limit_per_match"`
	MaxBid               int64 `json:"max_bid"`
	MinWalletBalance     int64 `json:"min_wallet_balance"`
	DailyLossLimit       int64 `json:"daily_loss_limit"`
	SessionLossLimit     int64 `json:"session_loss_limit"`
	MaxConcurrentMatches int   `json:"max_concurrent_matches"`
	CooldownLosses       int   `json:"cooldown_losses"`
	CooldownSeconds      int   `json:"cooldown_seconds"`
	AutoJoin             bool  `json:"auto_join"`

	// Live usage against those limits, so a limit can be read as "is this BITING right
	// now" rather than as a number in isolation. This is the difference between an
	// operator saying "you have a 500-coin daily stop" and "you have a 500-coin daily stop
	// and you are 500 coins down today, which is why it stopped".
	LossToday     int64 `json:"loss_today"`
	LossSession   int64 `json:"loss_session"`
	ActiveMatches int   `json:"active_matches"`
	Balance       int64 `json:"balance"`
}

// Blocked reports whether any guardrail is currently stopping this agent from entering a
// match, and which one.
//
// Computed here rather than left to the reader: the whole value of this surface is answering
// "why is nothing happening", and an operator should not have to compare six pairs of
// numbers to get there. Empty means nothing is blocking.
func (a AgentGuardrails) Blocked() string {
	switch {
	case a.Status != "active":
		return "agent is " + a.Status
	case a.DailyLossLimit > 0 && a.LossToday >= a.DailyLossLimit:
		return "daily loss limit reached"
	case a.SessionLossLimit > 0 && a.LossSession >= a.SessionLossLimit:
		return "session loss limit reached"
	case a.MaxConcurrentMatches > 0 && a.ActiveMatches >= a.MaxConcurrentMatches:
		return "already at its concurrent-match limit"
	case a.Balance <= 0:
		return "no coins allocated to this agent"
	}
	return ""
}

// UserAgentsDetail is every agent the account owns, with its guardrails.
type UserAgentsDetail struct {
	User   string            `json:"user"`
	Agents []AgentGuardrails `json:"agents"`
}

// AgentsRepo is the extra read access this surface needs. Separate from Repo so an
// implementation can adopt it independently.
type AgentsRepo interface {
	UserAgents(ctx context.Context, userPublicID string) ([]AgentGuardrails, bool, error)
}

// SetAgentsRepo wires the per-user agent reads. Optional: without it the route answers 503
// rather than 404, because "this feature is not wired" and "this user has no agents" must
// not look the same to an operator.
func (h *Handler) SetAgentsRepo(a AgentsRepo) { h.agentsRepo = a }

// registerAgents mounts the per-user agent route. Called from Register.
func (h *Handler) registerAgents(r chi.Router, guard func(http.Handler) http.Handler) {
	r.With(guard).Get("/v1/admin/users/{id}/agents", h.userAgents)
}

func (h *Handler) userAgents(w http.ResponseWriter, r *http.Request) {
	if h.agentsRepo == nil {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "agents_view_unavailable",
			"The per-user agent view is not wired in this deployment."))
		return
	}
	id := chi.URLParam(r, "id")
	agents, found, err := h.agentsRepo.UserAgents(r.Context(), id)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	if agents == nil {
		agents = []AgentGuardrails{} // always marshal as [], never null
	}
	// Never cached: a guardrail an operator is reading to explain a block has to be the
	// current one, and these change the moment the developer saves.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, UserAgentsDetail{User: id, Agents: agents})
}
