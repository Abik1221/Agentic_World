package antifraud

import "math"

// Action-correlation detection looks at collusion in the *process* (how two agents
// bid), not just the *outcome* (who won). Two agents quietly coordinating — one
// reliably dumping prizes to the other — leave a statistical fingerprint: their
// per-round bids become dependent in a way independent strategies are not. We
// measure that with normalized mutual information over coarse-binned bids.
//
// This is plain information theory, not machine learning: no model, no training.
// It is deliberately conservative and ADDITIVE — it only fires for pairs that are
// concentrated enough to be suspicious but BELOW the pairwise ban threshold (the
// band the outcome test alone would miss), and every flag is a hold + human
// review, never an auto-ban.

// MoveSample is one revealed round between the pair: each agent's card and the
// deck size (max card) used to bin the cards into low/mid/high thirds.
type MoveSample struct {
	CardA, CardB, Deck int
}

const (
	actionMinSamples    = 30   // need a few full matches of rounds before MI is meaningful
	actionConcentration = 0.60 // floor: ignore balanced (competitive) pairs
	actionMIThreshold   = 0.60 // normalized MI at/above which bids look coordinated
	actionBins          = 3    // low | mid | high
)

// bin maps a card to 0 (low), 1 (mid) or 2 (high) by thirds of the deck.
func bin(card, deck int) int {
	if deck <= 0 {
		deck = 13
	}
	switch {
	case card*3 <= deck:
		return 0
	case card*3 <= 2*deck:
		return 1
	default:
		return 2
	}
}

// ActionMI returns the normalized mutual information (0..1) between the two seats'
// binned bids across the samples. 0 means independent (or no signal); values near
// 1 mean one agent's bid is highly predictable from the other's — a coordination
// signal. Pure and deterministic.
func ActionMI(samples []MoveSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	n := float64(len(samples))
	var joint [actionBins][actionBins]float64
	var px, py [actionBins]float64
	for _, s := range samples {
		a, b := bin(s.CardA, s.Deck), bin(s.CardB, s.Deck)
		joint[a][b]++
		px[a]++
		py[b]++
	}

	mi, hx, hy := 0.0, 0.0, 0.0
	for i := 0; i < actionBins; i++ {
		if px[i] > 0 {
			p := px[i] / n
			hx -= p * math.Log2(p)
		}
		if py[i] > 0 {
			p := py[i] / n
			hy -= p * math.Log2(p)
		}
		for j := 0; j < actionBins; j++ {
			if joint[i][j] == 0 {
				continue
			}
			pxy := joint[i][j] / n
			mi += pxy * math.Log2(pxy/((px[i]/n)*(py[j]/n)))
		}
	}
	// Normalize by the smaller marginal entropy. If either agent always bid the
	// same band (entropy 0), there is no signal to read — return 0 (conservative).
	denom := math.Min(hx, hy)
	if denom <= 0 {
		return 0
	}
	nmi := mi / denom
	if nmi < 0 {
		return 0 // floating-point guard
	}
	if nmi > 1 {
		return 1
	}
	return nmi
}

// actionSuspect gates which pairs are worth the move-correlation check: enough
// rounds to be meaningful, and a result concentrated enough to suspect but below
// the pairwise ban threshold (collusionWinR) — i.e. exactly the band the outcome
// test misses. Pairs at/above the ban are already caught by IsColluding.
func actionSuspect(p Pair, nSamples int) bool {
	total := p.AWins + p.BWins
	if nSamples < actionMinSamples || total == 0 {
		return false
	}
	winR := float64(max(p.AWins, p.BWins)) / float64(total)
	return winR >= actionConcentration && winR < collusionWinR
}
