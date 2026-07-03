package payments_test

import (
	"testing"

	"github.com/agent-arena/arena/internal/payments"
)

func TestQuoteDepositPassThroughFee(t *testing.T) {
	pack := payments.Pack{Key: "pro", Label: "$20 pack", Cents: 2000, Coins: 2400}
	q := payments.QuoteDeposit(pack, payments.MethodCard, nil)
	if q.Coins != 2400 {
		t.Fatalf("coins = %d, want 2400 (full pack, fee not deducted)", q.Coins)
	}
	if q.PackCents != 2000 {
		t.Fatalf("pack cents = %d", q.PackCents)
	}
	if q.ProcessingFeeCents <= 0 {
		t.Fatal("expected a positive processing fee")
	}
	if q.TotalCents != q.PackCents+q.ProcessingFeeCents {
		t.Fatalf("total = %d, want pack+fee", q.TotalCents)
	}
}
