package telemetry

import "testing"

// The allowlist must FAIL CLOSED. If someone adds an event type and forgets to
// classify it, the correct failure is "a developer cannot see their own data", never
// "a developer can read the platform's books or a rival's strategy".
func TestUnknownEventTypesAreHiddenFromDevelopers(t *testing.T) {
	for _, unknown := range []string{
		"", "some_future_event", "stake_escrowed", "payout_sent", "platform_revenue",
	} {
		if DevVisible(unknown) {
			t.Fatalf("unclassified event %q is dev-visible; the allowlist must fail closed", unknown)
		}
	}
}

// Money movement and gateway metering are operator-only, and must stay that way.
//
// The ledger's internal postings are the platform's books — a developer reads their
// own balance and results through the wallet/match APIs instead. Gateway metering
// carries the provenance used for anti-cheat, so exposing it tells someone precisely
// what the platform can and cannot observe.
func TestMoneyAndMeteringAreNeverDevVisible(t *testing.T) {
	for _, sensitive := range []string{
		"model_call_completed", "log_record", "benchmark_recorded",
		"stake_escrowed", "stake_released", "payout_completed", "settlement_posted",
	} {
		if DevVisible(sensitive) {
			t.Fatalf("%q must not be visible to developers", sensitive)
		}
	}
}

// The agent's own behaviour IS visible — that is the point of the feature.
func TestOwnAgentBehaviourIsDevVisible(t *testing.T) {
	for _, allowed := range []string{
		EventAgentDecision, EventAgentSaid, EventAgentSayRejected,
		EventAgentConnected, EventAgentDisconnected,
		EventAgentEndpointVerified, EventAgentEndpointFailed,
	} {
		if !DevVisible(allowed) {
			t.Fatalf("%q should be visible to the owning developer", allowed)
		}
	}
	if len(DevVisibleEventTypes()) != len(devVisibleEventTypes) {
		t.Fatal("DevVisibleEventTypes() drifted from the allowlist it is derived from")
	}
}
