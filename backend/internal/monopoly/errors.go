package monopoly

import (
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
	ErrBadConfig     = httpx.NewError(http.StatusBadRequest, "bad_config", "Invalid table configuration.")
)
