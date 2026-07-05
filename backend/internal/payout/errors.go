package payout

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

var (
	ErrTooSmall      = httpx.NewError(http.StatusBadRequest, "withdrawal_too_small", "Below the minimum withdrawal, or fees exceed the amount.")
	ErrInsufficient  = httpx.NewError(http.StatusPaymentRequired, "insufficient_winnings", "You can only cash out net winnings, and not more than your balance.")
	ErrNoKYC         = httpx.NewError(http.StatusForbidden, "kyc_required", "Complete payout onboarding (Stripe Connect) before withdrawing.")
	ErrFlagged       = httpx.NewError(http.StatusForbidden, "account_flagged", "Withdrawals are paused while your account is under review.")
	ErrDebt          = httpx.NewError(http.StatusConflict, "outstanding_debt", "Clear your outstanding chargeback debt before withdrawing.")
	ErrNotFound      = httpx.ErrNotFound
	ErrClearing      = httpx.NewError(http.StatusConflict, "clearing_period", "The withdrawal is still in its clearing window; try again later.")
	ErrBadState      = httpx.NewError(http.StatusConflict, "bad_state", "This withdrawal is not in a state that allows that action.")
	ErrForbiddenSelf = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this agent.")
)
