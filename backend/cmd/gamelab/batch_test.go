package main

import (
	"errors"
	"io"
	"log"
	"testing"
	"time"
)

var errStartFailed = errors.New("start failed")

// resetTracker clears global batch state between tests.
func resetTracker() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.seen = map[string]bool{}
}

// TestABatchCountsMatchesNotGameEndDeliveries is the whole reason the tracker de-duplicates.
//
// /game-end is delivered to EVERY seat, so one 4-seat Mafia table produces four calls. Counting
// calls would make a 10-match batch stop after three, and the run would report success while
// having collected under a third of the data it was asked for — the kind of shortfall that is
// only noticed much later, when the numbers are already published.
func TestABatchCountsMatchesNotGameEndDeliveries(t *testing.T) {
	resetTracker()
	for i := 0; i < 4; i++ {
		noteMatchFinished("mf_one")
	}
	if got := finishedCount(); got != 1 {
		t.Fatalf("four game-end deliveries for one table counted as %d matches, want 1", got)
	}
	noteMatchFinished("mf_two")
	if got := finishedCount(); got != 2 {
		t.Fatalf("count = %d after a second table, want 2", got)
	}
}

// TestAGameEndWithNoMatchIdIsIgnored: an id-less payload cannot be de-duplicated, so counting
// it would let one table's four deliveries end a batch early. Ignoring it can only make a batch
// run long, which is visible; the other way loses matches silently.
func TestAGameEndWithNoMatchIdIsIgnored(t *testing.T) {
	resetTracker()
	noteMatchFinished("")
	noteMatchFinished("")
	if got := finishedCount(); got != 0 {
		t.Fatalf("id-less game-ends counted as %d matches, want 0", got)
	}
}

// TestAStalledMatchDoesNotAbandonTheRestOfTheBatch: a table that never finishes must cost its
// own slot and nothing more. Aborting the batch would throw away the matches still to come;
// retrying would hide the stall and, on a rate-limited key, burn the day's budget.
func TestAStalledMatchDoesNotAbandonTheRestOfTheBatch(t *testing.T) {
	resetTracker()
	starts := 0
	quiet := log.New(io.Discard, "", 0)
	runBatch(quiet, 3, 40*time.Millisecond, func() error {
		starts++
		if starts != 2 { // match 2 stalls: no game-end for it
			noteMatchFinished(string(rune('a' + starts)))
		}
		return nil
	})
	if starts != 3 {
		t.Fatalf("started %d matches, want all 3 attempted despite the stall", starts)
	}
	if got := finishedCount(); got != 2 {
		t.Fatalf("%d matches completed, want 2 (the stalled one must not be counted)", got)
	}
}

// TestABatchStopsWhenAMatchCannotStart: a start failure repeats (no funds, platform down), so
// looping on it would produce a wall of identical errors instead of one legible one.
func TestABatchStopsWhenAMatchCannotStart(t *testing.T) {
	resetTracker()
	starts := 0
	quiet := log.New(io.Discard, "", 0)
	runBatch(quiet, 5, 40*time.Millisecond, func() error {
		starts++
		return errStartFailed
	})
	if starts != 1 {
		t.Fatalf("attempted %d starts after a start failure, want 1", starts)
	}
}

// TestAStragglerFromAnEarlierSlotIsNotCreditedTwice pins the miscount seen in the first real
// batch: match 1 timed out, finished nine seconds into slot 2, and satisfied slot 2 — whose own
// match was still running. The run reported a completion that slot had not produced.
func TestAStragglerFromAnEarlierSlotIsNotCreditedTwice(t *testing.T) {
	resetTracker()
	quiet := log.New(io.Discard, "", 0)
	slot := 0
	runBatch(quiet, 2, 300*time.Millisecond, func() error {
		slot++
		if slot == 2 {
			// The FIRST slot's match lands now, during slot 2. Slot 2 must not accept it.
			noteMatchFinished("m_from_slot_1")
		}
		return nil
	})
	// One real completion total. The old "one more than before" rule would have let slot 2
	// claim it and reported two.
	if got := finishedCount(); got != 1 {
		t.Fatalf("finished = %d, want 1 — a straggler must count once, toward the total", got)
	}
}
