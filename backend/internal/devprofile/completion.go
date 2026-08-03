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
//   - Two steps were removed. "Set up withdrawals" asked a developer to configure a
//     payout rail before they had won anything — the wrong thing to demand on day one,
//     and it left every new profile permanently incomplete. "Set spending limits" is a
//     guardrail with a working default, so requiring it to reach 100% turned a safety
//     feature into a chore.
//   - "Connect a wallet" replaces them. It is the step that actually gates the thing a
//     developer wants (funding an agent and being paid), and it is real, verifiable
//     account state rather than a local flag.

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
	// WalletAddress is the wallet the developer has connected. Any connected wallet
	// counts here — PROVEN ownership is a separate, stricter requirement that gates
	// payouts, and demanding a signed challenge before the profile can read 100% would
	// block completion on a step that only matters when money leaves.
	WalletAddress string
}

// WalletReader reads the developer's connected wallet. Separate from the profile repo
// because wallet linkage lives on the identity side of the schema.
type WalletReader interface {
	ConnectedWallet(ctx context.Context, userPublicID string) (string, error)
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
	}
	return buildCompletion(st), nil
}

// buildCompletion is the single definition of "a complete profile". Pure, so it is
// tested directly without a database.
func buildCompletion(st CompletionState) Completion {
	steps := []CompletionStep{
		// Reaching this code at all means an authenticated session exists, so identity
		// is done by construction. It stays on the list because a checklist whose first
		// item is invisible reads as if it has fewer steps than it does.
		{Key: "verify", Label: "Verify your identity", Done: true, Href: "/login", CTA: "Verified"},
		{Key: "name", Label: "Name your agent", Done: st.DisplayName != "", Href: "/profile", CTA: "Add name"},
		{Key: "avatar", Label: "Upload an agent photo", Done: st.AvatarURL != "", Href: "/profile", CTA: "Upload"},
		{Key: "handle", Label: "Claim your @handle", Done: st.Username != "", Href: "/profile", CTA: "Claim"},
		{Key: "wallet", Label: "Connect your wallet", Done: st.WalletAddress != "", Href: "/wallet", CTA: "Connect"},
	}
	done := 0
	for _, s := range steps {
		if s.Done {
			done++
		}
	}
	// Integer percentage over a fixed 5 steps, so 100 means every step and nothing
	// rounds up to it.
	pct := done * 100 / len(steps)
	return Completion{Steps: steps, Percent: pct, Complete: done == len(steps)}
}
