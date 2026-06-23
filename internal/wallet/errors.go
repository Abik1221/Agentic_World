package wallet

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// ErrInvalidBid rejects a non-positive bid before any limit check.
var ErrInvalidBid = httpx.NewError(http.StatusBadRequest, "invalid_bid", "Bid must be a positive number of coins.")

// limit codes — each maps to a distinct, machine-readable reason for a blocked join.
const (
	limitMinBalance = "min_wallet_balance"
	limitPerMatch   = "coin_limit_per_match"
	limitDailyLoss  = "daily_loss_limit"
	limitSession    = "session_loss_limit"
	limitCooldown   = "cooldown"
	limitConcurrent = "max_concurrent_matches"
	limitMaxBid     = "max_bid"
)

// blockBalance is the insufficient-funds block (402); the rest are policy blocks (409).
func blockBalance(msg string, details map[string]any) *httpx.APIError {
	return httpx.NewError(http.StatusPaymentRequired, "insufficient_balance", msg).WithDetails(details)
}

func block(limit, msg string, details map[string]any) *httpx.APIError {
	if details == nil {
		details = map[string]any{}
	}
	details["limit"] = limit
	return httpx.NewError(http.StatusConflict, "limit_"+limit, msg).WithDetails(details)
}
