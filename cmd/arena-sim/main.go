// Command arena-sim runs full Goofspiel matches through the REAL engine entirely
// offline — no server, no onboarding, no coins. It's the agent author's local
// proving ground: pit two strategies, watch every round, and confirm the match
// is provably fair (the same commit-reveal seed + replay verification the live
// arena uses). Fork the strategies map below to drop in your own logic and see
// how it does before you ever deploy.
//
//	go run ./cmd/arena-sim                          # highest vs lowest, one match
//	go run ./cmd/arena-sim -a highest -b random -n 200   # win-rate over 200 games
//	go run ./cmd/arena-sim -seed mymatch -v          # reproducible, round-by-round
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	mrand "math/rand"
	"os"
	"sort"
	"strings"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/replay"
)

// Policy chooses a card to play from the seat's current view of the game. It MUST
// return a card still in hand (s.Hands[seat]); returning an illegal card is
// reported as an error, which is exactly the kind of bug this tool exists to
// surface before you deploy. Goofspiel is open-information, so a policy may also
// read the opponent's remaining hand (s.Hands[1-seat]) and the full history.
type Policy func(s gs.State, seat int, rng *mrand.Rand) int

func highest(s gs.State, seat int, _ *mrand.Rand) int { return pick(s.Hands[seat], true) }
func lowest(s gs.State, seat int, _ *mrand.Rand) int  { return pick(s.Hands[seat], false) }

func randomPlay(s gs.State, seat int, rng *mrand.Rand) int {
	h := s.Hands[seat]
	return h[rng.Intn(len(h))]
}

// proportional bids the card closest to the current prize's value — "pay what
// it's worth". A reasonable baseline to test a smarter strategy against.
func proportional(s gs.State, seat int, _ *mrand.Rand) int {
	prize, h := s.CurrentPrize(), s.Hands[seat]
	best, bestD := h[0], 1<<30
	for _, c := range h {
		d := c - prize
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

var strategies = map[string]Policy{
	"highest":      highest,
	"lowest":       lowest,
	"random":       randomPlay,
	"proportional": proportional,
}

func pick(hand []int, max bool) int {
	v := hand[0]
	for _, c := range hand[1:] {
		if (max && c > v) || (!max && c < v) {
			v = c
		}
	}
	return v
}

type outcome struct {
	Winner   int    // gs.SeatA | gs.SeatB | gs.Tie
	ScoreA   int
	ScoreB   int
	Seed     string
	Commit   string
	Hash     string
	Verified bool
}

func main() {
	aName := flag.String("a", "highest", "seat A strategy: "+strategyList())
	bName := flag.String("b", "lowest", "seat B strategy: "+strategyList())
	seedStr := flag.String("seed", "", "match seed (hex or text); random if empty — always printed so a run is reproducible")
	rounds := flag.Int("rounds", 13, "number of rounds (deck is 1..rounds)")
	fairness := flag.String("fairness", "shuffled", "prize order: shuffled (hidden, commit-revealed) | open (fixed)")
	n := flag.Int("n", 1, "number of matches to simulate (different seed each); prints win-rates when >1")
	verbose := flag.Bool("v", false, "round-by-round trace (implied when -n=1)")
	asJSON := flag.Bool("json", false, "emit machine-readable JSON")
	flag.Parse()

	polA, okA := strategies[*aName]
	polB, okB := strategies[*bName]
	if !okA || !okB {
		fmt.Fprintf(os.Stderr, "unknown strategy (have: %s)\n", strategyList())
		os.Exit(2)
	}

	baseSeed := *seedStr
	if baseSeed == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		baseSeed = hex.EncodeToString(b)
	}

	cfg := gs.DefaultConfig()
	if *rounds > 0 {
		cards := make([]int, *rounds)
		for i := range cards {
			cards[i] = i + 1
		}
		cfg.Cards, cfg.Rounds = cards, *rounds
	}
	cfg.FairnessMode = *fairness
	eng := gs.New(cfg)

	trace := *verbose || *n == 1

	var aw, bw, tie int
	var last outcome
	for i := 0; i < *n; i++ {
		seed := baseSeed
		if *n > 1 {
			seed = fmt.Sprintf("%s:%d", baseSeed, i)
		}
		oc, err := simulate(eng, []byte(seed), polA, polB, trace && !*asJSON)
		if err != nil {
			fmt.Fprintf(os.Stderr, "match %d (seed %q): %v\n", i, seed, err)
			os.Exit(1)
		}
		oc.Seed = seed
		switch oc.Winner {
		case gs.SeatA:
			aw++
		case gs.SeatB:
			bw++
		default:
			tie++
		}
		last = oc
	}

	if *asJSON {
		emitJSON(*aName, *bName, *n, aw, bw, tie, last)
		return
	}
	if *n == 1 {
		reportOne(*aName, *bName, last)
		return
	}
	reportMany(*aName, *bName, *n, aw, bw, tie)
}

// simulate plays one full match and (always) verifies its replay — proving the
// engine produced a sound, reproducible game from the committed seed.
func simulate(eng *gs.Engine, seed []byte, polA, polB Policy, trace bool) (outcome, error) {
	rngA := newRNG(seed, gs.SeatA)
	rngB := newRNG(seed, gs.SeatB)
	s, events := eng.Init(seed)

	if trace {
		fmt.Printf("  prize order committed as %s\n", gs.Commit(seed)[:16]+"…")
		fmt.Printf("  %-5s %-6s %-6s %-6s %-8s\n", "round", "prize", "A", "B", "winner")
	}

	for !s.Finished {
		ca, err := legal(polA, s, gs.SeatA, rngA)
		if err != nil {
			return outcome{}, fmt.Errorf("seat A: %w", err)
		}
		cb, err := legal(polB, s, gs.SeatB, rngB)
		if err != nil {
			return outcome{}, fmt.Errorf("seat B: %w", err)
		}
		var e1, e2, e3 []gs.Event
		if s, e1, err = eng.Seal(s, gs.SeatA, ca); err != nil {
			return outcome{}, err
		}
		if s, e2, err = eng.Seal(s, gs.SeatB, cb); err != nil {
			return outcome{}, err
		}
		if s, e3, err = eng.Resolve(s); err != nil {
			return outcome{}, err
		}
		events = append(append(append(events, e1...), e2...), e3...)
		if trace {
			r := s.History[len(s.History)-1]
			fmt.Printf("  %-5d %-6d %-6d %-6d %-8s\n", r.Round, r.Prize, r.Cards[gs.SeatA], r.Cards[gs.SeatB], winnerLabel(r.Winner))
		}
	}

	hash, err := replay.Hash(events)
	if err != nil {
		return outcome{}, err
	}
	verified := replay.Verify(seed, events) == nil
	return outcome{
		Winner: s.Winner, ScoreA: s.Scores[gs.SeatA], ScoreB: s.Scores[gs.SeatB],
		Commit: gs.Commit(seed), Hash: hash, Verified: verified,
	}, nil
}

func legal(p Policy, s gs.State, seat int, rng *mrand.Rand) (int, error) {
	c := p(s, seat, rng)
	for _, h := range s.Hands[seat] {
		if h == c {
			return c, nil
		}
	}
	return 0, fmt.Errorf("strategy returned illegal card %d; legal cards are %v", c, s.Hands[seat])
}

func reportOne(a, b string, oc outcome) {
	fmt.Printf("\n  result: %s\n", winnerSentence(a, b, oc))
	fmt.Printf("  score:  A(%s) %d — %d B(%s)\n", a, oc.ScoreA, oc.ScoreB, b)
	fmt.Printf("  seed:   %s\n", oc.Seed)
	fmt.Printf("  replay: hash %s… — %s\n", oc.Hash[:16], verifiedLabel(oc.Verified))
}

func reportMany(a, b string, n, aw, bw, tie int) {
	fmt.Printf("\n  %d matches: A(%s) %s\n", n, a, "")
	fmt.Printf("  %-14s %d wins  (%.1f%%)\n", a, aw, 100*float64(aw)/float64(n))
	fmt.Printf("  %-14s %d wins  (%.1f%%)\n", b, bw, 100*float64(bw)/float64(n))
	fmt.Printf("  %-14s %d ties  (%.1f%%)\n", "tie", tie, 100*float64(tie)/float64(n))
}

func emitJSON(a, b string, n, aw, bw, tie int, last outcome) {
	out := map[string]any{
		"strategy_a": a, "strategy_b": b, "matches": n,
		"a_wins": aw, "b_wins": bw, "ties": tie,
		"last": map[string]any{
			"winner": winnerLabel(last.Winner), "score_a": last.ScoreA, "score_b": last.ScoreB,
			"seed": last.Seed, "prize_seed_commit": last.Commit, "replay_hash": last.Hash, "replay_verified": last.Verified,
		},
	}
	b2, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b2))
}

func newRNG(seed []byte, seat int) *mrand.Rand {
	h := fnv.New64a()
	_, _ = h.Write(seed)
	_, _ = h.Write([]byte{byte(seat)})
	return mrand.New(mrand.NewSource(int64(h.Sum64()))) //nolint:gosec // deterministic sim RNG, not security-sensitive
}

func winnerLabel(w int) string {
	switch w {
	case gs.SeatA:
		return "A"
	case gs.SeatB:
		return "B"
	default:
		return "tie"
	}
}

func winnerSentence(a, b string, oc outcome) string {
	switch oc.Winner {
	case gs.SeatA:
		return fmt.Sprintf("A (%s) wins", a)
	case gs.SeatB:
		return fmt.Sprintf("B (%s) wins", b)
	default:
		return "tie"
	}
}

func verifiedLabel(ok bool) string {
	if ok {
		return "replay verified ✓"
	}
	return "REPLAY VERIFICATION FAILED ✗"
}

func strategyList() string {
	names := make([]string, 0, len(strategies))
	for k := range strategies {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}
