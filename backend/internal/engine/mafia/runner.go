package mafia

import (
	"errors"
	"sort"
	"strconv"
)

// runner.go is the dedicated, embeddable environment that actually plays a Mafia
// match. The engine is pure rules; the Table owns the live state, the event log,
// and the seating, and drives the many simultaneous actors of each phase.
//
// This is what lets a single user join a full table: the user's agent (or a human
// at the keyboard) takes one seat, the sandbox seats deterministic bots in the
// rest, and the Table runs every bot — handing each only its REDACTED view — so the
// person only ever decides their own seat.
//
// It is NOT the production match worker (no timers, persistence, or networking); it
// is the game loop those layers wrap, and the harness the demo and tests use.

// ErrNotPending is returned when a seat submits out of turn (it is not currently
// owed an action this phase).
var ErrNotPending = errors.New("mafia: seat has no pending action this phase")

// Table is one live match.
type Table struct {
	engine   *Engine
	seed     []byte
	seats    []int         // sorted living-or-dead seat ids (e.g. 1..12)
	agents   map[int]Agent // bot/external agent per seat; humans are absent here
	humans   map[int]bool  // seats with no agent (human-controlled)
	defaults map[int]Agent // a bot for EVERY seat, used to time out a human
	state    State
	log      []Event
	moves    []Move // every applied (seat, action), in order — the replay script

	MaxSteps int // robustness bound on AdvanceBots/PlayOut
}

// StandardSeats returns 1..RosterSize, the canonical 12-seat table.
func StandardSeats() []int {
	seats := make([]int, RosterSize)
	for i := range seats {
		seats[i] = i + 1
	}
	return seats
}

// NewTable creates a match. Pass agents == nil for an all-bot table; otherwise a
// seat present (non-nil) in the map uses that agent and any other seat is treated
// as human. maxDays caps the game (<=0 uses a safe default).
func NewTable(seats []int, seed []byte, agents map[int]Agent, maxDays int) *Table {
	if len(seats) == 0 {
		seats = StandardSeats()
	}
	if maxDays <= 0 {
		maxDays = 60
	}
	e := NewWithMaxDays(maxDays)
	s, evs := e.Init(seed, seats)

	sorted := append([]int(nil), seats...)
	sort.Ints(sorted)
	t := &Table{
		engine:   e,
		seed:     seed,
		seats:    sorted,
		agents:   map[int]Agent{},
		humans:   map[int]bool{},
		defaults: map[int]Agent{},
		state:    s,
		log:      append([]Event(nil), evs...),
		MaxSteps: 2_000_000,
	}
	for _, seat := range sorted {
		t.defaults[seat] = NewBot(defaultName(seat), seed, seat)
		switch {
		case agents == nil:
			t.agents[seat] = t.defaults[seat]
		case agents[seat] != nil:
			t.agents[seat] = agents[seat]
		default:
			t.humans[seat] = true
		}
	}
	return t
}

// State / Log / Finished / Winner expose the match.
func (t *Table) State() State        { return t.state }
func (t *Table) Log() []Event        { return t.log }
func (t *Table) Finished() bool      { return t.state.Finished }
func (t *Table) Winner() string      { return t.state.Winner }
func (t *Table) Seats() []int        { return append([]int(nil), t.seats...) }
func (t *Table) RoleOf(s int) string { return t.state.Roles[s] }

// IsHuman reports whether a seat is human-controlled.
func (t *Table) IsHuman(seat int) bool { return t.humans[seat] }

// SeatName returns a display name for a seat.
func (t *Table) SeatName(seat int) string {
	if t.humans[seat] {
		return "You"
	}
	if a, ok := t.agents[seat]; ok {
		return a.Name()
	}
	return "seat " + strconv.Itoa(seat)
}

// PendingActors lists the seats still owed an action this phase.
func (t *Table) PendingActors() []int { return PendingActors(t.state) }

// ViewFor returns the redacted view a human/external agent at `seat` should see.
func (t *Table) ViewFor(seat int) AgentView { return BuildView(t.state, seat, t.log) }

// LegalActions lists the action kinds a seat may submit now.
func (t *Table) LegalActions(seat int) []string { return LegalActions(t.state, seat) }

// Apply submits one action for `seat` (the human/external-agent path). It is
// transactional: on error the state is unchanged and the error is returned.
func (t *Table) Apply(seat int, a Action) error {
	if !contains(PendingActors(t.state), seat) {
		return ErrNotPending
	}
	if !t.tryAct(seat, a) {
		// Re-run to surface the exact engine error to the caller.
		_, _, err := t.engine.Act(t.state, seat, a)
		return err
	}
	return nil
}

// Moves returns the ordered (seat, action) script of the match so far.
func (t *Table) Moves() []Move { return append([]Move(nil), t.moves...) }

// ReplayHash is the canonical hash of the match's event log (see replay.go).
func (t *Table) ReplayHash() string { return ReplayHash(t.log) }

// AdvanceBots plays every pending bot action, stopping when only human seats are
// pending or the match ends. Returns events produced.
func (t *Table) AdvanceBots() []Event {
	start := len(t.log)
	for steps := 0; steps < t.MaxSteps && !t.state.Finished; steps++ {
		pend := PendingActors(t.state)
		if len(pend) == 0 {
			break
		}
		acted := false
		for _, seat := range pend {
			if agent, ok := t.agents[seat]; ok {
				t.applyAgent(seat, agent)
				acted = true
				break
			}
		}
		if !acted {
			break // only human seats remain pending
		}
	}
	return t.log[start:]
}

// PlayOut runs to completion. Bot seats use their agents; human seats are filled
// with their deterministic default bot, so the table always finishes.
func (t *Table) PlayOut() []Event {
	start := len(t.log)
	for steps := 0; steps < t.MaxSteps && !t.state.Finished; steps++ {
		pend := PendingActors(t.state)
		if len(pend) == 0 {
			break
		}
		seat := pend[0]
		agent := t.agents[seat]
		if agent == nil {
			agent = t.defaults[seat]
		}
		t.applyAgent(seat, agent)
	}
	return t.log[start:]
}

// TimeoutPending fills the current pending seats with their deterministic default
// action (e.g. an abandoned human seat).
func (t *Table) TimeoutPending() []Event {
	start := len(t.log)
	for _, seat := range PendingActors(t.state) {
		if t.state.Finished {
			break
		}
		if contains(PendingActors(t.state), seat) {
			t.applyAgent(seat, t.defaults[seat])
		}
	}
	return t.log[start:]
}

// applyAgent asks the seat's agent for an action and applies it. Robustness: an
// agent that errors or returns an illegal action falls back to its default bot,
// then to a deterministic engine timeout, so a bad agent can never wedge a match.
func (t *Table) applyAgent(seat int, agent Agent) {
	if t.tryAct(seat, agent.Decide(t.ViewFor(seat))) {
		return
	}
	if t.tryAct(seat, t.defaults[seat].Decide(t.ViewFor(seat))) {
		return
	}
	t.tryAct(seat, defaultActionFor(t.state, seat, t.seed)) // last resort, still recorded
}

// tryAct applies one action and, on success, records it to the move log so the
// match can be replayed and verified. Every applied action flows through here.
func (t *Table) tryAct(seat int, a Action) bool {
	ns, evs, err := t.engine.Act(t.state, seat, a)
	if err != nil {
		return false
	}
	t.state = ns
	t.log = append(t.log, evs...)
	t.moves = append(t.moves, Move{Seat: seat, Action: a})
	return true
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func defaultName(seat int) string {
	names := []string{"Athena", "Borg", "Cleo", "Dax", "Echo", "Foxtrot", "Gizmo", "Helix", "Iris", "Juno", "Kilo", "Lyra"}
	if seat-1 >= 0 && seat-1 < len(names) {
		return names[seat-1]
	}
	return "Bot-" + strconv.Itoa(seat)
}
