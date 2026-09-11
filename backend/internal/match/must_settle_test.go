package match

import "testing"

// Escrow OUT must be decided by the same fact as escrow IN: was money staked.
//
// Production took 500 coins from each seat in six matches and settled none of them. The
// matches were finished, one had a real winner, and the CLI reported a +420 payout — but
// the ledger showed only `stake -500` rows, six of them, and roughly 3,000 coins per
// agent sat in escrow with nothing to release them.
//
// finalize skipped its ENTIRE settlement block because the match's mode said sandbox,
// while staking had gone ahead because the bid was 500. The two decisions were keyed on
// different facts, so a match could be "free" for the purpose of paying out and "staked"
// for the purpose of charging.
//
// These pin the rule rather than the symptom: however a staked match comes to be
// mis-moded — and that has not been found yet — the money still has to come back.

func TestAStakedMatchMustSettleEvenIfItSaysSandbox(t *testing.T) {
	// The exact production shape. This is the case that lost the coins.
	if !mustSettle(ModeSandbox, 500) {
		t.Fatal("a match that took a 500-coin stake was allowed to finish without settling — " +
			"both stakes stay in escrow and nobody is ever paid")
	}
}

func TestARealSandboxTableStillSettlesNothing(t *testing.T) {
	// The other half. Practice tables move no coins, and settling one would post a
	// zero-value disbursement that consumes the match's single disburse key.
	if mustSettle(ModeSandbox, 0) {
		t.Fatal("a free practice table should have nothing to disburse")
	}
}

func TestACompetitiveMatchAlwaysSettles(t *testing.T) {
	if !mustSettle(ModeCompetitive, 500) {
		t.Fatal("a staked competitive match must settle")
	}
	// Unchanged from the previous behaviour: mode alone was already enough here, and
	// narrowing it would strand escrow on any competitive table with an odd bid.
	if !mustSettle(ModeCompetitive, 0) {
		t.Fatal("a competitive match must still take the settlement path")
	}
}

func TestAnUnknownModeWithMoneyOnItSettles(t *testing.T) {
	// A mode this build does not recognise must not become a way to skip payout. The
	// dangerous direction is asymmetric: settling a table that owed nothing posts zeroes,
	// while skipping one that owed 1,000 keeps a developer's coins.
	if !mustSettle("", 500) {
		t.Fatal("an unrecognised mode carrying a stake must still settle")
	}
	if !mustSettle("some-future-mode", 500) {
		t.Fatal("an unrecognised mode carrying a stake must still settle")
	}
}
