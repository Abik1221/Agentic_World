package main

import (
	"log"
	"sync"
	"time"
)

// Running a BATCH of matches in one process.
//
// # Why this exists
//
// gamelab ends in `select {}` — it onboards its agents, starts a table, and then serves turns
// forever. That is right for watching one match, and it makes a batch impossible: a shell loop
// like `for i in 1 2 3; do gamelab; done` never reaches its second iteration.
//
// Working around it by killing the process between runs does not work either, and cost three
// failed runs to learn: the next invocation races the dying one for ports 9101-9104 and dies
// with "address already in use", while a run that IS still alive silently grabs the port a
// later run wanted.
//
// So a batch runs inside ONE process. That also happens to be the right shape for the thing a
// batch is for — collecting real model-backed matches:
//
//   - The four agents are onboarded, funded and endpoint-verified ONCE, not once per match.
//     Onboarding is the slow part and it has nothing to do with what is being measured.
//   - Matches run strictly one after another. On a rate-limited free tier (Groq: 30 RPM,
//     14.4k RPD) overlapping matches are how a batch turns into a wall of 429s.
//   - There is one place that knows how many matches have finished, so the process can exit
//     when the batch is done instead of hanging and needing to be killed.

// matchTracker counts DISTINCT finished matches.
//
// Distinct, because /game-end is delivered to every seat: a 4-seat Mafia table produces four
// game-end calls for one match, and counting calls would end a 10-match batch after three.
type matchTracker struct {
	mu   sync.Mutex
	seen map[string]bool
}

var tracker = &matchTracker{seen: map[string]bool{}}

// noteMatchFinished records that a match ended. Safe to call from any seat's HTTP handler.
func noteMatchFinished(matchID string) {
	if matchID == "" {
		// A game-end with no id cannot be de-duplicated, so counting it could end the batch
		// early on a single match's four deliveries. Ignoring it can only make a batch run
		// long, which the caller notices; the other way loses matches silently.
		return
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.seen[matchID] {
		return
	}
	tracker.seen[matchID] = true
}

// finishedCount is how many distinct matches have completed.
func finishedCount() int {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return len(tracker.seen)
}

// awaitCount blocks until at least `want` matches total have finished, or the timeout elapses.
//
// A TOTAL, not "one more than when this slot started". Waiting for one more lets a match from
// an EARLIER slot satisfy a later one: observed in the first real batch, where match 1 timed
// out at three minutes, finished nine seconds into slot 2, and was credited to slot 2 — whose
// own match was still running. The batch then reported a match complete that had not been
// started by that slot, and the run ended with one real match while the log implied two.
//
// Counting to a total is immune to that. Slot i waits for i completions, so a straggler from
// an earlier slot advances the total honestly without any slot claiming a match twice.
func awaitCount(want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if finishedCount() >= want {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// runBatch plays n matches one after another and returns when they are done.
//
// start is whatever begins one match — the same staked or free path a single run uses, so a
// batch exercises exactly the code a normal run does rather than a parallel implementation.
//
// A match that does not finish inside perMatch is NOT retried. Retrying would hide the thing
// most worth knowing (that a table stalled) behind a batch that eventually reports success,
// and on a rate-limited key a retry storm is the fastest way to spend the day's budget. The
// batch says which match number stalled and moves to the next one.
func runBatch(lg *log.Logger, n int, perMatch time.Duration, start func() error) {
	for i := 1; i <= n; i++ {
		lg.Printf("BATCH %d/%d starting (perMatch timeout %s)", i, n, perMatch)
		if err := start(); err != nil {
			lg.Printf("BATCH %d/%d could not start: %v — stopping the batch here rather than "+
				"looping on a failure that will repeat", i, n, err)
			return
		}
		if !awaitCount(i, perMatch) {
			lg.Printf("BATCH %d/%d did NOT finish within %s — not retried; continuing so the "+
				"rest of the batch still runs", i, n, perMatch)
			continue
		}
		lg.Printf("BATCH %d/%d finished (%d matches complete)", i, n, finishedCount())
	}
	lg.Printf("BATCH DONE — %d matches completed", finishedCount())
}

// stringField reads a top-level string from a decoded JSON body.
//
// The game-end payloads differ per game (Goofspiel's result is flat, Monopoly's carries a whole
// final board), but every one of them carries match_id at the top level, which is the only
// field a batch needs.
func stringField(body map[string]any, key string) string {
	if s, ok := body[key].(string); ok {
		return s
	}
	return ""
}
