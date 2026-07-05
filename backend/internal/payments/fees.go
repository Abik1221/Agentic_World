package payments

import "math"

// PaymentMethod identifies how the buyer pays; fee schedules differ per method.
type PaymentMethod string

const (
	MethodCard   PaymentMethod = "card"
	MethodACH    PaymentMethod = "ach"
	MethodPayPal PaymentMethod = "paypal"
)

// FeeSchedule is the pass-through processing cost for a payment method.
type FeeSchedule struct {
	Pct       int   // percent of pack price (e.g. 290 = 2.90%)
	FlatCents int64 // flat fee in cents
}

// DepositQuote is the transparent checkout breakdown shown before payment.
type DepositQuote struct {
	PackKey            string `json:"pack_key"`
	PackLabel          string `json:"pack_label"`
	Coins              int64  `json:"coins"`
	PackCents          int64  `json:"pack_cents"`
	ProcessingFeeCents int64  `json:"processing_fee_cents"`
	TotalCents         int64  `json:"total_cents"`
	PaymentMethod      string `json:"payment_method"`
	Currency           string `json:"currency"`
}

// DefaultFeeSchedules maps payment methods to typical provider costs (basis points
// stored as pct*100 for precision: 290 = 2.9%).
func DefaultFeeSchedules() map[PaymentMethod]FeeSchedule {
	return map[PaymentMethod]FeeSchedule{
		MethodCard:   {Pct: 290, FlatCents: 30},
		MethodACH:    {Pct: 80, FlatCents: 0},
		MethodPayPal: {Pct: 349, FlatCents: 49},
	}
}

// QuoteDeposit computes the pass-through processing fee for a coin pack purchase.
// The user receives the full coin amount; processing fees are charged on top.
func QuoteDeposit(pack Pack, method PaymentMethod, schedules map[PaymentMethod]FeeSchedule) DepositQuote {
	if schedules == nil {
		schedules = DefaultFeeSchedules()
	}
	sch, ok := schedules[method]
	if !ok {
		sch = schedules[MethodCard]
	}
	fee := pack.Cents*int64(sch.Pct)/10000 + sch.FlatCents
	if fee < 0 {
		fee = 0
	}
	return DepositQuote{
		PackKey:            pack.Key,
		PackLabel:          pack.Label,
		Coins:              pack.Coins,
		PackCents:          pack.Cents,
		ProcessingFeeCents: fee,
		TotalCents:         pack.Cents + fee,
		PaymentMethod:      string(method),
		Currency:           "usd",
	}
}

// NormalizeMethod coerces unknown methods to card.
func NormalizeMethod(m string) PaymentMethod {
	switch PaymentMethod(m) {
	case MethodACH, MethodPayPal:
		return PaymentMethod(m)
	default:
		return MethodCard
	}
}

// RoundCents ensures fee math stays in whole cents.
func RoundCents(v float64) int64 {
	return int64(math.Round(v))
}
