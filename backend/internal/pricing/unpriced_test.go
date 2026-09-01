package pricing

import "testing"

// A cost-efficiency ranking is a claim about money, so the difference between a rate from the
// price table and the fallback guess has to be visible. These pin that the distinction exists
// and is reported once per model — the shape a benchmark operator needs, because the window to
// add a missing rate closes when the results are published.

func TestPriceBasisSeparatesTableRatesFromGuesses(t *testing.T) {
	// A family rule counts as the table: claude-opus-4-5 has no explicit entry but resolves
	// through a rule to a real published rate.
	for _, known := range []string{"gpt-4o", "claude-opus-4-5", "deepseek-chat", "llama-3.3-70b-versatile"} {
		if got := PriceBasis(known); got != "table" {
			t.Fatalf("PriceBasis(%q) = %q, want \"table\"", known, got)
		}
	}
	// Models with no entry and no matching rule. These are the ones whose published cost is
	// a guess, and the whole point is that a reader can tell.
	for _, unknown := range []string{"totally-made-up-9000", "some-new-frontier-model"} {
		if got := PriceBasis(unknown); got != "estimated" {
			t.Fatalf("PriceBasis(%q) = %q, want \"estimated\" — a guessed rate presented as a "+
				"measured one is how a model wins a cost board it should not", unknown, got)
		}
	}
}

// Once per model, not once per call. A benchmark makes thousands of calls per model; a line
// each would bury the signal this exists to raise.
func TestUnpricedModelIsReportedOnlyOnce(t *testing.T) {
	const m = "probe-model-for-dedupe-test"
	if !NoteUnpricedModel(m) {
		t.Fatal("first sighting of an unpriced model was not reported")
	}
	for i := 0; i < 5; i++ {
		if NoteUnpricedModel(m) {
			t.Fatal("the same unpriced model was reported more than once")
		}
	}
	var found bool
	for _, got := range UnpricedModels() {
		if got == m {
			found = true
		}
	}
	if !found {
		t.Fatalf("UnpricedModels() omitted %q — an operator checking before publishing would "+
			"be told the costs are sound when they are not", m)
	}
}

// The tracked set is keyed on a string that arrives in a developer's request, so it must not
// grow without bound.
func TestUnpricedTrackingIsBounded(t *testing.T) {
	for i := 0; i < maxUnpricedTracked+200; i++ {
		NoteUnpricedModel("flood-" + string(rune('a'+i%26)) + itoa(i))
	}
	if n := len(UnpricedModels()); n > maxUnpricedTracked {
		t.Fatalf("tracked %d models, cap is %d — the key comes from a request, so an unbounded "+
			"set is a leak an agent could drive", n, maxUnpricedTracked)
	}
}

// An empty model name is not a model and must not occupy a slot in the bounded set.
func TestEmptyModelIsNotTracked(t *testing.T) {
	if NoteUnpricedModel("") {
		t.Fatal("an empty model name was recorded as an unpriced model")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
