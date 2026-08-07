package monopoly

import (
	"errors"
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

var (
	ErrNotFound      = httpx.ErrNotFound
	ErrNotActive     = httpx.NewError(http.StatusConflict, "match_not_active", "This match is not active.")
	ErrNotPlayer     = httpx.NewError(http.StatusForbidden, "not_in_match", "Your agent is not seated at this table.")
	ErrNotYourTurn   = httpx.NewError(http.StatusConflict, "not_your_turn", "It is not your seat's turn to act.")
	ErrBusy          = httpx.NewError(http.StatusConflict, "match_busy", "The match is being updated; retry shortly.")
	ErrIllegalAction = httpx.NewError(http.StatusBadRequest, "illegal_action", "That action is not legal in the current phase.")
	// The engine distinguishes WHY an action was rejected, and these keep that distinction on the
	// wire. Collapsing them all into illegal_action told every agent to go and check the phase —
	// including the ones whose phase was correct and whose field was simply absent or whose cash
	// was simply short. An error that names the wrong cause costs more than a vague one, because
	// it is specific enough to be acted on and wrong.
	ErrBidAmountMissing  = httpx.NewError(http.StatusBadRequest, "bid_amount_missing", "Your bid named no amount. Send a positive \"amount\" alongside the \"bid\" action.")
	ErrInvalidBid        = httpx.NewError(http.StatusBadRequest, "bid_too_low", "Your bid must exceed the current high bid. The auction's high bid is in the view under \"auction\".")
	ErrInsufficientFunds = httpx.NewError(http.StatusBadRequest, "insufficient_funds", "Your seat does not hold enough cash for that action.")
	ErrInvalidProperty   = httpx.NewError(http.StatusBadRequest, "invalid_property", "That property index is not valid for this action. Legal targets are listed in the view under \"legal\".")
	ErrEmptyMessage      = httpx.NewError(http.StatusBadRequest, "empty_message", "The message text is empty.")
	// ErrSignatureRequired / ErrBadSignature: the agent registered an Ed25519
	// signing key, so a request-path move must carry a valid signature over the
	// canonical (match, next_seq, seat, action) message (per-move non-repudiation).
	ErrSignatureRequired = httpx.NewError(http.StatusBadRequest, "signature_required", "This agent registered a signing key; the move must be signed.")
	ErrBadSignature      = httpx.NewError(http.StatusForbidden, "bad_signature", "Move signature verification failed.")
	ErrBadConfig         = httpx.NewError(http.StatusBadRequest, "bad_config", "Invalid table configuration.")
	// Waiting-lobby errors (agent-vs-agent staked tables).
	ErrNotWaiting    = httpx.NewError(http.StatusConflict, "match_not_waiting", "This table is no longer open to join.")
	ErrAlreadyJoined = httpx.NewError(http.StatusConflict, "already_joined", "Your agent is already seated at this table.")
	ErrTableFull     = httpx.NewError(http.StatusConflict, "table_full", "This table is already full.")
	ErrSameOwner     = httpx.NewError(http.StatusConflict, "same_owner", "You already hold a seat at this table.")
	ErrNotCreator    = httpx.NewError(http.StatusForbidden, "not_creator", "Only the table creator can cancel it.")
	// ErrStakesUnavailable rejects a staked Monopoly table while no wallet is wired —
	// so the API never advertises a stake/pool/payout for a game that moves no coins.
	ErrStakesUnavailable = httpx.NewError(http.StatusServiceUnavailable, "stakes_unavailable", "Staked Monopoly is not available yet; create a practice table (no entry fee).")
	// ErrConcurrentUpdate is returned by the repo when a racing writer advanced the
	// match first (UNIQUE(match_id,seq) violation). The service re-reads and retries;
	// it is the optimistic-concurrency signal that makes the Redis lock optional.
	ErrConcurrentUpdate = errors.New("monopoly: concurrent update")
)
