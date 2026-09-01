// Package modeleq tests whether an API is still serving the model it advertises.
//
// # The hole this closes
//
// Completion binding (internal/movebind) proves that a submitted move came from the call the
// gateway made. It says nothing about what answered that call. A provider that silently routes
// to a quantised build, a distilled stand-in, or a cheaper sibling produces a completion that
// binds exactly as well as the real thing, and every downstream number — cost per decision,
// exploitability, rank — is then about a model nobody selected.
//
// On a benchmark this is a correctness problem. On a staked arena it is worse: an agent's cost
// efficiency is scored against the model it declared, so a provider substitution silently
// changes the terms of a bet.
//
// # Why a distributional test and not a fingerprint
//
// The obvious approach is a fingerprint: send a known prompt, hash the reply, compare. It fails
// on anything that samples, which is every endpoint we care about — the same model answers
// differently twice, so a hash mismatch means nothing. Forcing temperature to zero does not
// rescue it either: providers differ in tie-breaking, batching and kernel choice, so even greedy
// decoding drifts across deployments of the SAME weights.
//
// So this compares DISTRIBUTIONS. Fix a small set of prompts whose answer space is discrete and
// narrow. Sample each prompt many times from a reference deployment to obtain a baseline, sample
// the same prompts from the endpoint under test, and ask whether the two sets of counts could
// plausibly have come from one distribution.
//
// The statistic is a two-sample chi-square over the pooled answer alphabet, summed across
// prompts. It needs no distributional assumption about the model, only that the answer space is
// enumerable — which is exactly what a forced tool call gives us, and is why the probe reuses
// the move tool rather than free text.
//
// # What a rejection means, and what it does not
//
// A rejection says the endpoint's output distribution differs from the baseline by more than
// sampling noise. It does NOT identify what changed. A version bump, a system-prompt edit, a
// quantisation, a routing change and a genuine substitution all present identically. The honest
// consequence is therefore a FLAG FOR REVIEW and a note on the certificate, never an automatic
// accusation.
//
// A pass is weaker still: it says no difference was detected at this power. An adversary who
// matches the answer distribution on these prompts passes. This raises the cost of silent
// substitution; it does not make it impossible.
//
// # Why the baseline must be dated
//
// A baseline is a claim about a deployment at a moment. Providers legitimately update models,
// so a baseline taken a year ago will reject a healthy endpoint and the alert will be ignored
// within a week. Baselines therefore carry the capture time and the exact model string, and a
// stale one is reported as stale rather than silently compared.
package modeleq

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Prompt is one probe: a question whose answers fall into a small discrete alphabet.
//
// Prompts are part of the baseline's identity — comparing counts gathered under different
// prompts is not a test of anything — so the ID travels with the observations.
type Prompt struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Observation is the answer histogram for one prompt at one endpoint.
type Observation struct {
	PromptID string         `json:"prompt_id"`
	Counts   map[string]int `json:"counts"`
}

// Total is how many samples the histogram represents.
func (o Observation) Total() int {
	n := 0
	for _, c := range o.Counts {
		n += c
	}
	return n
}

// Baseline is a dated distributional snapshot of one model at one provider.
type Baseline struct {
	Provider   string        `json:"provider"`
	Model      string        `json:"model"`
	CapturedAt time.Time     `json:"captured_at"`
	Prompts    []Prompt      `json:"prompts"`
	Obs        []Observation `json:"observations"`
}

// Verdict is the outcome of comparing an endpoint against a baseline.
type Verdict struct {
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	ChiSq    float64 `json:"chi_square"`
	DF       int     `json:"degrees_of_freedom"`
	// PValue is an upper-tail probability. Small means the two samples are unlikely to share
	// a distribution.
	PValue float64 `json:"p_value"`
	// Differs is the decision at the requested level. It means "flag for review", not
	// "the provider substituted the model" — see the package comment.
	Differs bool `json:"differs"`
	// Stale reports that the baseline is older than the freshness window. A stale comparison
	// is still returned, because a reviewer wants the number AND the caveat, not neither.
	Stale bool `json:"baseline_stale"`
	// Underpowered marks a comparison with too few samples to conclude anything. Reported
	// rather than silently passing: "no difference detected" from 20 samples is not evidence
	// of sameness, and a green tick there would be actively misleading.
	Underpowered bool   `json:"underpowered"`
	Note         string `json:"note,omitempty"`
}

// MinSamplesPerPrompt is the floor below which a comparison is marked underpowered.
//
// The chi-square approximation degrades when expected cell counts fall below ~5. With a handful
// of distinct answers per prompt, 50 samples keeps the common cells above that; it does not
// rescue a rare answer, which is why sparse cells are pooled (see Compare).
const MinSamplesPerPrompt = 50

// DefaultFreshness is how long a baseline is treated as current.
//
// Three weeks is a judgement, not a measurement: long enough to survive a normal release
// cadence, short enough that a silently updated deployment is caught in the same quarter it
// changed.
const DefaultFreshness = 21 * 24 * time.Hour

// Compare tests observations from an endpoint against a baseline.
//
// alpha is the significance level for the flag. Cells are POOLED across the union of answers
// seen in either sample, and any answer whose combined count is below 5 is folded into an
// "other" bucket — the standard remedy for a sparse contingency table, and the reason a single
// exotic answer from one side cannot on its own trip the alarm.
func Compare(b Baseline, got []Observation, alpha float64, now time.Time) (Verdict, error) {
	if alpha <= 0 || alpha >= 1 {
		return Verdict{}, fmt.Errorf("modeleq: alpha must be in (0,1), got %v", alpha)
	}
	byID := make(map[string]Observation, len(got))
	for _, o := range got {
		byID[o.PromptID] = o
	}

	v := Verdict{Provider: b.Provider, Model: b.Model}
	if now.Sub(b.CapturedAt) > DefaultFreshness {
		v.Stale = true
	}

	total := 0.0
	df := 0
	compared := 0
	for _, base := range b.Obs {
		cur, ok := byID[base.PromptID]
		if !ok {
			// A missing prompt is a gap in the test, not a difference in the model.
			continue
		}
		if cur.Total() < MinSamplesPerPrompt || base.Total() < MinSamplesPerPrompt {
			v.Underpowered = true
		}
		chi, d := chiSquare(base.Counts, cur.Counts)
		total += chi
		df += d
		compared++
	}
	if compared == 0 {
		return Verdict{}, fmt.Errorf("modeleq: no prompts in common between baseline and sample")
	}
	if df < 1 {
		// Every prompt collapsed to a single pooled cell: both sides always answered the same
		// way. There is no signal here in either direction.
		v.Note = "no variation in either sample; the probe cannot discriminate"
		return v, nil
	}

	v.ChiSq, v.DF = total, df
	v.PValue = UpperTail(total, df)
	v.Differs = v.PValue < alpha
	switch {
	case v.Differs && v.Underpowered:
		v.Note = "difference detected, but at least one prompt is below the sample floor"
	case v.Differs:
		v.Note = "output distribution differs from baseline — flag for review, not proof of substitution"
	case v.Underpowered:
		v.Note = "no difference detected, but the sample is too small to call this a pass"
	}
	return v, nil
}

// chiSquare returns the two-sample statistic and its degrees of freedom for one prompt.
func chiSquare(a, b map[string]int) (float64, int) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	// Pool sparse answers so the approximation holds.
	var kept []string
	otherA, otherB := 0, 0
	for k := range keys {
		if a[k]+b[k] < 5 {
			otherA += a[k]
			otherB += b[k]
			continue
		}
		kept = append(kept, k)
	}
	sort.Strings(kept)
	ca := make([]float64, 0, len(kept)+1)
	cb := make([]float64, 0, len(kept)+1)
	for _, k := range kept {
		ca = append(ca, float64(a[k]))
		cb = append(cb, float64(b[k]))
	}
	if otherA+otherB > 0 {
		ca = append(ca, float64(otherA))
		cb = append(cb, float64(otherB))
	}
	if len(ca) < 2 {
		return 0, 0
	}
	na, nb := 0.0, 0.0
	for i := range ca {
		na += ca[i]
		nb += cb[i]
	}
	if na == 0 || nb == 0 {
		return 0, 0
	}
	n := na + nb
	chi := 0.0
	for i := range ca {
		rowTotal := ca[i] + cb[i]
		ea := rowTotal * na / n
		eb := rowTotal * nb / n
		if ea > 0 {
			chi += (ca[i] - ea) * (ca[i] - ea) / ea
		}
		if eb > 0 {
			chi += (cb[i] - eb) * (cb[i] - eb) / eb
		}
	}
	return chi, len(ca) - 1
}

// UpperTail is P(X > x) for a chi-square with df degrees of freedom.
//
// Exported because it decides every verdict this package issues: if the tail is miscalibrated
// then so is the flag, silently and in one direction. Keeping it reachable lets a test check it
// against published critical values instead of trusting the implementation.
//
// Implemented via the regularised upper incomplete gamma so the package carries no dependency
// for one distribution function.
func UpperTail(x float64, df int) float64 {
	if x <= 0 || df < 1 {
		return 1
	}
	return gammaQ(float64(df)/2, x/2)
}

// gammaQ is the regularised upper incomplete gamma Q(s,x), by series below the transition and
// continued fraction above it — the standard split, because each converges where the other
// does not.
func gammaQ(s, x float64) float64 {
	if x < s+1 {
		return 1 - gammaP(s, x)
	}
	const eps = 1e-14
	b := x + 1 - s
	c := 1 / 1e-300
	d := 1 / b
	h := d
	for i := 1; i < 300; i++ {
		an := -float64(i) * (float64(i) - s)
		b += 2
		d = an*d + b
		if math.Abs(d) < 1e-300 {
			d = 1e-300
		}
		c = b + an/c
		if math.Abs(c) < 1e-300 {
			c = 1e-300
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	lg, _ := math.Lgamma(s)
	return math.Exp(-x+s*math.Log(x)-lg) * h
}

func gammaP(s, x float64) float64 {
	const eps = 1e-14
	ap := s
	sum := 1 / s
	del := sum
	for i := 0; i < 300; i++ {
		ap++
		del *= x / ap
		sum += del
		if math.Abs(del) < math.Abs(sum)*eps {
			break
		}
	}
	lg, _ := math.Lgamma(s)
	return sum * math.Exp(-x+s*math.Log(x)-lg)
}
