package solanadeposit

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// ErrNotFound is returned when a deposit session does not exist (or isn't owned
// by the caller).
var ErrNotFound = httpx.NewError(http.StatusNotFound, "deposit_not_found", "Deposit session not found.")

func errInvalid(msg string) error {
	return httpx.NewError(http.StatusBadRequest, "invalid_request", msg)
}
