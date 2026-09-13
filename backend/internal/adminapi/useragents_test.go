package adminapi

import "testing"

// Blocked() is the sentence an operator repeats to a developer, so it has to be right and it
// has to be specific.
//
// The surface exists because "why did my agent stop playing?" was unanswerable from any
// operator screen: the guardrails lived on the agents row and only the owner could read them.
// The one fact an operator COULD see — a balance sitting there unspent — points at a fault
// that does not exist, which is how these tickets turned into engineering time.

func active() AgentGuardrails {
	// A healthy agent: funded, under every limit, nothing in flight.
	return AgentGuardrails{
		Status:               "active",
		Balance:              5_000,
		CoinLimitPerMatch:    500,
		MaxBid:               200,
		MinWalletBalance:     100,
		DailyLossLimit:       1_000,
		SessionLossLimit:     2_000,
		MaxConcurrentMatches: 3,
	}
}

func TestHealthyAgentIsNotReportedAsBlocked(t *testing.T) {
	if reason := active().Blocked(); reason != "" {
		t.Fatalf("a healthy agent reported blocked: %q", reason)
	}
}

func TestDailyLossStopIsNamed(t *testing.T) {
	a := active()
	a.LossToday = 1_000 // exactly at the limit — the stop is inclusive
	if got := a.Blocked(); got != "daily loss limit reached" {
		t.Fatalf("Blocked() = %q, want the daily loss stop", got)
	}
}

func TestSessionLossStopIsNamed(t *testing.T) {
	a := active()
	a.LossSession = 2_400
	if got := a.Blocked(); got != "session loss limit reached" {
		t.Fatalf("Blocked() = %q, want the session loss stop", got)
	}
}

func TestConcurrencyCapIsNamed(t *testing.T) {
	a := active()
	a.ActiveMatches = 3
	if got := a.Blocked(); got != "already at its concurrent-match limit" {
		t.Fatalf("Blocked() = %q, want the concurrency cap", got)
	}
}

// min_wallet_balance is a soft UI floor only — sit needs balance ≥ stake. An agent
// sitting exactly on that floor with coins left is NOT blocked from playing.
func TestMinWalletBalanceDoesNotBlockSit(t *testing.T) {
	a := active()
	a.Balance = 100
	a.MinWalletBalance = 100
	if got := a.Blocked(); got != "" {
		t.Fatalf("Blocked() = %q, want empty (soft floor is not a sit gate)", got)
	}
}

func TestEmptyWalletIsNamedRatherThanBlamedOnALimit(t *testing.T) {
	a := active()
	a.Balance = 0
	a.MinWalletBalance = 0 // no reserve configured, so this is simply unfunded
	if got := a.Blocked(); got != "no coins allocated to this agent" {
		t.Fatalf("Blocked() = %q, want the unfunded case", got)
	}
}

// A suspended agent outranks every other explanation. Telling a developer their daily loss
// stop is the problem when the platform has actually suspended them is worse than saying
// nothing — they would tune a limit and get nowhere.
func TestSuspensionOutranksEveryGuardrail(t *testing.T) {
	a := active()
	a.Status = "suspended"
	a.LossToday = 5_000
	a.ActiveMatches = 9
	a.Balance = 0
	if got := a.Blocked(); got != "agent is suspended" {
		t.Fatalf("Blocked() = %q, want the suspension", got)
	}
}

// A limit of zero means "no limit", not "a limit of zero". Reading it the other way would
// report every agent with an unset daily stop as permanently blocked.
func TestZeroLimitsMeanUnlimited(t *testing.T) {
	a := AgentGuardrails{Status: "active", Balance: 1_000}
	if reason := a.Blocked(); reason != "" {
		t.Fatalf("an agent with no limits set reported blocked: %q", reason)
	}
}

// Being under a limit is not being at it. An off-by-one here tells a developer to raise a
// stop they have not reached.
func TestJustUnderALimitIsNotBlocked(t *testing.T) {
	a := active()
	a.LossToday = a.DailyLossLimit - 1
	a.LossSession = a.SessionLossLimit - 1
	a.ActiveMatches = a.MaxConcurrentMatches - 1
	a.Balance = a.MinWalletBalance + 1
	if reason := a.Blocked(); reason != "" {
		t.Fatalf("an agent one short of every limit reported blocked: %q", reason)
	}
}
