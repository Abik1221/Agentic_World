package main

// Small helpers shared across the gamelab drivers.
//
// These lived in monopoly.go and were used from mafia.go — so removing the Monopoly
// driver took two functions Mafia depends on with it. They are not Monopoly logic in
// any sense; they only happened to be written there first.
//
// That is the whole hazard in withdrawing an arena: the parts that are genuinely
// game-specific are easy to spot, and the shared ones hiding inside a game's file are
// not. Moving them here makes the sharing explicit, so the next removal cannot repeat
// it by accident.

// legalSet indexes a legal-action list for O(1) membership tests.
func legalSet(l []string) map[string]bool {
	m := make(map[string]bool, len(l))
	for _, k := range l {
		m[k] = true
	}
	return m
}

// itoa formats an int without pulling in strconv.
//
// Deliberate: gamelab's agent files are also read as worked examples of an SDK-free
// agent, and a hand-rolled conversion keeps the import list down to what an agent
// genuinely needs.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
