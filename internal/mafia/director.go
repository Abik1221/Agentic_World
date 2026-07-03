package mafia

import (
	"context"
	"sync"
	"time"
)

// director drives one match. It walks its script on a fixed cadence, assigns a
// monotonic sequence to each event, broadcasts it through the Hub, and retains
// the current run's frames so late joiners can replay the match-so-far. When the
// script ends it pauses, then restarts the match — the demo table is always live.
type director struct {
	matchID string
	title   string
	script  []scriptEvent
	step    time.Duration // delay between events
	pause   time.Duration // intermission between match runs
	hub     *Hub

	mu     sync.Mutex
	seq    int     // monotonic across runs (the SSE id space)
	run    []frame // frames of the current run, for backlog/resume
	day    int
	phase  string
	alive  int
	winner string
}

func newDirector(id, title string, script []scriptEvent, hub *Hub, step, pause time.Duration) *director {
	return &director{
		matchID: id, title: title, script: script, hub: hub,
		step: step, pause: pause,
		day: 1, phase: "night", alive: rosterSize,
	}
}

type matchStatus struct {
	day    int
	phase  string
	alive  int
	winner string
}

func (d *director) status() matchStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return matchStatus{day: d.day, phase: d.phase, alive: d.alive, winner: d.winner}
}

// backlog returns the current run's frames after lastSeq (the whole run for a
// fresh viewer, since lastSeq is -1).
func (d *director) backlog(lastSeq int) []frame {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]frame, 0, len(d.run))
	for _, fr := range d.run {
		if fr.seq > lastSeq {
			out = append(out, fr)
		}
	}
	return out
}

// loop runs the match forever until the context is cancelled.
func (d *director) loop(ctx context.Context) {
	t := time.NewTicker(d.step)
	defer t.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if i == 0 {
				d.resetRun()
			}
			ev := d.script[i]
			d.mu.Lock()
			d.seq++
			fr := encodeEvent(d.seq, ev)
			d.run = append(d.run, fr)
			d.applyStatusLocked(ev)
			d.mu.Unlock()

			d.hub.broadcast(d.matchID, fr)

			i++
			if i >= len(d.script) {
				i = 0
				select {
				case <-ctx.Done():
					return
				case <-time.After(d.pause):
				}
			}
		}
	}
}

// resetRun clears the retained run and resets the derived status for a new match.
func (d *director) resetRun() {
	d.mu.Lock()
	d.run = d.run[:0]
	d.day, d.phase, d.alive, d.winner = 1, "night", rosterSize, ""
	d.mu.Unlock()
}

// applyStatusLocked folds an event into the lightweight derived status surfaced
// by the live-match list. Caller holds d.mu.
func (d *director) applyStatusLocked(ev scriptEvent) {
	switch p := ev.payload.(type) {
	case phasePayload:
		d.day, d.phase = p.Day, p.Phase
	case eliminatePayload:
		if d.alive > 0 {
			d.alive--
		}
	case victoryPayload:
		d.winner = p.Team
	}
}
