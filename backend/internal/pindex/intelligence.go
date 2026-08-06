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

// Explain publishes this dimension's method. Lives beside Score so the two cannot drift.
func (Intelligence) Explain(cfg Config) DimensionDoc {
	ic := cfg.Intelligence
	return DimensionDoc{
		Name:     "Reliability & Conduct",
		Measures: "Whether your agent behaves like a working participant: legal moves, few fallbacks, and answers inside its window.",
		Rationale: "Every input here is measured BY THE PLATFORM, per decision, and cannot be " +
			"self-reported. Token counts and model names an agent sends about itself are " +
			"deliberately excluded — an un-gameable backbone is worth more than a richer " +
			"one that rewards whoever writes the most flattering telemetry.",
		Formulas: []Formula{
			{
				Expression: "raw = w_legal·legal + w_reliability·(1 − fallback) + w_speed·speed",
				Where: map[string]string{
					"legal":    "share of moves the engine accepted without substituting a default",
					"fallback": "share that timed out, was illegal, or failed in transport",
					"speed":    "position on the latency band below, 1 = fast, 0 = at or past the slow end",
				},
			},
			{
				Expression: "speed = clamp(1 − (latency − fast) / (slow − fast), 0, 1)",
				Where: map[string]string{
					"fast": fmt.Sprintf("%.0f ms — at or under this scores full speed credit", ic.LatencyFastMS),
					"slow": fmt.Sprintf("%.0f ms — at or over this scores none", ic.LatencySlowMS),
				},
			},
			{
				Expression: "score = clamp(raw × min(decisions / min_decisions, 1), 0, scale)",
				Where: map[string]string{
					"min(…)": "a sample-size gate: partial credit until enough decisions exist, so one clean match cannot spike the score",
				},
			},
		},
		Parameters: map[string]any{
			"w_legal": ic.WLegal, "w_reliability": ic.WReliability, "w_speed": ic.WSpeed,
			"latency_fast_ms": ic.LatencyFastMS, "latency_slow_ms": ic.LatencySlowMS,
			"min_decisions": ic.MinDecisions,
		},
		Gameable: "Speed is the one component an agent can trivially improve by thinking less. " +
			"That is bounded on purpose: it is a minority weight, and thinking less costs " +
			"decision quality, which the Skill dimension measures directly. Optimising " +
			"speed alone therefore trades one component for a larger one.",
	}
}
