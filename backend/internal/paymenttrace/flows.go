// Package paymenttrace is the payment log and the end-to-end flow model built on
// top of it.
//
// A money flow on this platform is a fixed sequence of stages across several
// independent systems — the browser, the user's wallet, the Solana network, a
// background listener, the ledger, the notifier. When someone says "I paid and
// nothing happened", the only useful question is WHICH of those stages the attempt
// reached. Nothing recorded that, so the answer required reading server logs and
// joining three tables by hand.
//
// Two halves:
//
//   - The LOG (payment_events, migration 0069): one row per (flow, ref, stage),
//     upserted, carrying status + detail + timing.
//   - The MODEL (this file): the canonical ordered stage list per flow, with a
//     human sentence for each. Recorded stages are matched against it to produce a
//     timeline where every expected step is present — including the ones that never
//     happened. That is what makes the break point visible rather than inferred
//     from an absence.
//
// The log is DIAGNOSTIC. The ledger is the only authority on money. A missing row
// here means we failed to observe a step, never that coins did or did not move,
// and no money path may fail because a write to this table failed.
package paymenttrace

// Flow kinds.
const (
	FlowDeposit    = "deposit"
	FlowWithdrawal = "withdrawal"
	FlowTopup      = "topup"
)

// Stage statuses.
const (
	StatusOK      = "ok"
	StatusPending = "pending"
	StatusFailed  = "failed"
)

// Deposit stages, in order.
const (
	StageSessionCreated  = "session_created"
	StagePaymentDetected = "payment_detected"
	StageChainFinalized  = "chain_finalized"
	StageCoinsCredited   = "coins_credited"
	StageUserNotified    = "user_notified"
	// Terminal failures. They are not part of the happy path, so they are not in
	// the expected list — they REPLACE the stage that was pending when they fired.
	StageSessionExpired = "session_expired"
	StageChainRejected  = "chain_rejected"
	StageCreditFailed   = "credit_failed"
)

// Withdrawal stages, in order.
const (
	StageRequested        = "requested"
	StageCoinsHeld        = "coins_held"
	StageApproved         = "approved"
	StageBroadcast        = "broadcast"
	StagePaid             = "paid"
	StageWithdrawNotified = "withdraw_notified"
	// Terminal failures.
	StageRejected        = "rejected"
	StageBroadcastFailed = "broadcast_failed"
	StageOnchainFailed   = "onchain_failed"
)

// Top-up stages (card / subscription grant). Short by nature: the provider owns
// everything before the webhook lands.
const (
	StagePaymentReceived = "payment_received"
	StageTopupCredited   = "coins_credited"
	StageTopupNotified   = "user_notified"
)

// StageSpec describes one step of a flow for the UI.
type StageSpec struct {
	Key string `json:"key"`
	// Label is the node caption in the diagram. Short, no jargon.
	Label string `json:"label"`
	// Who owns this step. The single most useful thing to know when a payment is
	// stuck, because it says whether the user, the chain or the platform is the
	// thing to chase. Values: "you" | "your wallet" | "solana" | "pyyol".
	Actor string `json:"actor"`
	// What this step means, in a sentence a non-engineer can act on.
	Description string `json:"description"`
	// StuckHint is shown when the flow is stuck HERE. It must say what to do, not
	// restate the problem.
	StuckHint string `json:"stuck_hint"`
}

// FlowSpec is a flow's full expected path.
type FlowSpec struct {
	Flow   string      `json:"flow"`
	Label  string      `json:"label"`
	Stages []StageSpec `json:"stages"`
}

// depositFlow is the USDC-on-Solana deposit, end to end.
var depositFlow = FlowSpec{
	Flow:  FlowDeposit,
	Label: "Deposit (USDC on Solana)",
	Stages: []StageSpec{
		{
			Key: StageSessionCreated, Label: "Request created", Actor: "pyyol",
			Description: "We generated a payment request with a unique reference and started watching the chain for it.",
			StuckHint:   "Nothing was charged. Start a new deposit.",
		},
		{
			Key: StagePaymentDetected, Label: "Transfer seen", Actor: "your wallet",
			Description: "A transfer carrying this request's reference appeared on Solana.",
			StuckHint: "We have not seen a transfer yet. If your wallet says it sent, it may still be " +
				"propagating — check the signature on an explorer. If you never approved it, nothing was charged.",
		},
		{
			Key: StageChainFinalized, Label: "Confirmed on-chain", Actor: "solana",
			Description: "Solana finalized the transfer. We only credit at finality, because a confirmed-but-not-final transfer can still be rolled back and a credit cannot.",
			StuckHint:   "Solana has seen your transfer but has not finalized it yet. This is the network, not us — it normally clears in under a minute.",
		},
		{
			Key: StageCoinsCredited, Label: "Credits added", Actor: "pyyol",
			Description: "Your treasury balance was credited through the ledger, minus the deposit fee.",
			StuckHint:   "The transfer is final but the credit did not post. This is ours to fix — contact support with the deposit id.",
		},
		{
			Key: StageUserNotified, Label: "You were told", Actor: "pyyol",
			Description: "A confirmation was pushed to your open tabs and written to your notification feed.",
			StuckHint:   "Your credits are safe and in your balance; only the notification failed.",
		},
	},
}

// withdrawalFlow is the USDC cash-out, end to end.
var withdrawalFlow = FlowSpec{
	Flow:  FlowWithdrawal,
	Label: "Withdrawal (USDC on Solana)",
	Stages: []StageSpec{
		{
			Key: StageRequested, Label: "Requested", Actor: "you",
			Description: "You asked to cash out. We quoted the fee and the net amount at this moment and hold you to that quote.",
			StuckHint:   "The request was not accepted. Nothing left your balance.",
		},
		{
			Key: StageCoinsHeld, Label: "Credits held", Actor: "pyyol",
			Description: "The credits left your spendable balance and are reserved for this payout. They are still yours — a failed payout returns them.",
			StuckHint:   "The hold did not apply, so nothing was reserved and no payout will run.",
		},
		{
			Key: StageApproved, Label: "Approved", Actor: "pyyol",
			Description: "A reviewer cleared the payout. Withdrawals wait for review by design — it is the control that stops a compromised account draining.",
			StuckHint:   "Awaiting review. Your credits are held and safe; nothing is lost while this waits.",
		},
		{
			Key: StageBroadcast, Label: "Sent to Solana", Actor: "pyyol",
			Description: "The USDC transfer was signed and broadcast to your verified wallet.",
			StuckHint:   "Approved but not yet broadcast. Your credits remain held — contact support with the withdrawal id.",
		},
		{
			Key: StagePaid, Label: "Confirmed on-chain", Actor: "solana",
			Description: "The transfer finalized on Solana and the held credits were burned.",
			StuckHint:   "Broadcast and awaiting finality. Check the signature on an explorer — the funds are in flight.",
		},
		{
			Key: StageWithdrawNotified, Label: "You were told", Actor: "pyyol",
			Description: "A confirmation was pushed to your open tabs and written to your notification feed.",
			StuckHint:   "The payout completed; only the notification failed.",
		},
	},
}

// topupFlow is a card charge or subscription grant landing as credits.
var topupFlow = FlowSpec{
	Flow:  FlowTopup,
	Label: "Card / subscription credits",
	Stages: []StageSpec{
		{
			Key: StagePaymentReceived, Label: "Payment received", Actor: "pyyol",
			Description: "The provider told us your payment settled.",
			StuckHint:   "We have not been told your payment settled. If your card was charged, contact support with the date and amount.",
		},
		{
			Key: StageTopupCredited, Label: "Credits added", Actor: "pyyol",
			Description: "Your treasury balance was credited through the ledger.",
			StuckHint:   "The payment settled but the credit did not post. This is ours to fix — contact support.",
		},
		{
			Key: StageTopupNotified, Label: "You were told", Actor: "pyyol",
			Description: "A confirmation was pushed to your open tabs and written to your notification feed.",
			StuckHint:   "Your credits are in your balance; only the notification failed.",
		},
	},
}

// failureStages maps a terminal failure stage to the expected stage it replaces,
// so the diagram can render the break IN PLACE rather than appending an orphan
// node the reader has to relate back to the path themselves.
var failureStages = map[string]string{
	StageSessionExpired:  StagePaymentDetected,
	StageChainRejected:   StageChainFinalized,
	StageCreditFailed:    StageCoinsCredited,
	StageRejected:        StageApproved,
	StageBroadcastFailed: StageBroadcast,
	StageOnchainFailed:   StagePaid,
}

// Spec returns the expected path for a flow kind (zero FlowSpec if unknown).
func Spec(flow string) FlowSpec {
	switch flow {
	case FlowDeposit:
		return depositFlow
	case FlowWithdrawal:
		return withdrawalFlow
	case FlowTopup:
		return topupFlow
	}
	return FlowSpec{Flow: flow, Label: flow}
}

// Specs returns every flow model, for a client that wants to render the diagram
// before any attempt exists (the "how a deposit works" explainer).
func Specs() []FlowSpec { return []FlowSpec{depositFlow, withdrawalFlow, topupFlow} }

// replacedStage reports which expected stage a terminal failure stands in for.
func replacedStage(stage string) (string, bool) {
	s, ok := failureStages[stage]
	return s, ok
}
