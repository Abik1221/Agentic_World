// Package devplatform is the engine-agnostic developer-platform layer for
// Agent Arena. It gives the developer portal, matchmaker, and certification
// pipeline a single, uniform way to reason about every supported game without
// knowing any concrete engine type.
//
// The three shipped games — Goofspiel, Mafia, and Monopoly — each have their
// own hand-written, deterministic engine under internal/engine/*. Those engines
// intentionally do NOT share a Go interface (their state, actions, and seat
// models differ). This package is the seam that unifies them: each game is
// described by a GameSpec whose run closure drives the real engine to
// completion and returns a normalized MatchOutcome.
//
// This is what makes the platform "scalable across games": adding a fourth game
// is a single GameSpec registration, and every platform capability
// (certification, sandbox execution, tournament version-lock, telemetry) works
// against it unchanged.
package devplatform

import "fmt"

// GameID is the stable, public identifier for a game across the API, database,
// and SDK. Never renamed once shipped (it is embedded in match records).
type GameID string

// Supported games. These map 1:1 to internal/engine/* packages.
const (
	GameGoofspiel GameID = "goofspiel"
	GameMafia     GameID = "mafia"
	GameMonopoly  GameID = "monopoly"
)

// InfoModel classifies how information is hidden in a game. It drives the
// spec's "Allowed vs Forbidden Information" rules and the anti-leak checks the
// sandbox enforces on agent I/O.
type InfoModel string

const (
	// InfoSimultaneous: both seats commit secretly, revealed together (Goofspiel).
	InfoSimultaneous InfoModel = "simultaneous_reveal"
	// InfoHiddenRole: each seat holds a private role/knowledge set (Mafia).
	InfoHiddenRole InfoModel = "hidden_role"
	// InfoPerfectPlusChance: full board is public; only future dice are unknown (Monopoly).
	InfoPerfectPlusChance InfoModel = "perfect_plus_chance"
)

// MatchOutcome is the normalized result of one headless sandbox match, produced
// identically for every game so the certifier never special-cases an engine.
type MatchOutcome struct {
	Completed  bool   // reached a terminal state (no step cap / wedge)
	Winner     string // normalized winner label (e.g. "seat_0", "town", "tie")
	Moves      int    // number of applied legal actions
	Events     int    // length of the produced event log
	ReplayHash string // canonical hash of the match; equal seed => equal hash
	Seats      int    // seats actually played
}

// GameSpec is the platform-level, engine-agnostic description of a game.
type GameSpec struct {
	ID             GameID
	Name           string
	Summary        string
	Info           InfoModel
	MinSeats       int
	MaxSeats       int
	PreferredSeats int    // seat count used for certification matches
	EngineVersion  string // rule-set version, frozen into every match record

	// run drives the concrete engine to completion for the given seat count and
	// seed, returning a normalized outcome. The single seam per game.
	run func(seats int, seed []byte) (MatchOutcome, error)
}

// Run executes one headless match, clamping the requested seat count into the
// game's supported range so a caller can never start an out-of-bounds table.
func (g GameSpec) Run(seats int, seed []byte) (MatchOutcome, error) {
	if g.run == nil {
		return MatchOutcome{}, fmt.Errorf("devplatform: game %q has no runner", g.ID)
	}
	if seats < g.MinSeats {
		seats = g.MinSeats
	}
	if g.MaxSeats > 0 && seats > g.MaxSeats {
		seats = g.MaxSeats
	}
	return g.run(seats, seed)
}

// CertSeats returns the seat count used for certification (preferred, clamped).
func (g GameSpec) CertSeats() int {
	s := g.PreferredSeats
	if s < g.MinSeats {
		s = g.MinSeats
	}
	if g.MaxSeats > 0 && s > g.MaxSeats {
		s = g.MaxSeats
	}
	return s
}

// Registry is the set of games a platform instance offers. It is the source of
// truth behind "Select Supported Games" in the registration flow.
type Registry struct {
	games map[GameID]GameSpec
	order []GameID
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{games: map[GameID]GameSpec{}}
}

// Register adds (or replaces) a game. Registration order is preserved for
// stable listing in the portal.
func (r *Registry) Register(g GameSpec) {
	if _, exists := r.games[g.ID]; !exists {
		r.order = append(r.order, g.ID)
	}
	r.games[g.ID] = g
}

// Get returns a game by id.
func (r *Registry) Get(id GameID) (GameSpec, bool) {
	g, ok := r.games[id]
	return g, ok
}

// All returns every registered game in registration order.
func (r *Registry) All() []GameSpec {
	out := make([]GameSpec, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.games[id])
	}
	return out
}

// IDs returns the registered game ids in order.
func (r *Registry) IDs() []GameID {
	return append([]GameID(nil), r.order...)
}

// DefaultRegistry returns a registry with all three shipped games wired to their
// real engines. This is what a server or the certify CLI uses.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(goofspielSpec())
	r.Register(mafiaSpec())
	r.Register(monopolySpec())
	return r
}
