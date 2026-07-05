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
	ErrNotActive     = httpx.NewError(http.StatusConflict, "match_not_active", "This match is not active.")
	ErrBusy          = httpx.NewError(http.StatusConflict, "match_busy", "The match is being updated; retry shortly.")
	ErrNotCreator    = httpx.NewError(http.StatusForbidden, "not_creator", "Only the table creator can cancel a waiting lobby entry.")
	ErrIllegalAction = httpx.NewError(http.StatusBadRequest, "illegal_action", "That action is not legal in the current phase.")
)
