package devprofile

import "context"

// Profile completion, derived from what the DATABASE actually holds.
//
// This used to be computed in the browser from localStorage flags, and the
// consequences were exactly what you would expect from storing account state on one
// device: a developer who filled everything in on their phone opened the dashboard on
// a laptop and saw 0%, with a checklist telling them to redo work they had already
// done. Signing out and back in did not help, because the flags were never on the
// server to begin with — nothing was lost, nothing was ever saved.
//
// Deriving it server-side also means the checklist cannot disagree with reality: a
// step is done when the row says so, so "upload a photo" ticks the moment the avatar
// is stored and stays ticked on every device, forever.
//
// WHAT COUNTS, and what deliberately does not:
//   - "Set up withdrawals" and "Set spending limits" are GONE. The first asked a developer
//     to configure a payout rail before they had won anything — the wrong thing to demand
//     on day one, and it left every new profile permanently incomplete. The second is a
//     guardrail with a working default, so requiring it to reach 100% turned a safety
//     feature into a chore. Neither was ever real account state: both were localStorage
//     booleans the developer ticked by hand, which is not a checklist, it is a to-do list
//     that lies on a second device.
//   - The wallet replaces both, as TWO steps: connect, then verify. They were one step,
//     satisfied by either column, and that understated the work — a developer read 100%
//     complete and then found at the moment of cashing out that a signature was still
//     required. Connecting shares a public address; verifying proves ownership and is what
//     makes a payout possible. Both are real, server-side, and identical on every device.

// CompletionStep is one item on the profile checklist.
type CompletionStep struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Done  bool   `json:"done"`
	// Href/CTA travel with the step so the checklist has one definition. The client
	// previously owned this list and drifted from the server's idea of completeness.
	Href string `json:"href"`
	CTA  string `json:"cta"`
}

// Completion is the whole checklist plus the headline percentage.
type Completion struct {
	Steps    []CompletionStep `json:"steps"`
	Percent  int              `json:"percent"`
	Complete bool             `json:"complete"`
}

// CompletionState is the account state the checklist is derived from.
type CompletionState struct {
	DisplayName string
	AvatarURL   string
	Username    string
	// WalletAddress is any wallet the developer has connected — the login hint or the
	// proven address, whichever exists.
	WalletAddress string
	// VerifiedWallet is the address whose ownership was PROVEN by a signed challenge.
	// Its own step, because it is what actually gates a payout.
	VerifiedWallet string
}

// WalletReader reads the developer's wallet linkage. Separate from the profile repo
// because wallet linkage lives on the identity side of the schema.
type WalletReader interface {
	ConnectedWallet(ctx context.Context, userPublicID string) (string, error)
	// VerifiedWallet is the ownership-proven payout address ("" when unproven).
	VerifiedWallet(ctx context.Context, userPublicID string) (string, error)
}

// SetWalletReader wires the wallet lookup used by profile completion. Nil ⇒ the wallet
// step reports not-done rather than the request failing: a missing optional dependency
// must not break the dashboard.
func (s *Service) SetWalletReader(r WalletReader) { s.wallets = r }

// Completion assembles the caller's checklist. Identity is read fresh, so the result
// always reflects the current rows.
func (s *Service) Completion(ctx context.Context, userPublicID string) (Completion, error) {
	id, found, err := s.repo.ResolveHandle(ctx, userPublicID)
	if err != nil {
		return Completion{}, err
	}
	if !found {
		// No such developer: everything except "verify" is undone, and the caller (an
		// authenticated route) has a session, so even that is a strange state. Return
		// the empty checklist rather than an error — the dashboard should render.
		return buildCompletion(CompletionState{}), nil
	}
	st := CompletionState{
		DisplayName: id.DisplayName,
		AvatarURL:   id.AvatarURL,
		Username:    id.Username,
	}
	if s.wallets != nil {
		// Best-effort: a wallet lookup failure leaves that one step unticked instead of
		// failing the whole checklist.
		if addr, werr := s.wallets.ConnectedWallet(ctx, userPublicID); werr == nil {
			st.WalletAddress = addr
		}
		if addr, werr := s.wallets.VerifiedWallet(ctx, userPublicID); werr == nil {
			st.VerifiedWallet = addr
		}
	}
	return buildCompletion(st), nil
}

// buildCompletion is the single definition of "a complete profile". Pure, so it is
// tested directly without a database.
func buildCompletion(st CompletionState) Completion {
	// A connected wallet is IMPLIED by a proven one. Verification runs against an address
	// the developer connected, so an account with a proven wallet has necessarily done the
	// connecting — but the hint column can be empty for accounts that verified before the
	// connect-recording endpoint existed, and showing those developers an unticked
	// "connect your wallet" underneath a ticked "verify your wallet" would be nonsense.
	connected := st.WalletAddress != "" || st.VerifiedWallet != ""

	steps := []CompletionStep{
		// Reaching this code at all means an authenticated session exists, so identity
		// is done by construction. It stays on the list because a checklist whose first
		// item is invisible reads as if it has fewer steps than it does.
		{Key: "verify", Label: "Verify your identity", Done: true, Href: "/login", CTA: "Verified"},
		{Key: "name", Label: "Name your agent", Done: st.DisplayName != "", Href: "/profile", CTA: "Add name"},
		{Key: "avatar", Label: "Upload an agent photo", Done: st.AvatarURL != "", Href: "/profile", CTA: "Upload"},
		{Key: "handle", Label: "Claim your @handle", Done: st.Username != "", Href: "/profile", CTA: "Claim"},
		{Key: "wallet", Label: "Connect your wallet", Done: connected, Href: "/wallet", CTA: "Connect"},
		// The payout gate, and the reason this is its own line. Rolled into the step
		// above, a developer reached 100% and then hit a signature request at the moment
		// they tried to cash out — the one moment a surprise requirement is least welcome.
		{Key: "wallet_verified", Label: "Verify your wallet for payouts", Done: st.VerifiedWallet != "", Href: "/wallet", CTA: "Verify"},
	}
	done := 0
	for _, s := range steps {
		if s.Done {
			done++
		}
	}
	// Integer percentage over the real step count, so 100 means every step and nothing
	// rounds up to it.
	pct := done * 100 / len(steps)
	return Completion{Steps: steps, Percent: pct, Complete: done == len(steps)}
}
