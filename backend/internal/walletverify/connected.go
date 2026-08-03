package walletverify

import (
	"context"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/gagliardetto/solana-go"
)

// Recording WHICH wallet a developer connected in their browser.
//
// Nothing did this before, and three things were broken by its absence:
//
//  1. The Super Admin's wallet panel has a `provider` column that was permanently
//     empty for anyone who connected Phantom directly. Only the Privy login path ever
//     wrote users.wallet_provider, so an operator looking at a real account could not
//     tell which wallet app was on it.
//  2. The profile-completion "connect your wallet" step reads users.wallet_address,
//     which — again — only Privy ever wrote. A developer who connected Phantom could
//     never reach 100%, no matter what they did.
//  3. The dashboard could not name the connected wallet after a reload, because the
//     brand only existed in browser memory.
//
// WHY THIS IS CAREFUL RATHER THAN A ONE-LINE UPDATE
//
// users.wallet_address is NOT decoration. The payout path compares it against
// verified_wallet_address and refuses to pay unless they are equal (payout/service.go:
// "an unverified or swapped hint can't redirect funds"). That equality is a real
// safety control, and it means a naive "save whatever the browser connected" write is
// dangerous in a way that has nothing to do with attackers:
//
//	a developer with working payouts connects a DIFFERENT wallet to look at something,
//	the hint is overwritten, the equality breaks, and their next withdrawal is refused
//	as "wallet not verified" — with no obvious connection to what they did.
//
// So the hint is only ever FILLED IN, never REPOINTED: it is written when empty (the
// first connect, which is the case that unblocks completion) or when it already equals
// the incoming address (a no-op refresh). A different address updates the provider and
// leaves the payout pairing exactly as it was.
//
// The address is also self-reported and therefore UNVERIFIED — that is why it can never
// be a payout destination on its own. Proving ownership is a separate, signed flow
// (StartChallenge/Verify), and money only ever goes to what that produced.

// ConnectedWallet is the resulting state, so the client can render what is on file
// without a second call.
type ConnectedWallet struct {
	Address  string `json:"wallet_address,omitempty"`
	Provider string `json:"wallet_provider,omitempty"`
	// Verified is true when this address is also the PROVEN payout destination. The
	// client uses it to say "connected" vs "connected and verified for payouts", which
	// are very different states to be in when you try to cash out.
	Verified bool `json:"verified"`
	// HintKept is true when a DIFFERENT address was already on file and was left alone.
	// Surfaced rather than hidden: silently ignoring half of what a caller asked for is
	// how a developer concludes the button does nothing.
	HintKept bool `json:"hint_kept,omitempty"`
}

// ConnectedRepo is the extra persistence the connect path needs.
type ConnectedRepo interface {
	// LinkedWallet returns the current hint address, the verified address, and the
	// provider on file.
	LinkedWallet(ctx context.Context, userPublicID string) (hint, verified, provider string, err error)
	// SetWalletProvider records the wallet brand. Display only — never consulted by any
	// money path, which is what makes it safe to overwrite freely.
	SetWalletProvider(ctx context.Context, userPublicID, provider string) error
	// SetWalletHintIfEmpty fills users.wallet_address ONLY when it is currently empty.
	// Returns whether it wrote. The guard lives in the SQL so a future caller cannot
	// accidentally repoint an established payout pairing.
	SetWalletHintIfEmpty(ctx context.Context, userPublicID, walletAddress string) (wrote bool, err error)
}

// SetConnectedRepo wires the connect-recording persistence. Nil leaves the route
// answering 503 rather than the arena failing to start.
func (s *Service) SetConnectedRepo(r ConnectedRepo) { s.connected = r }

// maxProvider bounds the self-reported brand string. It is rendered in the admin UI, so
// it is length-capped and stripped of anything that is not a plain identifier — an
// operator's screen is not a place to render arbitrary caller-supplied text.
const maxProvider = 32

// RecordConnected saves the wallet a developer just connected in their browser.
func (s *Service) RecordConnected(ctx context.Context, userPublicID, walletAddress, provider string) (ConnectedWallet, error) {
	if s.connected == nil {
		return ConnectedWallet{}, httpx.NewError(http.StatusServiceUnavailable, "wallet_connect_unavailable",
			"Recording a connected wallet is not available in this environment.")
	}
	walletAddress = strings.TrimSpace(walletAddress)
	// Validated as a real Solana public key, exactly as the challenge flow does. An
	// unparseable address would otherwise sit in the admin panel looking like an account
	// somebody could pay.
	if _, err := solana.PublicKeyFromBase58(walletAddress); err != nil {
		return ConnectedWallet{}, httpx.NewError(http.StatusBadRequest, "invalid_wallet_address",
			"That is not a valid Solana wallet address.")
	}

	if p := sanitizeProvider(provider); p != "" {
		if err := s.connected.SetWalletProvider(ctx, userPublicID, p); err != nil {
			return ConnectedWallet{}, err
		}
	}

	// Fill the hint only if there is nothing there. See the note at the top of the file
	// for why this must not repoint an existing one.
	if _, err := s.connected.SetWalletHintIfEmpty(ctx, userPublicID, walletAddress); err != nil {
		return ConnectedWallet{}, err
	}

	hint, verified, prov, err := s.connected.LinkedWallet(ctx, userPublicID)
	if err != nil {
		return ConnectedWallet{}, err
	}
	return ConnectedWallet{
		Address:  hint,
		Provider: prov,
		Verified: verified != "" && verified == hint,
		HintKept: hint != "" && hint != walletAddress,
	}, nil
}

// Connected reads what is on file, for rendering the wallet card after a reload.
func (s *Service) Connected(ctx context.Context, userPublicID string) (ConnectedWallet, error) {
	if s.connected == nil {
		return ConnectedWallet{}, nil
	}
	hint, verified, prov, err := s.connected.LinkedWallet(ctx, userPublicID)
	if err != nil {
		return ConnectedWallet{}, err
	}
	return ConnectedWallet{
		Address:  hint,
		Provider: prov,
		Verified: verified != "" && verified == hint,
	}, nil
}

// sanitizeProvider reduces a self-reported brand to a short, plain identifier
// ("phantom", "solflare", "backpack"). Anything else is dropped rather than stored:
// this string is displayed to an operator, and the safest treatment of caller-supplied
// display text is to accept only the shape we expect.
func sanitizeProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if len(p) > maxProvider {
		p = p[:maxProvider]
	}
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
