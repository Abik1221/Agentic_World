package platformcfg

import (
	"encoding/json"
	"testing"
)

// The other half of the admin's pointer-based publisher, asserted from this side of
// the bus so the two cannot drift apart silently.
//
// parse() unmarshals the published snapshot OVER our defaults, which means presence
// on the wire is what decides whether the admin's value or ours wins. These tests pin
// the three cases that matter for money.
func parseOver(t *testing.T, defaults Snapshot, raw string) Snapshot {
	t.Helper()
	s := defaults
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// No economy row published → our configured rake survives untouched.
func TestAbsentCommissionKeepsOurDefault(t *testing.T) {
	def := Snapshot{Economy: Economy{PlatformCommissionPct: 5}}
	got := parseOver(t, def, `{"economy":{}}`)
	if got.Economy.PlatformCommissionPct != 5 {
		t.Fatalf("absent commission overwrote our default with %d%%", got.Economy.PlatformCommissionPct)
	}
	// And with no economy object at all.
	got = parseOver(t, def, `{}`)
	if got.Economy.PlatformCommissionPct != 5 {
		t.Fatalf("missing economy block overwrote our default with %d%%", got.Economy.PlatformCommissionPct)
	}
}

// A published 0 is a deliberate rake-free setting and MUST win over our default,
// otherwise the operator cannot turn the rake off at all.
func TestPublishedZeroCommissionWins(t *testing.T) {
	def := Snapshot{Economy: Economy{PlatformCommissionPct: 5}}
	got := parseOver(t, def, `{"economy":{"platform_commission_pct":0}}`)
	if got.Economy.PlatformCommissionPct != 0 {
		t.Fatalf("a deliberate 0%% rake was ignored; got %d%%", got.Economy.PlatformCommissionPct)
	}
	if got.CommissionPct(5) != 0 {
		t.Fatalf("CommissionPct rejected a legitimate 0%%; got %d%%", got.CommissionPct(5))
	}
}

func TestPublishedCommissionWins(t *testing.T) {
	def := Snapshot{Economy: Economy{PlatformCommissionPct: 5}}
	got := parseOver(t, def, `{"economy":{"platform_commission_pct":8}}`)
	if got.CommissionPct(5) != 8 {
		t.Fatalf("published 8%% was not adopted; got %d%%", got.CommissionPct(5))
	}
}
