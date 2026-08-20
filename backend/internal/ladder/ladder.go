// Package ladder runs the certified exploitability ladder: the pinned spec, the run state
// machine, and the digests that make a published certificate reproducible by someone else.
//
// # What a certification run is
//
// An agent plays a fixed number of matches against a reference opponent (PHASE A), we fit its
// behavioural strategy and compute an exact best response, then that best response plays it
// live (PHASE B) until the bound is conclusive or the match budget runs out. The result is a
// statement of the form "this agent is at least this far from optimal, with 95% confidence" —
// absolute, opponent-pool independent, and unfarmable. See internal/exploit for the
// mathematics and internal/gops for the solver.
//
// This package is the part that has to survive being AUDITED rather than merely being right:
// what exactly was run, against which spec, producing which prober, and can a stranger check
// it a year from now.
//
// # Three design decisions, all driven by cost
//
// STORE THE PROBER'S DIGEST, NOT THE PROBER. The prober is a deterministic function of the
// spec and the Phase A counts, and solving takes milliseconds. Persisting a digest instead of
// a strategy means (a) a third party can recompute it and check, (b) any change to the solver
// invalidates the certificate loudly instead of silently altering what was measured, and
// (c) the row stays small. Reproducibility stops being a feature and becomes a consequence.
//
// AGGREGATE PHASE A, DO NOT KEEP RAW OBSERVATIONS. Sample splitting is what makes the bound
// valid, and it is tempting to conclude that per-decision rows must be kept so the split can
// be audited. They do not, because the split here is BY PHASE and declared before any match
// is played, not drawn at analysis time. Counts per (node, card) are sufficient, and storage
// becomes O(nodes) — bounded by the spec — instead of O(decisions), which grows forever.
//
// BATCH PHASE B TO THE NEXT CHECKPOINT. The bound may only be read at the alpha-spending
// checkpoints, so asking for one match at a time buys nothing but scheduling round-trips. The
// runner requests exactly the number that will produce the next decision.
//
// # Immutability
//
// A Spec is frozen once any run references it. The audit found `PutConfig` upserting
// already-published P-Index parameters in place, so "v3" stopped meaning what it meant when
// developers were scored under it. A certificate that names a spec version has to be able to
// rely on that version meaning one thing forever, so specs are append-only and the hash is
// stored beside every certificate.
package ladder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
	"github.com/agent-arena/arena/internal/timecontrol"
)

// Spec pins one certified ladder. Every field is part of the hash.
type Spec struct {
	Version int `json:"version"`
	// N is the deck size. Small by necessity: the solver is exact, and gops.MaxCards is
	// where honesty about that stops.
	N int `json:"n"`
	// PrizeOrders is the SET of boards a run cycles through. More than one is what makes
	// the metric unfarmable: a single fixed board is memorisable and a lookup table plays it
	// perfectly. The cost is that Phase A spreads over K games — see exploit/orders.go.
	PrizeOrders [][]int `json:"prize_orders"`
	TieRule     string  `json:"tie_rule"`

	// Alpha is the Dirichlet smoothing used to fit the agent's policy. It affects only how
	// TIGHT the resulting bound is, never whether it is valid — but it is in the hash
	// anyway, because two runs that fitted differently did not run the same experiment.
	Alpha float64 `json:"alpha"`
	// Delta is the one-sided error probability for the published bound.
	Delta float64 `json:"delta"`

	// ReferenceOpponent is who the agent plays during Phase A. It is part of the identity
	// because it decides WHICH information sets the fit ever sees: a reference that always
	// bids near the pot explores a different slice of the tree than a uniform one, so two
	// runs with different references did not run the same experiment even though both
	// produce "a bound".
	ReferenceOpponent string `json:"reference_opponent"`

	// ProberMixture is how many bootstrap best responses the prober mixes over. In the
	// hash because a run measured against one prober is not the same experiment as a run
	// measured against eight, and because 1 reinstates the memorisation vulnerability.
	ProberMixture int `json:"prober_mixture"`

	// TargetPrecision is the Phase B stopping target, as a fraction of the payoff range.
	// The run buys matches until the bound's slack falls below it. Zero spends the whole
	// budget. See exploit.ShouldStop for why this replaced "stop when the bound clears
	// zero", which published the weakest bound the schedule could certify.
	TargetPrecision float64 `json:"target_precision"`

	PhaseAMatches   int `json:"phase_a_matches"`
	FirstCheckpoint int `json:"first_checkpoint"`
	MaxPhaseB       int `json:"max_phase_b"`

	// Time control, in milliseconds so the JSON is unambiguous across languages. A spec
	// that did not pin the clock would not be reproducible: how long an agent may think is
	// part of what was measured.
	BudgetMS    int64 `json:"budget_ms"`
	PerMoveMS   int64 `json:"per_move_ms"`
	IncrementMS int64 `json:"increment_ms"`
	GraceMS     int64 `json:"grace_ms"`
}

// DefaultSpec is the shipped n=5 ladder.
//
// PhaseAMatches is 1600 — 200 per board across the 8 default boards. The old value was 60,
// chosen when there was one board, and it was measured to be NET-NEGATIVE: at that budget only
// the root node had 20+ observations, so the fitted prober was WORSE than not fitting at all
// (a plain best-response-to-uniform captured 70-87% of the available value against 48-68% for
// the fit). Coverage is the number to watch, not the match count: 200 matches per board gave
// ~75% node coverage at n=4, and MultiProber.CoveredFraction reports it per run.
//
// TargetPrecision at 0.05 means "measure the lower bound to within 5% of the payoff range".
// Not tuned — it is the coarsest precision at which two agents a tenth of the range apart can
// still be separated, and the spec carries it so a run that needs finer can ask.
//
// MaxPhaseB is a hard cost ceiling. An agent close to optimal would otherwise consume matches
// forever chasing a bound asymptoting to zero, and "not distinguishable from unexploitable at
// 1024 matches" is a real result rather than a failure to be retried into significance.
func DefaultSpec() Spec {
	return Spec{
		Version: 1, N: 5, PrizeOrders: defaultOrders(), TieRule: "carry",
		Alpha: exploit.DefaultAlpha, Delta: exploit.DefaultDelta,
		ReferenceOpponent: RefUniform, ProberMixture: exploit.DefaultProberMixture,
		TargetPrecision: 0.05,
		PhaseAMatches:   1600, FirstCheckpoint: exploit.DefaultFirstCheckpoint, MaxPhaseB: 2048,
		BudgetMS: 600_000, PerMoveMS: 180_000, IncrementMS: 5_000, GraceMS: 2_000,
	}
}

// Reference opponents available for Phase A.
const (
	// RefUniform bids uniformly at random over the legal cards. The natural default: it
	// explores the tree evenly rather than steering the fit toward one region.
	RefUniform = "uniform"
	// RefNearestPool bids the card closest to the contested pool — the platform's existing
	// "proportional" house heuristic. Explores the lines a plausible opponent actually
	// creates, at the cost of leaving odd corners of the tree unvisited.
	RefNearestPool = "nearest_pool"
)

// defaultOrders is the shipped board set: 8 distinct permutations, drawn deterministically.
//
// Eight rather than all 120. Every extra board divides the Phase A budget, and the measured
// tradeoff at a fixed 2400 matches on n=4 was coverage 98% -> 88% -> 75% for K = 1, 4, 12
// with the upper bound widening 5.82 -> 6.78 -> 7.22. Eight is enough that no single board
// can be memorised within a 1024-match Phase B (128 matches per board, spread over 8 mixture
// members) while keeping per-board coverage usable.
func defaultOrders() [][]int {
	o, err := exploit.SamplePrizeOrders(5, 8, 20260820)
	if err != nil {
		panic("ladder: default prize orders are invalid: " + err.Error())
	}
	return o
}

// Games returns one solver configuration per prize order.
func (s Spec) Games() ([]gops.Config, error) {
	if s.TieRule != "carry" {
		return nil, fmt.Errorf("ladder: unsupported tie rule %q", s.TieRule)
	}
	return exploit.Orders(s.N, s.PrizeOrders, gops.TieCarry)
}

// Game returns the FIRST order's config. Retained for callers that only need the deck shape
// (payoff range, node enumeration); anything that measures must use Games.
func (s Spec) Game() (gops.Config, error) {
	cfgs, err := s.Games()
	if err != nil {
		return gops.Config{}, err
	}
	return cfgs[0], nil
}

// TimeControl returns the clock every seat runs on.
func (s Spec) TimeControl() timecontrol.Control {
	return timecontrol.Control{
		Budget:    time.Duration(s.BudgetMS) * time.Millisecond,
		PerMove:   time.Duration(s.PerMoveMS) * time.Millisecond,
		Increment: time.Duration(s.IncrementMS) * time.Millisecond,
		Grace:     time.Duration(s.GraceMS) * time.Millisecond,
	}
}

// Validate rejects a spec that could not produce a certificate anyone should trust.
func (s Spec) Validate() error {
	if s.Version < 1 {
		return fmt.Errorf("ladder: version must be >= 1, got %d", s.Version)
	}
	if _, err := s.Games(); err != nil {
		return err
	}
	if err := s.TimeControl().Validate(); err != nil {
		return err
	}
	switch {
	case s.Alpha < 0:
		return fmt.Errorf("ladder: alpha must be >= 0, got %v", s.Alpha)
	case s.Delta <= 0 || s.Delta >= 1:
		return fmt.Errorf("ladder: delta must be in (0,1), got %v", s.Delta)
	case s.ReferenceOpponent != RefUniform && s.ReferenceOpponent != RefNearestPool:
		return fmt.Errorf("ladder: unknown reference opponent %q", s.ReferenceOpponent)
	case s.ProberMixture < 1:
		return fmt.Errorf("ladder: prober mixture must be >= 1, got %d", s.ProberMixture)
	case s.TargetPrecision < 0 || s.TargetPrecision >= 1:
		return fmt.Errorf("ladder: target precision must be in [0,1), got %v", s.TargetPrecision)
	case s.PhaseAMatches < 1:
		return fmt.Errorf("ladder: phase A needs at least one match, got %d", s.PhaseAMatches)
	case s.FirstCheckpoint < 2:
		return fmt.Errorf("ladder: first checkpoint must be >= 2, got %d", s.FirstCheckpoint)
	case s.MaxPhaseB < s.FirstCheckpoint:
		// Otherwise the run can never reach a look and every certificate is uninformative
		// for a reason that has nothing to do with the agent.
		return fmt.Errorf("ladder: max phase B (%d) is below the first checkpoint (%d)",
			s.MaxPhaseB, s.FirstCheckpoint)
	}
	return nil
}

// Hash is the canonical SHA-256 of the spec, and the identity a certificate cites.
//
// Canonical because Go's encoding/json emits struct fields in declaration order, which is
// stable, but a future refactor that reorders fields would silently change every hash and
// orphan every published certificate. Marshalling through a sorted map removes that hazard:
// the hash depends on the VALUES, not on the source layout.
func (s Spec) Hash() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	canon, err := canonicalJSON(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON serialises with object keys in sorted order, recursively.
func canonicalJSON(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			vb, err := canonicalJSON(t[k])
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
			out = append(out, ':')
			out = append(out, vb...)
		}
		return append(out, '}'), nil
	case []any:
		out := []byte{'['}
		for i, e := range t {
			if i > 0 {
				out = append(out, ',')
			}
			eb, err := canonicalJSON(e)
			if err != nil {
				return nil, err
			}
			out = append(out, eb...)
		}
		return append(out, ']'), nil
	default:
		return json.Marshal(t)
	}
}

// ProberDigest is a stable fingerprint of a computed best response.
//
// Nodes are emitted in sorted order because Go map iteration is randomised, and a digest that
// changed between two runs over the same strategy would be worse than no digest — it would
// fail every audit for the wrong reason.
//
// This is what pins the experiment. Once a run enters Phase B its digest is fixed, so the
// strategy the agent was measured against cannot be swapped afterwards, and a later solver
// change is detected instead of quietly rewriting history.
func ProberDigest(cfg gops.Config, moves map[gops.Node]int) string {
	type entry struct {
		me, opp     uint16
		carry, card int
	}
	list := make([]entry, 0, len(moves))
	for n, card := range moves {
		list = append(list, entry{n.Me, n.Opp, n.Carry, card})
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch {
		case a.me != b.me:
			return a.me < b.me
		case a.opp != b.opp:
			return a.opp < b.opp
		default:
			return a.carry < b.carry
		}
	})
	h := sha256.New()
	fmt.Fprintf(h, "gops-prober-v1;n=%d;order=%v;tie=%d\n", cfg.N, cfg.Order, cfg.Tie)
	for _, e := range list {
		fmt.Fprintf(h, "%d,%d,%d=%d\n", e.me, e.opp, e.carry, e.card)
	}
	return hex.EncodeToString(h.Sum(nil))
}
