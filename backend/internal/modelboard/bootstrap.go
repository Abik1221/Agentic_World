package modelboard

import (
	"runtime"
	"sort"
	"sync"
)

// bootstrapResult holds the resampled strengths and ranks for every model.
type bootstrapResult struct {
	// thetas[model] is that model's strength across replicates, sorted ascending so the
	// percentile bounds are a lookup rather than a re-sort per model.
	thetas map[string][]float64
	// ranks[model][rank] counts replicates in which the model landed on that rank.
	ranks map[string]map[int]int
	reps  int
}

// splitmix64 is the PRNG.
//
// Deliberately not math/rand: a published interval must be recomputable years later, and the
// standard library's global source has changed its algorithm between Go releases. splitmix64 is
// four lines, has no state beyond a uint64, and will produce the same stream on any machine and
// any compiler — which is what "reproducible leaderboard" has to mean.
type splitmix64 struct{ s uint64 }

func (r *splitmix64) next() uint64 {
	r.s += 0x9E3779B97F4A7C15
	z := r.s
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// intn returns a value in [0,n) without modulo bias.
//
// Lemire's rejection method. The bias from plain modulo is tiny at these magnitudes, but a
// bootstrap is precisely a claim about a sampling distribution, and knowingly sampling from a
// slightly wrong one to save three lines is not a trade worth making in code that produces
// confidence intervals.
func (r *splitmix64) intn(n int) int {
	if n <= 0 {
		return 0
	}
	un := uint64(n)
	limit := ^uint64(0) - (^uint64(0) % un) - 1
	for {
		v := r.next()
		if v <= limit {
			return int(v % un)
		}
	}
}

// bootstrap resamples MATCHES with replacement and refits.
//
// Clustered on the match, which is the unit of independence here. An N-player Monopoly match
// contributes several pairwise comparisons that share the same board, the same dice and the same
// opponents; resampling comparisons individually would count those as independent draws and
// narrow every interval by roughly sqrt(comparisons per match). A board that overstates its own
// certainty is worse than one that admits noise, because the reader cannot see the error.
//
// Strata and model slots are FIXED across replicates rather than re-indexed per resample: a
// replicate that happens to omit a model must produce a slot for it that simply receives no
// evidence, not a differently-shaped parameter vector whose indices mean something else.
func bootstrap(cmp []Comparison, byMatch map[string][]int, models, strata map[string]int, cfg Config) *bootstrapResult {
	res := &bootstrapResult{
		thetas: map[string][]float64{},
		ranks:  map[string]map[int]int{},
	}
	if cfg.BootstrapReplicates <= 0 || len(byMatch) == 0 {
		return res
	}
	matchIDs := make([]string, 0, len(byMatch))
	for id := range byMatch {
		matchIDs = append(matchIDs, id)
	}
	// Sorted so the resampling draw sequence depends only on the seed and the data, never on map
	// iteration order.
	sort.Strings(matchIDs)

	// The bootstrap does not need intervals of its own, and running one inside each replicate
	// would be quadratic for no gain.
	inner := cfg
	inner.BootstrapReplicates = 0

	// Replicates run in PARALLEL, each with a seed derived from (cfg.Seed, rep).
	//
	// Deriving per-replicate seeds rather than sharing one stream is what keeps the result
	// reproducible: with a shared RNG the draws a replicate receives would depend on the order
	// goroutines happened to reach it, so the same data and seed would give different intervals on
	// every run and on every machine. Here replicate 7 always sees replicate 7's resample.
	//
	// This is also why the fit is usable at the arena convention of 1000 replicates at all — each
	// replicate is a full optimization, and the serial version took minutes on data a real season
	// would dwarf.
	type replicate struct {
		thetas []float64 // indexed by model slot
		order  []string  // models, best first
	}
	out := make([]replicate, cfg.BootstrapReplicates)
	workers := runtime.GOMAXPROCS(0)
	if workers > cfg.BootstrapReplicates {
		workers = cfg.BootstrapReplicates
	}
	var wg sync.WaitGroup
	next := make(chan int, cfg.BootstrapReplicates)
	for rep := 0; rep < cfg.BootstrapReplicates; rep++ {
		next <- rep
	}
	close(next)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resampled := make([]Comparison, 0, len(cmp))
			for rep := range next {
				// splitmix64 is seeded by mixing the run seed with the replicate index. Any two
				// replicates get independent streams, and both are fixed by the config.
				rng := &splitmix64{s: cfg.Seed ^ (uint64(rep+1) * 0x9E3779B97F4A7C15)}
				resampled = resampled[:0]
				for i := 0; i < len(matchIDs); i++ {
					id := matchIDs[rng.intn(len(matchIDs))]
					for _, k := range byMatch[id] {
						resampled = append(resampled, cmp[k])
					}
				}
				p := &problem{cmp: resampled, modelIdx: models, stratumIdx: strata, cfg: inner}
				x, _, _ := solve(p)

				th := make([]float64, len(models))
				type mr struct {
					m string
					t float64
				}
				row := make([]mr, 0, len(models))
				for m, idx := range models {
					th[idx] = x[idx]
					row = append(row, mr{m, x[idx]})
				}
				// Rank within the replicate on the point estimate — a replicate has no interval of
				// its own, and this is what "would this model have held its place" means.
				sort.Slice(row, func(i, j int) bool {
					if row[i].t != row[j].t {
						return row[i].t > row[j].t
					}
					return row[i].m < row[j].m
				})
				order := make([]string, len(row))
				for i, r := range row {
					order[i] = r.m
				}
				out[rep] = replicate{thetas: th, order: order}
			}
		}()
	}
	wg.Wait()

	// Accumulated in replicate order, serially, so floating-point summation and slice ordering do
	// not depend on which goroutine finished first.
	for rep := range out {
		if out[rep].thetas == nil {
			continue
		}
		for m, idx := range models {
			res.thetas[m] = append(res.thetas[m], out[rep].thetas[idx])
		}
		for i, m := range out[rep].order {
			if res.ranks[m] == nil {
				res.ranks[m] = map[int]int{}
			}
			res.ranks[m][i+1]++
		}
		res.reps++
	}
	for m := range res.thetas {
		sort.Float64s(res.thetas[m])
	}
	return res
}

// interval returns the 95% percentile bounds for a model's strength.
//
// Percentile method rather than a normal approximation: Bradley-Terry strengths are skewed for
// thinly-observed models — a 3-0 record's distribution has a long right tail — and a symmetric
// interval around the point estimate would put the lower bound in a region the resampling never
// visited.
func (b *bootstrapResult) interval(model string) (lo, hi float64, ok bool) {
	v := b.thetas[model]
	if len(v) < 20 {
		// Below ~20 replicates the 2.5th percentile is the extreme observation itself, which is
		// an estimate of nothing. Better to report no interval than a meaningless one.
		return 0, 0, false
	}
	return quantile(v, 0.025), quantile(v, 0.975), true
}

// quantile is the linear-interpolated empirical quantile of a SORTED slice.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[lo+1]*frac
}

// stability is the share of replicates in which the model held the given rank.
//
// The "stability certificate" of arXiv:2605.15761, obtained free from the bootstrap we already
// ran. It answers the question a rank alone cannot: would this ordering survive a different
// sample of the same matches? A rank held in 60% of replicates and one held in 99% are
// different claims, and printing both as an integer position hides the difference.
func (b *bootstrapResult) stability(model string, rank int) float64 {
	if b.reps == 0 || b.ranks[model] == nil {
		return 0
	}
	return float64(b.ranks[model][rank]) / float64(b.reps)
}
