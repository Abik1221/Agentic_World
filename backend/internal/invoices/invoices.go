// Package invoices turns money that already moved into a document the user can
// keep.
//
// Everything here is DERIVED. There is no invoices table and there must not be
// one: the deposit, the withdrawal and the ledger are the record, and a second
// stored copy of the same figures is a reconciliation bug waiting to be found by
// an accountant. An invoice is a projection, regenerated on read, so it can never
// disagree with the money.
//
// Why it exists at all: a wallet history line reading "+950 credits" is not a
// receipt. It does not say what was paid, what the platform took, at what rate,
// on which chain, or against which transaction — which is exactly the set of
// facts someone needs to reconcile their own books, claim an expense, or dispute
// a charge. Those facts exist; they were simply never assembled in one place.
//
// Numbers are presented in BOTH units throughout. Credits are what the product
// speaks; currency is what a user's accounting speaks; and every fee here is
// applied in one of the two and felt in the other.
package invoices

import (
	"context"
	"fmt"
	"time"
)

// Kinds of invoice.
const (
	KindDeposit    = "deposit"    // money in, on-chain
	KindTopup      = "topup"      // money in, card / subscription
	KindWithdrawal = "withdrawal" // money out
)

// Line is one row of the invoice breakdown.
//
// Signed from the USER's perspective throughout: a fee they paid is negative, the
// credits they received are positive. Mixing conventions inside one document is
// how a receipt ends up not adding up.
type Line struct {
	Label string `json:"label"`
	// Detail is the "why" — a rate, a percentage, a policy. Optional.
	Detail string `json:"detail,omitempty"`
	// Coins is the credit-denominated amount (0 when the line is currency-only).
	Coins int64 `json:"coins"`
	// Cents is the currency-denominated amount (0 when the line is credits-only).
	Cents int64 `json:"cents"`
	// Total marks the summary row the eye should land on.
	Total bool `json:"total,omitempty"`
}

// Invoice is one completed (or in-flight) money movement, as a document.
type Invoice struct {
	// ID is the underlying object's public id (dep_…, wd_…, or the ledger
	// idempotency key). Stable, so a user can quote it to support.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Number is the human-facing reference. Derived from the id, never
	// sequential: a sequential counter across all users leaks how many payments
	// the platform has processed.
	Number string `json:"number"`
	Status string `json:"status"`
	// Direction is "in" or "out" — stated rather than inferred from a sign,
	// because a withdrawal's amounts are all positive on its own invoice.
	Direction string    `json:"direction"`
	IssuedAt  time.Time `json:"issued_at"`

	Asset string `json:"asset,omitempty"` // USDC, USD, …
	Chain string `json:"chain,omitempty"` // solana, stripe

	// GrossCents is the transacted value before fees, NetCents after. For a
	// deposit, net is what became credits; for a withdrawal, what reaches the
	// wallet.
	GrossCents int64 `json:"gross_cents"`
	FeeCents   int64 `json:"fee_cents"`
	NetCents   int64 `json:"net_cents"`

	Coins    int64 `json:"coins"`     // credits gained (in) or spent (out)
	FeeCoins int64 `json:"fee_coins"` // fee taken in credits, when it was
	NetCoins int64 `json:"net_coins"`

	// Reference is the on-chain signature or provider payment id — the thing that
	// makes this document verifiable by someone who does not trust us.
	Reference string `json:"reference,omitempty"`
	// Counterparty is where the money went or came from.
	Counterparty string `json:"counterparty,omitempty"`
	// ExplorerURL resolves Reference on a public block explorer, when the rail has
	// one. Present so a receipt is checkable in one click rather than by
	// copy-pasting a signature into a search engine.
	ExplorerURL string `json:"explorer_url,omitempty"`

	Lines []Line `json:"lines"`
}

// Row is the raw money movement the repo returns; Build turns it into a document.
type Row struct {
	ID         string
	Kind       string
	Status     string
	IssuedAt   time.Time
	Asset      string
	Chain      string
	AmountBase int64 // token base units (deposits)
	Decimals   int
	Coins      int64 // credits credited (in) or withdrawn (out)
	FeeCoins   int64
	GrossCents int64
	FeeCents   int64
	NetCents   int64
	Reference  string
	Counterpty string
}

// Repo reads the underlying money movements.
type Repo interface {
	// List returns a user's deposits, top-ups and withdrawals, newest first.
	List(ctx context.Context, userPublicID string, limit, offset int) ([]Row, error)
	// Get returns one movement owned by the user (found=false if it is not theirs,
	// which is deliberately indistinguishable from "does not exist").
	Get(ctx context.Context, userPublicID, id string) (Row, bool, error)
}

// Config carries the presentation facts the documents need.
type Config struct {
	// CoinCents is the face value of one credit, used to state a credit figure in
	// currency. Read at render time from the live economy rather than stored: a
	// receipt shows what the movement was worth, and re-pricing history silently
	// would be worse than showing today's rate openly.
	CoinCents int64
	// ExplorerTx is a printf template for a transaction URL (one %s). Empty
	// disables the link rather than emitting a broken one.
	ExplorerTx string
	// Issuer is the legal/product name printed on the document.
	Issuer string
}

type Service struct {
	repo Repo
	cfg  Config
}

func New(repo Repo, cfg Config) *Service {
	if cfg.CoinCents <= 0 {
		cfg.CoinCents = 1
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "Pyyol"
	}
	return &Service{repo: repo, cfg: cfg}
}

// Issuer is printed on every document.
func (s *Service) Issuer() string { return s.cfg.Issuer }

func (s *Service) List(ctx context.Context, userPublicID string, limit, offset int) ([]Invoice, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.repo.List(ctx, userPublicID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]Invoice, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.Build(r))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, userPublicID, id string) (Invoice, bool, error) {
	row, found, err := s.repo.Get(ctx, userPublicID, id)
	if err != nil || !found {
		return Invoice{}, found, err
	}
	return s.Build(row), true, nil
}

// Build renders one movement as a document. Pure — the interesting part (does the
// breakdown add up) is testable without a database.
func (s *Service) Build(r Row) Invoice {
	inv := Invoice{
		ID: r.ID, Kind: r.Kind, Number: number(r.Kind, r.ID), Status: r.Status,
		IssuedAt: r.IssuedAt, Asset: r.Asset, Chain: r.Chain,
		GrossCents: r.GrossCents, FeeCents: r.FeeCents, NetCents: r.NetCents,
		Coins: r.Coins, FeeCoins: r.FeeCoins,
		Reference: r.Reference, Counterparty: r.Counterpty,
	}
	if r.Reference != "" && s.cfg.ExplorerTx != "" && r.Chain == "solana" {
		inv.ExplorerURL = fmt.Sprintf(s.cfg.ExplorerTx, r.Reference)
	}

	switch r.Kind {
	case KindWithdrawal:
		inv.Direction = "out"
		inv.NetCoins = r.Coins - r.FeeCoins
		inv.Lines = s.withdrawalLines(r)
	default:
		inv.Direction = "in"
		inv.NetCoins = r.Coins
		inv.Lines = s.depositLines(r)
	}
	return inv
}

// depositLines breaks down money coming in.
//
// The gross/fee/net split is stated even when the fee is zero. A receipt that
// omits the fee line when it happens to be nil trains people not to look for it,
// and then a later non-zero fee reads as an unexplained shortfall.
func (s *Service) depositLines(r Row) []Line {
	gross := r.Coins + r.FeeCoins
	lines := []Line{{
		Label:  "Amount paid",
		Detail: fmt.Sprintf("%s %s", decimal(r.AmountBase, r.Decimals), orDefault(r.Asset, "USDC")),
		Cents:  r.GrossCents,
	}}
	if gross > 0 {
		lines = append(lines, Line{
			Label:  "Credits at the deposit rate",
			Detail: fmt.Sprintf("1 credit = %s", money(s.cfg.CoinCents)),
			Coins:  gross,
		})
	}
	if r.FeeCoins > 0 {
		lines = append(lines, Line{
			Label:  "Deposit fee",
			Detail: pct(r.FeeCoins, gross) + " of the credited amount",
			Coins:  -r.FeeCoins,
			Cents:  -r.FeeCoins * s.cfg.CoinCents,
		})
	}
	return append(lines, Line{
		Label: "Credits added to your balance",
		Coins: r.Coins, Cents: r.Coins * s.cfg.CoinCents, Total: true,
	})
}

// withdrawalLines breaks down money going out.
//
// Two fees exist on this path and they are charged in different units — the
// platform's cut in credits, the network/processor's in currency. Showing them as
// one number is how a user concludes the arithmetic is wrong.
func (s *Service) withdrawalLines(r Row) []Line {
	lines := []Line{{
		Label:  "Credits withdrawn",
		Detail: fmt.Sprintf("1 credit = %s", money(s.cfg.CoinCents)),
		Coins:  r.Coins, Cents: r.GrossCents,
	}}
	if r.FeeCoins > 0 {
		lines = append(lines, Line{
			Label:  "Platform fee",
			Detail: pct(r.FeeCoins, r.Coins) + " of the withdrawn credits",
			Coins:  -r.FeeCoins, Cents: -r.FeeCoins * s.cfg.CoinCents,
		})
	}
	if r.FeeCents > 0 {
		lines = append(lines, Line{
			Label:  "Network / processing fee",
			Detail: "charged by the payout rail",
			Cents:  -r.FeeCents,
		})
	}
	return append(lines, Line{
		Label:  "Paid to your wallet",
		Detail: orDefault(r.Counterpty, ""),
		Cents:  r.NetCents, Total: true,
	})
}

// number is the human reference: kind prefix + the tail of the public id. Not
// sequential on purpose — a global counter would tell every user how many
// payments the platform has ever processed.
func number(kind, id string) string {
	prefix := "INV"
	switch kind {
	case KindDeposit:
		prefix = "DEP"
	case KindWithdrawal:
		prefix = "WDR"
	case KindTopup:
		prefix = "TOP"
	}
	tail := id
	if len(tail) > 10 {
		tail = tail[len(tail)-10:]
	}
	return prefix + "-" + tail
}

// decimal renders token base units as a human decimal string.
func decimal(base int64, decimals int) string {
	if decimals <= 0 {
		return fmt.Sprintf("%d", base)
	}
	unit := int64(1)
	for i := 0; i < decimals; i++ {
		unit *= 10
	}
	whole, frac := base/unit, base%unit
	if frac == 0 {
		return fmt.Sprintf("%d", whole)
	}
	s := fmt.Sprintf("%0*d", decimals, frac)
	for len(s) > 1 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	return fmt.Sprintf("%d.%s", whole, s)
}

// money renders cents as a currency string.
func money(cents int64) string {
	neg := ""
	if cents < 0 {
		neg, cents = "-", -cents
	}
	return fmt.Sprintf("%s$%d.%02d", neg, cents/100, cents%100)
}

// pct renders part/whole as a percentage, guarding division by zero.
func pct(part, whole int64) string {
	if whole <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.4g%%", float64(part)*100/float64(whole))
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
