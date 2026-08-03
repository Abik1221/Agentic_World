package payout

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

var (
	ErrTooSmall          = httpx.NewError(http.StatusBadRequest, "withdrawal_too_small", "Below the minimum withdrawal, or fees exceed the amount.")
	ErrInsufficient      = httpx.NewError(http.StatusPaymentRequired, "insufficient_winnings", "You can only cash out net winnings, and not more than your balance.")
	ErrNoKYC             = httpx.NewError(http.StatusForbidden, "kyc_required", "Complete payout onboarding (Stripe Connect) before withdrawing.")
	ErrNoWallet          = httpx.NewError(http.StatusForbidden, "wallet_required", "Connect a Solana wallet before withdrawing.")
	ErrWalletNotVerified = httpx.NewError(http.StatusForbidden, "wallet_not_verified", "Verify ownership of your withdrawal wallet before withdrawing.")
	ErrFlagged           = httpx.NewError(http.StatusForbidden, "account_flagged", "Withdrawals are paused while your account is under review.")
	ErrDebt              = httpx.NewError(http.StatusConflict, "outstanding_debt", "Clear your outstanding chargeback debt before withdrawing.")
	ErrNotFound          = httpx.ErrNotFound
	ErrClearing          = httpx.NewError(http.StatusConflict, "clearing_period", "The withdrawal is still in its clearing window; try again later.")
	ErrBadState          = httpx.NewError(http.StatusConflict, "bad_state", "This withdrawal is not in a state that allows that action.")
	ErrForbiddenSelf     = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this agent.")
	ErrSelfApproval      = httpx.NewError(http.StatusForbidden, "self_approval_forbidden", "You cannot approve your own withdrawal; a different admin must review it.")
	ErrVelocity          = httpx.NewError(http.StatusTooManyRequests, "withdrawal_velocity", "You've reached the withdrawal limit for now; try again later.")
	ErrAddressCooldown   = httpx.NewError(http.StatusConflict, "wallet_cooldown", "Your withdrawal wallet was changed recently; withdrawals to it are on a short security hold.")
)

// ErrTreasuryUnfunded is returned to an ADMIN approving a cash-out the payout wallet
// cannot currently settle — not enough USDC in the float, or not enough SOL to pay the
// transaction fee.
//
// It exists because the alternative is worse for everyone. Without it the approval
// succeeds, the broadcast fails, and the user is notified that their withdrawal FAILED
// for what was purely an operational shortfall on our side — recorded against their
// payout history, and needing a manual retry. Refusing at the click keeps the
// withdrawal in `requested`, where it is still approvable the moment the float is
// topped up, and tells the operator which asset to move.
//
// The reason is appended per-call (see unfundedError) because "top up USDC" and "fund
// the wallet with SOL" are different actions and a generic message would send the
// operator to move the wrong one.
func unfundedError(reason string) error {
	return httpx.NewError(http.StatusConflict, "treasury_unfunded",
		"Not approved — "+reason+". The withdrawal is unchanged and can be approved once that is done.")
}
