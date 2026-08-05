package mafia

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

var (
	ErrNotFound      = httpx.ErrNotFound
	ErrNotWaiting    = httpx.NewError(http.StatusConflict, "match_not_open", "This table is no longer open to join.")
	ErrAlreadyJoined = httpx.NewError(http.StatusConflict, "already_joined", "You are already seated at this table.")
	ErrSameOwner     = httpx.NewError(http.StatusConflict, "same_owner", "You cannot join a table created by your own account.")
	ErrTableFull     = httpx.NewError(http.StatusConflict, "table_full", "This table is full.")
	ErrNotPlayer     = httpx.NewError(http.StatusForbidden, "not_in_match", "Your agent is not seated at this table.")
	// ErrNotHouseAgent guards the gate-bypassing house-seat path: it is returned when
	// JoinHouseSeat is called with an agent that is not on the declared house-bot
	// allowlist. Not reachable from any HTTP route — it means a server-side caller
	// tried to fill a seat with something that is not a seeded house bot.
	ErrNotHouseAgent = httpx.NewError(http.StatusInternalServerError, "not_house_agent", "Only seeded house bots may fill a table seat.")
	ErrNotActive     = httpx.NewError(http.StatusConflict, "match_not_active", "This match is not active.")
	ErrBusy          = httpx.NewError(http.StatusConflict, "match_busy", "The match is being updated; retry shortly.")
	// ErrConcurrentUpdate: a racing writer advanced the match first (the
	// UNIQUE(match_id,seq) event-log constraint rejected this write). The lockless
	// timeout path treats it as a no-op rather than a hard error.
	ErrConcurrentUpdate = httpx.NewError(http.StatusConflict, "concurrent_update", "The match advanced concurrently; retry.")
	ErrNotCreator       = httpx.NewError(http.StatusForbidden, "not_creator", "Only the table creator can cancel a waiting lobby entry.")
	ErrIllegalAction    = httpx.NewError(http.StatusBadRequest, "illegal_action", "That action is not legal in the current phase.")
	// ErrStalePhase rejects an action computed for a phase that has already resolved
	// (the game advanced to a new day/phase). Once a phase is DONE its late actions
	// must not be absorbed into the current round.
	ErrStalePhase = httpx.NewError(http.StatusConflict, "stale_phase", "That round has already ended; act on the current phase.")
	// ErrSignatureRequired / ErrBadSignature: the agent registered an Ed25519
	// signing key, so a request-path move must carry a valid signature over the
	// canonical (match, day, seat, action) message (per-move non-repudiation).
	ErrSignatureRequired = httpx.NewError(http.StatusBadRequest, "signature_required", "This agent registered a signing key; the move must be signed.")
	ErrBadSignature      = httpx.NewError(http.StatusForbidden, "bad_signature", "Move signature verification failed.")
)
