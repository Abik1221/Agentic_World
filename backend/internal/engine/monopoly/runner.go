package monopoly

import "errors"

// runner.go is the dedicated, self-contained environment that actually PLAYS a
// match. The engine is pure rules; the Table is the thin, deterministic shell
// that owns the live state, the event log, and the seating — driving bot agents
// automatically and pausing for the human seat.
//
// This is what makes a single user feel like they joined a full table: the human
// occupies one seat, the sandbox seats bot agents in the rest, and the Table runs
// every bot decision so the person only ever has to make their own moves.
//
// It is NOT the production match worker (no timers, persistence, or networking) —
// it is the embeddable game loop those layers wrap, and the harness the demo and
// tests use to play complete games.

// ErrSeatNotPending is returned when a caller submits an action for a seat the
// engine is not currently waiting on.
var ErrSeatNotPending = errors.New("monopoly: that seat has no pending decision")

// Table is one live match: engine + seed + state + log + seating.
type Table struct {
	engine *Engine
	seed   []byte
	agents []Agent // indexed by seat; a nil entry is a human-controlled seat
	state  State
	log    []Event
	moves  []Move // every applied (seat, action), in order — the replay script

	// MaxSteps bounds AdvanceBots/PlayOut so a logic error can never spin forever.
	MaxSteps int
}

// NewTable creates a match. len(agents) must equal cfg.Players; use nil for any
// human seat. The seed drives both the engine (dice/decks) and the bots, so the
// whole table is reproducible.
func NewTable(cfg Config, seed []byte, agents []Agent) *Table {
	e := New(cfg)
	if len(agents) < e.cfg.Players {
		// Pad missing seats with bots so the table is always complete and runnable.
		for len(agents) < e.cfg.Players {
			i := len(agents)
			agents = append(agents, NewBot(defaultName(i), DefaultStyles[i%len(DefaultStyles)], seed, i))
		}
	}
	agents = agents[:e.cfg.Players]
	s, evs := e.Init(seed)
	return &Table{
		engine:   e,
		seed:     seed,
		agents:   agents,
		state:    s,
		log:      append([]Event(nil), evs...),
		MaxSteps: 5_000_000,
	}
}

// Engine exposes the underlying engine (for LegalActions, Config, etc.).
func (t *Table) Engine() *Engine { return t.engine }

// State returns the current game state (a copy is cheap and safe to read).
func (t *Table) State() State { return t.state }

// Log returns the full append-only event log so far.
func (t *Table) Log() []Event { return t.log }

// Finished / Winner report the match outcome.
func (t *Table) Finished() bool { return t.state.Finished }
func (t *Table) Winner() int    { return t.state.Winner }

// IsHuman reports whether a seat is human-controlled (no agent).
func (t *Table) IsHuman(seat int) bool {
	return seat >= 0 && seat < len(t.agents) && t.agents[seat] == nil
}

// SeatName returns a display name for a seat (agent name, or "You" for a human).
func (t *Table) SeatName(seat int) string {
	if t.IsHuman(seat) {
		return "You"
	}
	if seat >= 0 && seat < len(t.agents) {
		return t.agents[seat].Name()
	}
	return "seat " + itoa(seat)
}

// PendingSeat returns the seat the engine is waiting on and whether it is human.
func (t *Table) PendingSeat() (seat int, human bool) {
	seat = t.engine.pendingActor(t.state)
	return seat, t.IsHuman(seat)
}

// LegalActions lists the action kinds the pending human seat may submit.
func (t *Table) LegalActions(seat int) []string { return t.engine.LegalActions(t.state, seat) }

// Apply submits one action for `seat` (the human path). It is transactional: on
// error the table state is unchanged and the error is returned for the caller to
// surface and re-prompt.
func (t *Table) Apply(seat int, a Action) error {
	if ps, _ := t.PendingSeat(); ps != seat {
		return ErrSeatNotPending
	}
	return t.commit(seat, a)
}

// commit applies one action through the engine and, on success, records it to the
// move log so the match can be replayed and verified. It is the single choke point
// every applied action flows through.
func (t *Table) commit(seat int, a Action) error {
	ns, evs, err := t.engine.Step(t.state, seat, a, t.seed)
	if err != nil {
		return err
	}
	t.state = ns
	t.log = append(t.log, evs...)
	t.moves = append(t.moves, Move{Seat: seat, Action: a})
	return nil
}

// AdvanceBots plays every pending bot decision, stopping at the human seat or the
// end of the match. Returns the events produced during this advance.
func (t *Table) AdvanceBots() []Event {
	start := len(t.log)
	for steps := 0; steps < t.MaxSteps && !t.state.Finished; steps++ {
		seat, human := t.PendingSeat()
		if human {
			break
		}
		t.stepAgent(seat)
	}
	return t.log[start:]
}

// PlayOut runs the table to completion. Bot seats are driven by their agents;
// any pending human seat is resolved with the deterministic timeout default, so
// an all-or-partial-bot table always finishes. Returns events produced.
func (t *Table) PlayOut() []Event {
	start := len(t.log)
	for steps := 0; steps < t.MaxSteps && !t.state.Finished; steps++ {
		seat, human := t.PendingSeat()
		if human {
			t.timeoutPending()
			continue
		}
		t.stepAgent(seat)
	}
	return t.log[start:]
}

// TimeoutPending applies the deterministic default action for the pending seat
// (e.g. a human who ran out of time). Safe to call any time before the match ends.
func (t *Table) TimeoutPending() []Event {
	start := len(t.log)
	t.timeoutPending()
	return t.log[start:]
}

// stepAgent asks the seat's agent for a move and applies it. Robustness: if the
// agent errors or returns an illegal action, the table falls back to a
// deterministic timeout so a misbehaving agent can never wedge the match.
func (t *Table) stepAgent(seat int) {
	if err := t.commit(seat, t.agents[seat].Decide(t.engine, t.state, seat)); err != nil {
		t.timeoutPending() // bad agent move -> deterministic default, still recorded
	}
}

// timeoutPending applies the deterministic default action for the pending seat and
// records it. (Behaviourally identical to engine.ForceTimeout, but recorded.)
func (t *Table) timeoutPending() {
	if t.state.Finished {
		return
	}
	seat := t.engine.pendingActor(t.state)
	_ = t.commit(seat, t.engine.defaultAction(t.state))
}

// Moves returns the ordered (seat, action) script of the match so far.
func (t *Table) Moves() []Move { return append([]Move(nil), t.moves...) }

// ReplayHash is the canonical hash of the match's event log (see replay.go).
func (t *Table) ReplayHash() string { return ReplayHash(t.log) }

func defaultName(seat int) string {
	names := []string{"Athena", "Borg", "Cleo", "Dax", "Echo", "Foxtrot", "Gizmo", "Helix"}
	if seat >= 0 && seat < len(names) {
		return names[seat]
	}
	return "Bot-" + itoa(seat)
}
