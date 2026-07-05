// Package antifraud hardens the economy: it holds suspect payouts (escrow is
// preserved, never lost), detects collusion rings / same-owner dumping /
// human-like play, runs the disputes workflow, and writes an immutable audit log.
// The scoring functions here are pure and conservative — the platform biases
// toward hold + human review over irreversible automated bans.
package antifraud

// Pair is the head-to-head record + coin flow between two agents.
type Pair struct {
	A, B        string
	Games       int
	AWins       int
	BWins       int
	NetFlowAToB int64 // net coins that moved from A to B across their matches
}

const (
	minPairGames  = 5
	collusionWinR = 0.85 // one side winning ≥85% of a long series is suspicious
)

// CollusionScore returns 0..1 for how lopsided a pairing is (the magnitude a
// reviewer sees). 0 below the minimum sample size.
func CollusionScore(p Pair) float64 {
	total := p.AWins + p.BWins
	if p.Games < minPairGames || total == 0 {
		return 0
	}
	hi := p.AWins
	if p.BWins > hi {
		hi = p.BWins
	}
	return (float64(hi)/float64(total) - 0.5) * 2 // 0 at 50/50 → 1 at 100/0
}

// IsColluding flags a pairing whose results are lopsided over a long series and
// whose coins concentrate on the consistent winner.
func IsColluding(p Pair) bool {
	total := p.AWins + p.BWins
	if p.Games < minPairGames || total == 0 {
		return false
	}
	if p.AWins >= p.BWins {
		// A wins most → coins should flow B→A (NetFlowAToB negative).
		return float64(p.AWins)/float64(total) >= collusionWinR && p.NetFlowAToB <= 0
	}
	return float64(p.BWins)/float64(total) >= collusionWinR && p.NetFlowAToB >= 0
}

// TimingStat is an agent's response-time distribution.
type TimingStat struct {
	Count  int
	MeanMs float64
	StdMs  float64
}

const (
	minTimingSamples = 10
	humanMeanMs      = 1500 // humans deliberate; bots fire fast
	humanCV          = 0.40 // humans are variable; bots are metronomic
)

// HumanLikelihood returns 0..1 — high for slow, variable (human-like) timing.
func HumanLikelihood(t TimingStat) float64 {
	if t.Count < minTimingSamples || t.MeanMs <= 0 {
		return 0
	}
	slow := clamp((t.MeanMs-500)/3000, 0, 1) // 500ms → 3500ms maps 0 → 1
	cv := clamp((t.StdMs/t.MeanMs)/0.8, 0, 1)
	return (slow + cv) / 2
}

// LooksHuman is the conservative flag (slow AND variable).
func LooksHuman(t TimingStat) bool {
	if t.Count < minTimingSamples || t.MeanMs <= 0 {
		return false
	}
	return t.MeanMs >= humanMeanMs && (t.StdMs/t.MeanMs) >= humanCV
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
