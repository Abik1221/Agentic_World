package pindex

// Engine combines the registered dimensions into the composite P-Index. The default
// set is the four beta dimensions; additional dimensions can be registered without
// touching existing behaviour.
type Engine struct {
	dims []Dimension
}

// NewEngine builds the engine with the beta dimensions (Arena, Consistency,
// Difficulty, Activity) plus the engine-measured Intelligence dimension. A config
// that gives Intelligence weight 0 (e.g. the original v1) leaves scores unchanged,
// so the dimension is inert until a config version activates it.
func NewEngine() *Engine {
	return &Engine{dims: []Dimension{
		Arena{}, Consistency{}, Difficulty{}, Activity{}, Intelligence{}, Skill{},
	}}
}

// weightOf maps a dimension key to its configured weight.
func weightOf(key string, cfg Config) float64 {
	switch key {
	case keyArena:
		return cfg.Weights.Arena
	case keyConsistency:
		return cfg.Weights.Consistency
	case keyDifficulty:
		return cfg.Weights.Difficulty
	case keyActivity:
		return cfg.Weights.Activity
	case keyIntelligence:
		return cfg.Weights.Intelligence
	case keySkill:
		return cfg.Weights.Skill
	default:
		return 0
	}
}

// Compute is the pure, deterministic P-Index calculation: score every dimension,
// weight each, and sum. The Result carries the full breakdown for the transparency
// surface and the audit history.
func (e *Engine) Compute(in DeveloperInputs, cfg Config) Result {
	res := Result{ConfigVersion: cfg.Version}
	for _, d := range e.dims {
		sub := d.Score(in, cfg)
		w := weightOf(sub.Key, cfg)
		weighted := sub.Score * w
		res.PIndex += weighted
		res.Contributions = append(res.Contributions, Contribution{
			Key: sub.Key, Score: round2(sub.Score), Weight: w,
			Weighted: round2(weighted), Reason: sub.Reason,
		})
	}
	res.PIndex = round2(res.PIndex)
	return res
}

// ── shared math helpers (pure) ──────────────────────────────────────────────────

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// normalize maps a rating on [low, high] to [0, scale], clamped.
func normalize(rating, low, high, scale float64) float64 {
	if high <= low {
		return 0
	}
	return clamp((rating-low)/(high-low), 0, 1) * scale
}

// round2 rounds to 2 decimals without importing math (deterministic, avoids float
// display noise in the audit trail).
func round2(x float64) float64 {
	scaled := x * 100
	if scaled >= 0 {
		scaled += 0.5
	} else {
		scaled -= 0.5
	}
	return float64(int64(scaled)) / 100
}

// Dimensions returns the registered dimensions.
//
// Exported so the published methodology is assembled from the SAME list the engine scores
// with. Deriving the page from a second, hand-maintained list is how a published method
// starts describing dimensions that no longer exist, or omitting ones that do.
func (e *Engine) Dimensions() []Dimension { return e.dims }
