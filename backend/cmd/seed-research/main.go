// Command seed-research publishes a completed lab benchmark to the public model board.
//
// # Why this exists
//
// The benchmark's evidence lives in agent_model_calls and match_players — the gateway's own
// records — which are per-decision and per-seat. The public board reads model_board_history,
// which is per-model-per-day. Without something to bridge them a finished run is real but
// invisible: the numbers exist and nobody can see them.
//
// # Why Bradley–Terry rather than win rate
//
// Win rate discards margin and cannot cope with an unbalanced schedule, and this schedule IS
// unbalanced — pairings were replayed unevenly, so a model that happened to be replayed against
// a weak opponent looks better than one that was not. Bradley–Terry fits a latent strength per
// model to the whole comparison graph at once, so an opponent's own strength is priced into
// every result. It is also what the published benchmarks in this space use, which makes our
// numbers comparable to theirs rather than merely adjacent.
//
// Intervals are bootstrapped over MATCHES, not decisions: matches are the independent unit here
// (the decisions inside one match are anything but independent), and resampling the wrong unit
// is the standard way to manufacture intervals that are far too tight.
//
// # The separability field is the honest part
//
// separability is the fraction of model pairs whose bootstrap intervals do NOT overlap. On a
// small run it is near zero, and that is the point: it publishes, as data, how much of the
// ordering the evidence actually supports. A board that shows a confident rank order built on
// four matches is worse than no board.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type seat struct {
	Seat       int    `json:"seat"`
	Model      string `json:"model"`
	FinalScore int    `json:"final_score"`
	CoinsDelta int    `json:"coins_delta"`
}

type match struct {
	MatchID string `json:"match_id"`
	Game    string `json:"game"`
	Seats   []seat `json:"seats"`
	// Fallbacks is the platform's own count of decisions it had to substitute because a
	// seat missed its window. It is NOT the same as the bind rate: every model call made
	// can be cryptographically bound while a seventh of the turns were never model calls
	// at all. A match whose loser went dark is a scoreline about an endpoint, not a model.
	Fallbacks int `json:"fallbacks"`
}

// pair is one head-to-head result between two models.
type pair struct{ a, b string } // a beat b

func main() {
	var (
		dataPath     = flag.String("data", "", "path to matches.json from a research run (required)")
		dsn          = flag.String("dsn", os.Getenv("DATABASE_URL"), "postgres DSN")
		game         = flag.String("game", "goofspiel", "only seat results from this game")
		boots        = flag.Int("bootstrap", 2000, "bootstrap resamples for the intervals")
		maxFallbacks = flag.Int("max-fallbacks", 0,
			"drop any match with more than this many platform fallbacks; 0 keeps only matches the models played end to end")
		day   = flag.String("day", "", "board day, YYYY-MM-DD (default: today UTC)")
		board = flag.String("board", "research", "board namespace to write under")
		dry   = flag.Bool("dry-run", false, "print the board and write nothing")
	)
	flag.Parse()
	if *dataPath == "" {
		log.Fatal("-data is required")
	}
	raw, err := os.ReadFile(*dataPath)
	if err != nil {
		log.Fatalf("read %s: %v", *dataPath, err)
	}
	var all []match
	if err := json.Unmarshal(raw, &all); err != nil {
		log.Fatalf("parse %s: %v", *dataPath, err)
	}

	// Only decisive two-seat results carry Bradley–Terry information. A draw and a
	// four-seat table are both real outcomes, but neither is a pairwise comparison, so
	// folding them in would mean inventing one.
	var results []pair
	skipped, contaminated := 0, 0
	for _, m := range all {
		if m.Game != *game || len(m.Seats) != 2 {
			skipped++
			continue
		}
		// Refuse a match the models did not actually finish.
		//
		// Two of this run's matches "completed" only because their agents were killed
		// mid-game: the platform played the remaining turns with its legal fallback, and
		// the surviving seat won 81-10 against an opponent that had stopped answering.
		// Counting that as a win would credit a model for its opponent's outage. The
		// threshold is zero rather than a tolerance because there is no principled level
		// of "somebody else played some of it" that still measures the model.
		if m.Fallbacks > *maxFallbacks {
			contaminated++
			continue
		}
		x, y := m.Seats[0], m.Seats[1]
		switch {
		case x.FinalScore > y.FinalScore:
			results = append(results, pair{x.Model, y.Model})
		case y.FinalScore > x.FinalScore:
			results = append(results, pair{y.Model, x.Model})
		default:
			skipped++
		}
	}
	if len(results) == 0 {
		log.Fatalf("no decisive %s pairings in %s", *game, *dataPath)
	}

	models := modelsIn(results)
	theta := fit(results, models)

	// Bootstrap over matches for the intervals.
	rng := rand.New(rand.NewSource(1))
	samples := make(map[string][]float64, len(models))
	for i := 0; i < *boots; i++ {
		rs := make([]pair, len(results))
		for j := range rs {
			rs[j] = results[rng.Intn(len(results))]
		}
		// A resample can drop a model entirely; skip those draws rather than
		// scoring an absent model as weak, which would bias its interval down.
		bt := fit(rs, models)
		for _, m := range models {
			if v, ok := bt[m]; ok {
				samples[m] = append(samples[m], v)
			}
		}
	}

	type row struct {
		model                 string
		elo, low, high, theta float64
		w, l, n               int
	}
	rows := make([]row, 0, len(models))
	for _, m := range models {
		lo, hi := pct(samples[m], 0.025), pct(samples[m], 0.975)
		w, l := 0, 0
		for _, r := range results {
			if r.a == m {
				w++
			} else if r.b == m {
				l++
			}
		}
		rows = append(rows, row{
			model: m, theta: theta[m], w: w, l: l, n: w + l,
			elo: elo(theta[m]), low: elo(lo), high: elo(hi),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].elo > rows[j].elo })

	// Separability: the fraction of pairs whose intervals are disjoint.
	sep, pairs := 0.0, 0
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			pairs++
			if rows[i].low > rows[j].high || rows[j].low > rows[i].high {
				sep++
			}
		}
	}
	if pairs > 0 {
		sep /= float64(pairs)
	}

	boardDay := time.Now().UTC().Format("2006-01-02")
	if *day != "" {
		boardDay = *day
	}
	fmt.Printf("%s  %s  %d decisive pairings, %d skipped, %d dropped for fallbacks, separability %.2f\n",
		boardDay, *game, len(results), skipped, contaminated, sep)
	fmt.Printf("%-30s %6s %8s %8s %5s %4s %4s\n", "model", "elo", "low", "high", "n", "W", "L")
	for _, r := range rows {
		fmt.Printf("%-30s %6.0f %8.0f %8.0f %5d %4d %4d\n", r.model, r.elo, r.low, r.high, r.n, r.w, r.l)
	}
	if sep == 0 {
		fmt.Println("\nNOTE: separability 0.00 — no pair of models is distinguishable at 95%.\n" +
			"The board will render an ordering; the evidence does not support one.")
	}
	if *dry {
		return
	}
	if *dsn == "" {
		log.Fatal("-dsn or DATABASE_URL is required to write")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	for i, r := range rows {
		// Its OWN board namespace, never `developer` or `harness`. Those two are computed
		// continuously by the platform's rating job from live traffic; a curated research run
		// writing into them would silently overwrite the production board with a frozen
		// snapshot, and the overwrite would look like a rating change rather than a stomp.
		//
		// provisional is always true here: a run this small is by definition not settled, and
		// the flag is what lets a surface say so without reading separability.
		if _, err := tx.Exec(ctx, `
			INSERT INTO model_board_history
			  (board, day, model, elo, elo_low, elo_high, theta, rank, rank_stability,
			   comparisons, wins, losses, draws, harnesses, separability, provisional,
			   window_days, computed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,0,1,$13,true,1,now())
			ON CONFLICT (board, day, model) DO UPDATE SET
			  elo=EXCLUDED.elo, elo_low=EXCLUDED.elo_low, elo_high=EXCLUDED.elo_high,
			  theta=EXCLUDED.theta, rank=EXCLUDED.rank, rank_stability=EXCLUDED.rank_stability,
			  comparisons=EXCLUDED.comparisons, wins=EXCLUDED.wins, losses=EXCLUDED.losses,
			  separability=EXCLUDED.separability, provisional=EXCLUDED.provisional,
			  computed_at=now()`,
			*board, boardDay, r.model, r.elo, r.low, r.high, r.theta, i+1, sep, r.n, r.w, r.l, sep); err != nil {
			log.Fatalf("upsert %s: %v", r.model, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("\nseeded %d models onto board %q for %s\n", len(rows), *board, boardDay)
}

func modelsIn(rs []pair) []string {
	set := map[string]bool{}
	for _, r := range rs {
		set[r.a], set[r.b] = true, true
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// fit runs the Bradley–Terry MM iteration and returns log-strengths, mean-centred.
//
// Centring matters: BT strengths are only identified up to a common scale, so without it two
// runs of the same data can report different Elo numbers and look like drift.
func fit(rs []pair, models []string) map[string]float64 {
	present := map[string]bool{}
	wins := map[string]float64{}
	games := map[[2]string]float64{}
	for _, r := range rs {
		present[r.a], present[r.b] = true, true
		wins[r.a]++
		k := key(r.a, r.b)
		games[k]++
	}
	// A weak prior: every present pair is credited with `prior` virtual games split evenly.
	//
	// Without it an undefeated model has no finite maximum-likelihood strength, and the
	// bootstrap hits that case constantly on a small run — one resample where a model never
	// loses sends its rating to the ceiling and the published interval reads elo_high ~9900,
	// which looks like a broken board rather than an uncertain one. The prior is deliberately
	// weak (half a game per pairing): it bounds the estimate without moving the ordering that
	// the real games imply.
	const prior = 0.5
	ms := make([]string, 0, len(present))
	for m := range present {
		ms = append(ms, m)
	}
	for i := range ms {
		for j := i + 1; j < len(ms); j++ {
			games[key(ms[i], ms[j])] += prior
			wins[ms[i]] += prior / 2
			wins[ms[j]] += prior / 2
		}
	}

	p := map[string]float64{}
	for _, m := range models {
		if present[m] {
			p[m] = 1
		}
	}
	for iter := 0; iter < 200; iter++ {
		next := make(map[string]float64, len(p))
		for m := range p {
			den := 0.0
			for o := range p {
				if o == m {
					continue
				}
				n := games[key(m, o)]
				if n == 0 {
					continue
				}
				den += n / (p[m] + p[o])
			}
			if den == 0 {
				next[m] = p[m]
				continue
			}
			next[m] = wins[m] / den
		}
		norm := 0.0
		for _, v := range next {
			norm += math.Log(v)
		}
		norm = math.Exp(norm / float64(len(next)))
		for m := range next {
			p[m] = next[m] / norm
		}
	}
	out := make(map[string]float64, len(p))
	mean := 0.0
	for m, v := range p {
		out[m] = math.Log(v)
		mean += out[m]
	}
	mean /= float64(len(out))
	for m := range out {
		out[m] -= mean
	}
	return out
}

func key(a, b string) [2]string {
	if a < b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

// elo maps a Bradley–Terry log-strength onto the conventional 1500-centred scale.
func elo(theta float64) float64 { return 1500 + 400/math.Ln10*theta }

func pct(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(q * float64(len(s)-1))
	return s[i]
}
