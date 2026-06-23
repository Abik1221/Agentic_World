package match

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// Domain errors as *httpx.APIError for direct handler return (httpx does not
// import match, so no cycle).
var (
	ErrNotFound      = httpx.ErrNotFound
	ErrNotWaiting    = httpx.NewError(http.StatusConflict, "match_not_open", "This match is no longer open to join.")
	ErrAlreadyJoined = httpx.NewError(http.StatusConflict, "already_joined", "You are already in this match.")
	ErrSameOwner     = httpx.NewError(http.StatusConflict, "same_owner", "You cannot join a match created by your own account.")
	ErrNotPlayer     = httpx.NewError(http.StatusForbidden, "not_in_match", "Your agent is not a player in this match.")
	ErrNotActive     = httpx.NewError(http.StatusConflict, "match_not_active", "This match is not active.")
	ErrWrongRound    = httpx.NewError(http.StatusConflict, "wrong_round", "The submitted round is not the current round.")
	ErrBusy          = httpx.NewError(http.StatusConflict, "match_busy", "The match is being updated; retry shortly.")
	ErrSignatureRequired = httpx.NewError(http.StatusBadRequest, "signature_required", "This agent has a signing key; moves must be signed.")
	ErrBadSignature      = httpx.NewError(http.StatusForbidden, "bad_signature", "Move signature is invalid for (match, round, seat, card).")
)

func errIllegalCard(msg string) *httpx.APIError {
	return httpx.NewError(http.StatusBadRequest, "illegal_action", msg)
}
