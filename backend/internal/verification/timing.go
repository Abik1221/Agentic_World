// Package verification distinguishes automated agents from humans playing by
// hand. v1 (this package) captures per-action response times and computes a
// timing profile + a human-likelihood score; the match layer (Stage 3) feeds it
// samples and consults eligibility at join. v2 (Stage 9) adds graph/ML signals.
package verification

import "math"

// TimingProfile summarizes an agent's response-time distribution. Bots respond
// fast (≈50–500 ms) and consistently; humans are slower (≈2–8 s) and far more
// variable, clustered in waking hours.
type TimingProfile struct {
	Count           int
	MeanMs          float64
	StdDevMs        float64
	MinMs           int
	MaxMs           int
	HumanLikelihood float64 // 0.0 = clearly a bot, 1.0 = clearly a human
}

// AnalyzeTimingProfile computes the profile from response-time samples (ms).
func AnalyzeTimingProfile(samplesMs []int) TimingProfile {
	p := TimingProfile{Count: len(samplesMs)}
	if p.Count == 0 {
		return p
	}
	p.MinMs, p.MaxMs = samplesMs[0], samplesMs[0]
	var sum float64
	for _, v := range samplesMs {
		sum += float64(v)
		if v < p.MinMs {
			p.MinMs = v
		}
		if v > p.MaxMs {
			p.MaxMs = v
		}
	}
	p.MeanMs = sum / float64(p.Count)

	var variance float64
	for _, v := range samplesMs {
		d := float64(v) - p.MeanMs
		variance += d * d
	}
	p.StdDevMs = math.Sqrt(variance / float64(p.Count))
	p.HumanLikelihood = humanLikelihood(p.MeanMs, p.StdDevMs)
	return p
}

// humanLikelihood blends two signals: a slow mean and high variance both look
// human. Each is normalized to [0,1] and averaged. This is a deliberately simple,
// explainable heuristic for v1; v2 replaces it with a fitted model.
func humanLikelihood(meanMs, stdDevMs float64) float64 {
	meanScore := clamp01((meanMs - 500) / (4000 - 500)) // 500ms→0, 4s→1
	varScore := clamp01(stdDevMs / 2000)                // 0→0, 2s stddev→1
	return clamp01((meanScore + varScore) / 2)
}

func clamp01(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	default:
		return x
	}
}
