package devplatform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/engine/mafia"
)

// This file adapts each concrete engine to the platform's GameSpec.run seam.
// Each runner seats deterministic reference agents in every chair and plays a
// full headless match, so the certification pipeline exercises the *real*
// engine end-to-end and produces a real, reproducible replay hash.

// ---------------------------------------------------------------------------
// Goofspiel
// ---------------------------------------------------------------------------

func goofspielSpec() GameSpec {
	return GameSpec{
		ID:             GameGoofspiel,
		Name:           "Goofspiel",
		Summary:        "Two-seat simultaneous-bid card duel; pure skill with sealed reveals.",
		Info:           InfoSimultaneous,
		MinSeats:       2,
		MaxSeats:       2,
		PreferredSeats: 2,
		EngineVersion:  goofspiel.Version,
		run:            runGoofspiel,
	}
}

// runGoofspiel drives the pure Goofspiel engine (Init/Seal/Resolve) to a finish.
// Both seats follow the same deterministic "bid nearest the pot" policy, so the
// match is fully reproducible from the seed.
func runGoofspiel(_ int, seed []byte) (MatchOutcome, error) {
	e := goofspiel.New(goofspiel.DefaultConfig())
	s, evs := e.Init(seed)
	log := append([]goofspiel.Event(nil), evs...)
	moves := 0

	for guard := 0; !s.Finished; guard++ {
		if guard > 100_000 {
			return MatchOutcome{}, fmt.Errorf("goofspiel: exceeded step guard (possible wedge)")
		}
		for seat := goofspiel.SeatA; seat <= goofspiel.SeatB; seat++ {
			legal := e.LegalActions(s, seat)
			if len(legal) == 0 {
				continue
			}
			ns, sev, err := e.Seal(s, seat, pickGoofspielCard(s.PrizePool, legal))
			if err != nil {
				return MatchOutcome{}, fmt.Errorf("goofspiel seal seat %d: %w", seat, err)
			}
			s, log = ns, append(log, sev...)
			moves++
		}
		ns, rev, err := e.Resolve(s)
		if err != nil {
			return MatchOutcome{}, fmt.Errorf("goofspiel resolve: %w", err)
		}
		s, log = ns, append(log, rev...)
	}

	return MatchOutcome{
		Completed: s.Finished,
		Winner:    seatOrTie(s.Winner, goofspiel.Tie),
		Moves:     moves,
		Events:    len(log),
		ReplayHash: hashReplay(struct {
			History any
			Scores  [2]int
			Winner  int
		}{s.History, s.Scores, s.Winner}),
		Seats: 2,
	}, nil
}

// pickGoofspielCard chooses the legal card closest to the current pot, breaking
// ties toward the smaller card. Deterministic given (pot, legal).
func pickGoofspielCard(pot int, legal []int) int {
	best, bestDist := legal[0], 1<<30
	for _, c := range legal {
		d := c - pot
		if d < 0 {
			d = -d
		}
		if d < bestDist || (d == bestDist && c < best) {
			best, bestDist = c, d
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Mafia (fixed 12-seat roster)
// ---------------------------------------------------------------------------

func mafiaSpec() GameSpec {
	return GameSpec{
		ID:             GameMafia,
		Name:           "Mafia",
		Summary:        "12-seat hidden-role social deduction; Town vs Mafia, night/day phases.",
		Info:           InfoHiddenRole,
		MinSeats:       mafia.RosterSize,
		MaxSeats:       mafia.RosterSize,
		PreferredSeats: mafia.RosterSize,
		EngineVersion:  "mafia-1.0.0",
		run:            runMafia,
	}
}

func runMafia(_ int, seed []byte) (MatchOutcome, error) {
	seats := mafia.StandardSeats()
	tbl := mafia.NewTable(seats, seed, nil, 60) // nil agents => reference bots in every chair
	tbl.PlayOut()
	if !tbl.Finished() {
		return MatchOutcome{Seats: len(seats)}, nil
	}
	return MatchOutcome{
		Completed:  true,
		Winner:     tbl.Winner(), // "town" | "mafia"
		Moves:      len(tbl.Moves()),
		Events:     len(tbl.Log()),
		ReplayHash: tbl.ReplayHash(),
		Seats:      len(seats),
	}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// seatOrTie renders a seat-index winner as "tie" or "seat_N".
func seatOrTie(winner, tieSentinel int) string {
	if winner == tieSentinel {
		return "tie"
	}
	return fmt.Sprintf("seat_%d", winner)
}

// hashReplay produces a canonical, reproducible hash of any JSON-encodable value.
// Used for engines that do not expose a native replay hash (Goofspiel).
func hashReplay(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
