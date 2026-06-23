package ledger

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// Domain errors as *httpx.APIError so handlers can return them directly (httpx
// imports no module → no cycle). ErrUnbalanced / ErrInvariant are 500s: they
// signal a programming bug, never a client mistake, and must page on sight.
var (
	ErrEmpty          = httpx.NewError(http.StatusInternalServerError, "ledger_empty", "Refusing to post a transaction with no postings.")
	ErrUnbalanced     = httpx.NewError(http.StatusInternalServerError, "ledger_unbalanced", "Refusing to post an unbalanced transaction.")
	ErrInvariant      = httpx.NewError(http.StatusInternalServerError, "ledger_invariant", "A protected wallet would breach its non-negative invariant.")
	ErrInsufficient   = httpx.NewError(http.StatusPaymentRequired, "insufficient_balance", "Insufficient wallet balance.")
	ErrWalletNotFound = httpx.NewError(http.StatusNotFound, "wallet_not_found", "Wallet not found.")
)
