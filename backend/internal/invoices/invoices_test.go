package invoices

import (
	"strings"
	"testing"
	"time"
)

func svc() *Service {
	return New(nil, Config{CoinCents: 1, ExplorerTx: "https://solscan.io/tx/%s", Issuer: "Pyyol"})
}

// A receipt that does not add up is worse than no receipt: it invites a dispute
// and loses it. Every line must reconcile to the stated total.
func TestDepositBreakdownReconcilesToTheCreditedTotal(t *testing.T) {
	inv := svc().Build(Row{
		ID: "dep_abc123456789", Kind: KindDeposit, Status: "completed",
		IssuedAt: time.Now(), Asset: "USDC", Chain: "solana",
		AmountBase: 10_000_000, Decimals: 6, // 10 USDC
		Coins: 950, FeeCoins: 50, // 1000 gross, 5% fee
		GrossCents: 1000, FeeCents: 50, NetCents: 950,
		Reference: "5xSig",
	})

	if inv.Direction != "in" {
		t.Fatalf("direction = %q, want in", inv.Direction)
	}
	if inv.NetCoins != 950 {
		t.Fatalf("net credits = %d, want 950", inv.NetCoins)
	}

	var sum int64
	var total *Line
	for i, l := range inv.Lines {
		if l.Total {
			total = &inv.Lines[i]
			continue
		}
		sum += l.Coins
	}
	if total == nil {
		t.Fatal("the breakdown has no total line for the eye to land on")
	}
	// gross credits (+1000) minus the fee (−50) must equal the total (950).
	if sum != total.Coins {
		t.Fatalf("the lines sum to %d credits but the total says %d", sum, total.Coins)
	}
}

// The fee line must be present even at zero. Hiding it when it happens to be nil
// teaches people not to look for it, and a later non-zero fee then reads as an
// unexplained shortfall.
func TestDepositWithoutAFeeStillStatesTheAmountAndTheTotal(t *testing.T) {
	inv := svc().Build(Row{
		ID: "dep_nofee", Kind: KindDeposit, Status: "completed", IssuedAt: time.Now(),
		AmountBase: 5_000_000, Decimals: 6, Coins: 500, FeeCoins: 0,
		GrossCents: 500, NetCents: 500,
	})
	if len(inv.Lines) < 2 {
		t.Fatalf("a zero-fee deposit still needs an amount and a total, got %d lines", len(inv.Lines))
	}
	if !inv.Lines[len(inv.Lines)-1].Total {
		t.Error("the last line must be the total")
	}
}

// The two withdrawal fees are charged in DIFFERENT units. Collapsing them into
// one number is exactly how a user concludes the arithmetic is wrong.
func TestWithdrawalSeparatesTheCreditFeeFromTheCurrencyFee(t *testing.T) {
	inv := svc().Build(Row{
		ID: "wd_xyz", Kind: KindWithdrawal, Status: "paid", IssuedAt: time.Now(),
		Asset: "USDC", Chain: "solana",
		Coins: 1000, FeeCoins: 100, // 10% platform fee, in credits
		GrossCents: 1000, FeeCents: 25, NetCents: 875, // 25c network fee, in currency
		Reference: "sigOut", Counterpty: "9xQeWv…",
	})

	if inv.Direction != "out" {
		t.Fatalf("direction = %q, want out", inv.Direction)
	}
	if inv.NetCoins != 900 {
		t.Fatalf("net credits = %d, want 900 (1000 − 100 fee)", inv.NetCoins)
	}

	var creditFee, currencyFee bool
	for _, l := range inv.Lines {
		if l.Coins == -100 && l.Cents == -100 {
			creditFee = true
		}
		if l.Coins == 0 && l.Cents == -25 {
			currencyFee = true
		}
	}
	if !creditFee {
		t.Error("the platform fee must appear as its own line, in credits")
	}
	if !currencyFee {
		t.Error("the network fee must appear as its own line, in currency")
	}
	if last := inv.Lines[len(inv.Lines)-1]; !last.Total || last.Cents != 875 {
		t.Fatalf("the total must be what reaches the wallet, got %+v", last)
	}
}

// An in-flight withdrawal is still a document: the credits already left, so the
// user is owed an explanation of where they went before the payout lands.
func TestARequestedWithdrawalStillRenders(t *testing.T) {
	inv := svc().Build(Row{
		ID: "wd_pending", Kind: KindWithdrawal, Status: "requested", IssuedAt: time.Now(),
		Coins: 500, FeeCoins: 50, GrossCents: 500, NetCents: 450,
	})
	if inv.Status != "requested" || len(inv.Lines) == 0 {
		t.Fatalf("an in-flight withdrawal must render: %+v", inv)
	}
}

// Verifiability is the point of the reference. A receipt someone else has to
// trust is not a receipt.
func TestOnChainInvoiceLinksToAnExplorer(t *testing.T) {
	inv := svc().Build(Row{
		ID: "dep_1", Kind: KindDeposit, Chain: "solana", Reference: "5xSig",
		Coins: 100, AmountBase: 1_000_000, Decimals: 6,
	})
	if !strings.HasSuffix(inv.ExplorerURL, "5xSig") {
		t.Fatalf("explorer url = %q, want it to resolve the signature", inv.ExplorerURL)
	}
}

// A card charge has no on-chain signature, and a link built from a Stripe payment
// intent would 404 on a block explorer — worse than no link, because it looks
// like a verification that failed.
func TestOffChainInvoiceHasNoExplorerLink(t *testing.T) {
	inv := svc().Build(Row{
		ID: "pi_1", Kind: KindTopup, Chain: "stripe", Reference: "pi_1", Coins: 100,
	})
	if inv.ExplorerURL != "" {
		t.Fatalf("a stripe top-up must not link to a block explorer, got %q", inv.ExplorerURL)
	}
}

// The number is what a user quotes to support, so it must be stable and readable
// — and must NOT be a global sequence, which would disclose the platform's total
// payment volume to every customer.
func TestInvoiceNumberIsDerivedFromTheIdNotASequence(t *testing.T) {
	a := svc().Build(Row{ID: "dep_aaaaaaaaaaaa", Kind: KindDeposit})
	b := svc().Build(Row{ID: "dep_aaaaaaaaaaaa", Kind: KindDeposit})
	if a.Number != b.Number {
		t.Fatal("the same payment must always produce the same number")
	}
	if !strings.HasPrefix(a.Number, "DEP-") {
		t.Fatalf("number = %q, want a kind-prefixed reference", a.Number)
	}
	if c := svc().Build(Row{ID: "wd_bbbbbbbbbbbb", Kind: KindWithdrawal}); c.Number == a.Number {
		t.Fatal("different payments must not share a number")
	}
}

func TestDecimalRendersTokenBaseUnits(t *testing.T) {
	cases := map[string]struct {
		base     int64
		decimals int
		want     string
	}{
		"whole":          {10_000_000, 6, "10"},
		"fractional":     {10_500_000, 6, "10.5"},
		"trailing zeros": {1_100_000, 6, "1.1"},
		"sub-unit":       {500_000, 6, "0.5"},
		"no decimals":    {1234, 0, "1234"},
		"full precision": {1_234_567, 6, "1.234567"},
	}
	for name, c := range cases {
		if got := decimal(c.base, c.decimals); got != c.want {
			t.Errorf("%s: decimal(%d, %d) = %q, want %q", name, c.base, c.decimals, got, c.want)
		}
	}
}

// A fee percentage computed against a zero base is the shape a divide-by-zero
// takes on a receipt. It must render something harmless, not NaN or Inf.
func TestPercentageOfNothingIsZeroNotNaN(t *testing.T) {
	if got := pct(0, 0); got != "0%" {
		t.Fatalf("pct(0,0) = %q, want 0%%", got)
	}
}

func TestMoneyRendersNegativeAmountsReadably(t *testing.T) {
	if got := money(-50); got != "-$0.50" {
		t.Fatalf("money(-50) = %q, want -$0.50", got)
	}
	if got := money(1000); got != "$10.00" {
		t.Fatalf("money(1000) = %q, want $10.00", got)
	}
}
