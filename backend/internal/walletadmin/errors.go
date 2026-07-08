package walletadmin

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// Gate rejections (surfaced to the end user by the deposit/withdrawal handlers).
var (
	ErrMaintenance         = httpx.NewError(http.StatusServiceUnavailable, "wallet_maintenance", "Wallet operations are temporarily paused for maintenance.")
	ErrDepositsDisabled    = httpx.NewError(http.StatusForbidden, "deposits_disabled", "Deposits are currently disabled.")
	ErrWithdrawalsDisabled = httpx.NewError(http.StatusForbidden, "withdrawals_disabled", "Withdrawals are currently disabled.")
	ErrBelowMin            = httpx.NewError(http.StatusBadRequest, "below_minimum", "Amount is below the current minimum.")
	ErrAboveMax            = httpx.NewError(http.StatusBadRequest, "above_maximum", "Amount is above the current maximum.")
	ErrFrozen              = httpx.NewError(http.StatusForbidden, "wallet_frozen", "This wallet is frozen. Contact support.")
	ErrUserNotFound        = httpx.NewError(http.StatusNotFound, "user_not_found", "No wallet found for that user.")
)

func errInvalid(msg string) error {
	return httpx.NewError(http.StatusBadRequest, "invalid_request", msg)
}
