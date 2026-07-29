package pindex

import "fmt"

const keyIntelligence = "intelligence"

// Intelligence rewards the engine-MEASURED hallmarks of a capable agent, not the
// outcome: it plays legal moves (isn't force-defaulted by the engine), rarely
// times out / falls back, and decides quickly. All three are measured by the
// platform per move — an agent cannot fake them — so they are the trustworthy
// backbone of reputation. Self-reported signals (e.g. token counts) are
// deliberately NOT scored here to keep the dimension un-gameable.
//
// A sample-size gate gives partial credit until the agent has enough benchmarked
// decisions, so one fast, clean match can't spike the score.
type Intelligence struct{}

func (Intelligence) Key() string { return keyIntelligence }

func (Intelligence) Score(in DeveloperInputs, cfg Config) SubScore {
	ic := cfg.Intelligence
	if in.BenchDecisions <= 0 {
		return SubScore{Key: keyIntelligence, Score: 0, Reason: "no benchmarked decisions yet"}
	}

	legal := clamp(in.LegalRate, 0, 1)            // higher is better
	reliability := clamp(1-in.FallbackRate, 0, 1) // fewer timeouts/illegal/transport = better
	speed := 1.0                                  // default full credit if no band configured
	if ic.LatencySlowMS > ic.LatencyFastMS {
		speed = clamp(1-(in.AvgLatencyMS-ic.LatencyFastMS)/(ic.LatencySlowMS-ic.LatencyFastMS), 0, 1)
	}

	raw := ic.WLegal*legal + ic.WReliability*reliability + ic.WSpeed*speed

	// Sample-size gate: linear partial credit up to MinDecisions.
	gate := 1.0
	if ic.MinDecisions > 0 {
		gate = clamp(float64(in.BenchDecisions)/float64(ic.MinDecisions), 0, 1)
	}

	score := clamp(raw*gate*cfg.Scale, 0, cfg.Scale)
	return SubScore{
		Key:   keyIntelligence,
		Score: score,
		Reason: fmt.Sprintf("legal %.0f%%, reliability %.0f%%, %.0fms avg over %d decisions",
			legal*100, reliability*100, in.AvgLatencyMS, in.BenchDecisions),
	}
}
