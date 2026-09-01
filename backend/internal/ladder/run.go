// The certification run state machine.
//
// A run is long-lived: it spans hundreds of matches, many processes, and a restart or two.
// The machine is therefore a PURE function of persisted state — no timers, no in-memory
// progress, nothing that a crash loses. A runner asks "what next" and gets an instruction; if
// it dies mid-instruction the next caller asks again and gets the same answer, because the
// answer is derived from what is in the database rather than from what the last process was
// doing.
//
// That shape also makes the whole thing testable without a database, which matters here: the
// phase transitions are where a certification quietly becomes invalid — Phase B play folded
// into the fit, a prober recomputed after measurement began, a bound read at a sample size
// nobody committed to — and none of those failures are visible in the published number.
package ladder

import (
	"fmt"

	"github.com/agent-arena/arena/internal/exploit"
)

// Phase is where a run has got to.
type Phase string

const (
	// PhaseFit is collecting Phase A matches against the reference opponent.
	PhaseFit Phase = "fit"
	// PhaseCertify is playing the prober live and accumulating payoffs.
	PhaseCertify Phase = "certify"
	// PhaseDone is finished; a certificate exists.
	PhaseDone Phase = "done"
	// PhaseAbandoned is finished without a certificate — the agent stopped playing, or the
	// spec was retired mid-run. Distinct from Done so an abandoned run is never read as
	// "measured and found unexploitable".
	PhaseAbandoned Phase = "abandoned"
)

// Run is the persisted state of one agent's attempt at one spec.
type Run struct {
	ID          int64
	AgentID     string
	SpecVersion int
	Phase       Phase
	FitDone     int
	CertifyDone int
	// FitKept / FitOffered accumulate the reconstruction census across Phase A batches.
	// A run whose fit was mostly unusable produced a weak prober and so a loose bound, and
	// a reader comparing two certificates has to be able to see that.
	FitKept    int
	FitOffered int
	// ProberDigest is empty until the fit->certify transition and immutable after it. It is
	// what makes the published certificate checkable: recompute the prober from the spec
	// and the stored counts, digest it, and compare.
	ProberDigest string
}

// KeptFraction is the share of offered Phase A decisions that became usable observations.
//
// Returns 0 for a run that was offered nothing rather than 1, because "everything we asked
// for came back" and "we never asked" are different states and only one of them is evidence.
func (r Run) KeptFraction() float64 {
	if r.FitOffered <= 0 {
		return 0
	}
	return float64(r.FitKept) / float64(r.FitOffered)
}

// ActionKind is what the runner should do next.
type ActionKind string

const (
	// ActionPlayFit: run this many Phase A matches against the reference opponent.
	ActionPlayFit ActionKind = "play_fit"
	// ActionComputeProber: fit the policy, solve the best response, pin its digest, and
	// move to certify. No matches.
	ActionComputeProber ActionKind = "compute_prober"
	// ActionPlayCertify: run this many Phase B matches against the pinned prober.
	ActionPlayCertify ActionKind = "play_certify"
	// ActionFinish: write the certificate and close the run.
	ActionFinish ActionKind = "finish"
	// ActionNone: nothing to do; the run is already closed.
	ActionNone ActionKind = "none"
)

// Action is one instruction. Matches is zero for anything that plays nothing.
type Action struct {
	Kind    ActionKind
	Matches int
	Reason  string
}

// Next decides the run's next instruction.
//
// cert is the sequential certificate computed from the payoffs recorded so far; it is ignored
// outside PhaseCertify. Passing it in rather than computing it here keeps this function pure
// and free of the solver, so a caller can unit-test the transitions without playing a game.
//
// Phase B is requested in CHECKPOINT-SIZED BATCHES rather than one match at a time. The bound
// may only be read at the alpha-spending checkpoints, so a smaller batch cannot produce a
// decision and only buys scheduling round-trips.
func Next(r Run, s Spec, cert exploit.SequentialCertificate) (Action, error) {
	if err := s.Validate(); err != nil {
		return Action{}, err
	}
	if r.SpecVersion != s.Version {
		// A run measured against one spec cannot be continued against another: the prober,
		// the deck and the clock would all have changed underneath it.
		return Action{}, fmt.Errorf("ladder: run is on spec v%d, given v%d",
			r.SpecVersion, s.Version)
	}

	switch r.Phase {
	case PhaseDone, PhaseAbandoned:
		return Action{Kind: ActionNone, Reason: string(r.Phase)}, nil

	case PhaseFit:
		if r.ProberDigest != "" {
			// The digest is pinned at the transition and only there. A run still fitting
			// that already carries one has been rewound, and continuing would fold Phase B
			// play into the fit — the exact leak sample splitting exists to prevent.
			return Action{}, fmt.Errorf(
				"ladder: run %d is in fit but already has a prober digest; refusing to continue",
				r.ID)
		}
		if remaining := s.PhaseAMatches - r.FitDone; remaining > 0 {
			return Action{Kind: ActionPlayFit, Matches: remaining,
				Reason: "collecting fit sample"}, nil
		}
		return Action{Kind: ActionComputeProber,
			Reason: "fit sample complete"}, nil

	case PhaseCertify:
		if r.ProberDigest == "" {
			// Measuring against a strategy nobody pinned means the certificate cannot be
			// checked, and nothing downstream would notice.
			return Action{}, fmt.Errorf(
				"ladder: run %d is certifying with no pinned prober", r.ID)
		}
		cfg, err := s.Game()
		if err != nil {
			return Action{}, err
		}
		if stop, reason := exploit.ShouldStop(cfg, cert, r.CertifyDone, s.MaxPhaseB,
			s.TargetPrecision); stop {
			return Action{Kind: ActionFinish, Reason: reason}, nil
		}
		batch := exploit.NextCheckpoint(s.FirstCheckpoint, r.CertifyDone)
		if over := r.CertifyDone + batch - s.MaxPhaseB; over > 0 {
			// Never buy matches past the cost ceiling just to reach a look. If the budget
			// cannot fund the next checkpoint, the run finishes with what it has.
			batch -= over
		}
		if batch <= 0 {
			return Action{Kind: ActionFinish, Reason: "budget_exhausted"}, nil
		}
		return Action{Kind: ActionPlayCertify, Matches: batch,
			Reason: "advancing to next checkpoint"}, nil

	default:
		return Action{}, fmt.Errorf("ladder: unknown phase %q on run %d", r.Phase, r.ID)
	}
}

// EnterCertify applies the fit->certify transition.
//
// Separate from Next, and returning a new Run rather than mutating, so the digest can only be
// set at this one point. A setter would let any caller pin, repin, or clear it.
func EnterCertify(r Run, digest string) (Run, error) {
	if r.Phase != PhaseFit {
		return r, fmt.Errorf("ladder: cannot enter certify from phase %q", r.Phase)
	}
	if r.ProberDigest != "" {
		return r, fmt.Errorf("ladder: run %d already pinned a prober", r.ID)
	}
	if digest == "" {
		return r, fmt.Errorf("ladder: refusing to enter certify with an empty prober digest")
	}
	r.Phase = PhaseCertify
	r.ProberDigest = digest
	return r, nil
}

// Certificate is the published result of a run.
//
// SpecHash and ProberDigest travel with the numbers deliberately. A bound without them names
// a measurement nobody can reconstruct — which spec, against which strategy — and the whole
// argument for this ladder over a win-rate leaderboard is that it can be checked.
type Certificate struct {
	RunID        int64
	AgentID      string
	SpecVersion  int
	SpecHash     string
	ProberDigest string
	Games        int
	MeanPayoff   float64
	StdDev       float64
	// LowerBound comes from live Phase B play; UpperBound from the Phase A confidence
	// region. Both are published because a lower bound ALONE CANNOT ORDER AGENTS — an
	// external audit measured Kendall tau 0.49 and a quarter of pairs inverted when ranking
	// on it. Two agents are comparable only when their intervals are disjoint.
	LowerBound float64
	UpperBound float64
	// Separable is false when the interval is too wide to rank on. Publishing a midpoint
	// from a wide interval invents precision; internal/deception refuses the same way.
	Separable   bool
	Delta       float64
	Informative bool
	StopReason  string
	// FitMatches is how many matches the prober was fitted on. Published because a weak
	// fit yields a weaker bound, and a reader comparing two agents should be able to see
	// that one was probed harder than the other.
	FitMatches int
	// KeptFraction is the share of offered decisions that survived reconstruction. A run
	// that silently dropped most of its input produced a bound about a different
	// population than the one it names.
	KeptFraction float64
}

// Finish builds the published certificate from a finished run.
func Finish(r Run, s Spec, cert exploit.SequentialCertificate, upper float64, separable bool,
	reason string, kept float64) (Certificate, Run, error) {
	if r.Phase != PhaseCertify {
		return Certificate{}, r, fmt.Errorf("ladder: cannot finish from phase %q", r.Phase)
	}
	hash, err := s.Hash()
	if err != nil {
		return Certificate{}, r, err
	}
	out := Certificate{
		RunID: r.ID, AgentID: r.AgentID, SpecVersion: s.Version, SpecHash: hash,
		ProberDigest: r.ProberDigest,
		Games:        cert.Games, MeanPayoff: cert.MeanPayoff, StdDev: cert.StdDev,
		LowerBound: cert.LowerBound, UpperBound: upper, Separable: separable,
		Delta: s.Delta, Informative: cert.Informative,
		StopReason: reason, FitMatches: r.FitDone, KeptFraction: kept,
	}
	r.Phase = PhaseDone
	return out, r, nil
}
