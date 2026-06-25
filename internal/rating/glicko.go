// Package rating computes and persists skill ratings per agent per season, applied
// at match finalize (idempotently, keyed by match id), and serves the public
// leaderboard. It uses Glicko-2 (Glickman) rather than plain Elo: each agent
// carries a rating, a rating deviation (RD, how uncertain that rating is) and a
// volatility. This matters for a bot arena specifically — agents play in very high
// volume, so a new agent's rating should move fast and wide (high RD) and settle as
// evidence accumulates, instead of fixed-K Elo treating a brand-new 1500 like a
// veteran 1500. The math here is pure and deterministic; the read-modify-write
// persistence lives in the store, which calls the Compute closure this package
// supplies — so the formula never leaks into SQL.
//
// Reference: http://www.glicko.net/glicko/glicko2.pdf
package rating

import "math"

// Tie is the winner sentinel for a drawn match (matches gs.Tie).
const Tie = -1

// Glicko-2 constants. The displayed rating is centred on 1500; scale 173.7178
// converts to/from the internal (μ, φ) space. τ constrains how fast volatility
// changes — 0.5 is a conservative, widely-used value for fast-converging pools.
const (
	glickoCenter = 1500.0
	glickoScale  = 173.7178
	defaultRD    = 350.0  // a brand-new agent: maximally uncertain
	defaultVol   = 0.06   // standard starting volatility
	glickoTau    = 0.5    // system constant (volatility change constraint)
	glickoEps    = 1e-6   // convergence tolerance for the volatility solver
)

// PlayerRating is an agent's full Glicko-2 state. Elo is the displayed rating
// (Glicko r, rounded); RD and Vol are the deviation and volatility.
type PlayerRating struct {
	Elo int
	RD  float64
	Vol float64
}

// ScoreForSeat0 maps a winning seat (0, 1, or Tie) to seat 0's score.
func ScoreForSeat0(winnerSeat int) float64 {
	switch {
	case winnerSeat == Tie:
		return 0.5
	case winnerSeat == 0:
		return 1
	default:
		return 0
	}
}

// Glicko2 returns the updated ratings for seat A and seat B after a single game,
// where scoreA is 1 (A won), 0.5 (tie) or 0 (A lost). Each side is updated against
// the other's PRE-match rating, so the call is order-independent and symmetric.
func Glicko2(a, b PlayerRating, scoreA float64) (PlayerRating, PlayerRating) {
	return glickoUpdateOne(a, b, scoreA), glickoUpdateOne(b, a, 1-scoreA)
}

// glickoUpdateOne updates player p after one game with `score` against opp, both
// taken at their pre-match state. Treats the single game as its own rating period.
func glickoUpdateOne(p, opp PlayerRating, score float64) PlayerRating {
	mu := (float64(p.Elo) - glickoCenter) / glickoScale
	phi := rdOrDefault(p.RD) / glickoScale
	sigma := volOrDefault(p.Vol)
	muj := (float64(opp.Elo) - glickoCenter) / glickoScale
	phij := rdOrDefault(opp.RD) / glickoScale

	g := 1 / math.Sqrt(1+3*phij*phij/(math.Pi*math.Pi))
	e := 1 / (1 + math.Exp(-g*(mu-muj)))
	v := 1 / (g * g * e * (1 - e))
	delta := v * g * (score - e)

	sigmaPrime := newVolatility(phi, v, delta, sigma)
	phiStar := math.Sqrt(phi*phi + sigmaPrime*sigmaPrime)
	phiPrime := 1 / math.Sqrt(1/(phiStar*phiStar)+1/v)
	muPrime := mu + phiPrime*phiPrime*g*(score-e)

	return PlayerRating{
		Elo: int(math.Round(muPrime*glickoScale + glickoCenter)),
		RD:  phiPrime * glickoScale,
		Vol: sigmaPrime,
	}
}

// newVolatility solves Glickman's volatility equation via the Illinois variant of
// regula falsi (the algorithm specified in the Glicko-2 paper, step 5).
func newVolatility(phi, v, delta, sigma float64) float64 {
	a := math.Log(sigma * sigma)
	phi2, d2 := phi*phi, delta*delta
	f := func(x float64) float64 {
		ex := math.Exp(x)
		denom := phi2 + v + ex
		return ex*(d2-denom)/(2*denom*denom) - (x-a)/(glickoTau*glickoTau)
	}

	A := a
	var B float64
	if d2 > phi2+v {
		B = math.Log(d2 - phi2 - v)
	} else {
		k := 1.0
		for f(a-k*glickoTau) < 0 {
			k++
		}
		B = a - k*glickoTau
	}

	fA, fB := f(A), f(B)
	for math.Abs(B-A) > glickoEps {
		C := A + (A-B)*fA/(fB-fA)
		fC := f(C)
		if fC*fB <= 0 {
			A, fA = B, fB
		} else {
			fA /= 2
		}
		B, fB = C, fC
	}
	return math.Exp(A / 2)
}

func rdOrDefault(rd float64) float64 {
	if rd <= 0 {
		return defaultRD
	}
	return rd
}

func volOrDefault(vol float64) float64 {
	if vol <= 0 {
		return defaultVol
	}
	return vol
}
