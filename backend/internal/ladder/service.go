// The certification runner: the loop that turns a state machine and a solver into a
// published certificate.
//
// # Shape
//
// Three ports, because the three concerns fail differently and should be substitutable
// independently:
//
//	Repo    persists run state, fit counts and payoffs. Must be idempotent.
//	Driver  actually plays matches. The only part that costs money and wall-clock.
//	Service orchestrates. Pure decisions, no I/O of its own.
//
// Advance performs exactly ONE step and returns. It does not loop internally, and that is
// deliberate: a certification run spans hundreds of matches and many minutes, so the caller —
// a worker, a cron, an admin action — owns the pacing, the retry policy and the cancellation.
// A service that looped inside would take those decisions away from whoever can actually make
// them well.
//
// # Crash safety
//
// Every step is derived from persisted state, so a process that dies mid-run leaves nothing
// behind but work already recorded. The next Advance reads the database and computes the same
// instruction the dead process would have. There is no in-memory progress to lose, which is
// why Next in run.go is a pure function of a Run rather than a method on a live object.
//
// The one thing that MUST NOT be re-derived is the prober. It is pinned once, at the
// fit->certify transition, and every later step recomputes it from the stored counts and
// checks the digest. A solver change between two steps of the same run is therefore caught
// rather than silently changing what the agent is being measured against.
package ladder

import (
	"context"
	"fmt"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
)

// MatchPayoff is one Phase B result, from the PROBER's perspective.
type MatchPayoff struct {
	MatchID string
	Seq     int
	Payoff  float64
}

// Repo persists a certification run. Every write must be idempotent on its natural key: a
// worker that crashes after playing matches but before recording them will replay them, and a
// double-counted payoff would silently narrow a published bound.
type Repo interface {
	ActiveSpec(ctx context.Context) (Spec, error)
	// OpenRun returns the agent's open run for this spec, creating one in PhaseFit if none
	// exists. The schema permits only one open run per (agent, spec) — two would interleave
	// their Phase B matches and both bounds would be about a mixture.
	OpenRun(ctx context.Context, agentPublicID string, specVersion int) (Run, error)
	// AddFitCounts records a Phase A batch: the tally, how many matches it covered, and
	// the reconstruction census. All three in one call so a crash cannot leave them
	// disagreeing — which would either re-play matches already paid for, advance a phase on
	// a short sample, or publish a kept fraction that does not match the counts.
	AddFitCounts(ctx context.Context, runID int64, counts []exploit.OrderedCount, matchesPlayed int, census exploit.Census) error
	FitCounts(ctx context.Context, runID int64) ([]exploit.OrderedCount, error)
	PinProber(ctx context.Context, runID int64, digest string) error
	AddPayoffs(ctx context.Context, runID int64, p []MatchPayoff) error
	Payoffs(ctx context.Context, runID int64) ([]float64, error)
	SaveCertificate(ctx context.Context, c Certificate) error
}

// Driver plays matches on the certified ladder. Zero-stake and unrated, always: a measurement
// that can move coins acquires an incentive to be wrong.
type Driver interface {
	// PlayFit runs n Phase A matches against the reference opponent and returns the agent's
	// decisions, plus a census of what could not be reconstructed.
	//
	// startIndex is how many fit matches this run has ALREADY played. It exists because a
	// resumed run would otherwise replay identical match seeds and fit the prober on the
	// same board twice, biasing it toward whichever lines those seeds happened to create.
	PlayFit(ctx context.Context, agentPublicID string, s Spec, n, startIndex int) ([]exploit.Observation, exploit.Census, error)
	// PlayCertify runs n Phase B matches with the prober seated opposite the agent, and
	// returns one payoff per match FROM THE PROBER'S SIDE.
	//
	// startIndex is how many Phase B matches this run has already played, and is
	// load-bearing rather than cosmetic: Phase B is requested one checkpoint batch at a
	// time, and without it every batch would mint the same match ids. AddPayoffs is
	// idempotent on the match id, so the duplicates would be absorbed, certify_done would
	// never grow, and the run would loop forever paying for matches it then discarded.
	PlayCertify(ctx context.Context, agentPublicID string, s Spec, prober exploit.MultiProber, n, startIndex int) ([]MatchPayoff, error)
}

// Service drives certification runs.
type Service struct {
	repo   Repo
	driver Driver
}

func NewService(r Repo, d Driver) *Service { return &Service{repo: r, driver: d} }

// Step reports what one Advance did, so a caller can log progress and decide whether to keep
// going without re-reading the database.
type Step struct {
	Action      Action
	Run         Run
	Certificate *Certificate // non-nil only on the step that finishes the run
	Done        bool
}

// Advance performs one step of an agent's certification run.
//
// Safe to call repeatedly. Calling it on a finished run is a no-op rather than an error: a
// worker sweeping every open run should not have to race the closing of one.
func (s *Service) Advance(ctx context.Context, agentPublicID string) (Step, error) {
	spec, err := s.repo.ActiveSpec(ctx)
	if err != nil {
		return Step{}, fmt.Errorf("ladder: read active spec: %w", err)
	}
	cfgs, err := spec.Games()
	if err != nil {
		return Step{}, err
	}
	run, err := s.repo.OpenRun(ctx, agentPublicID, spec.Version)
	if err != nil {
		return Step{}, fmt.Errorf("ladder: open run: %w", err)
	}

	// The sequential certificate is only meaningful in the certify phase, and reading
	// payoffs during the fit phase would be a wasted query on every step of the longest
	// part of the run.
	var cert exploit.SequentialCertificate
	if run.Phase == PhaseCertify {
		payoffs, err := s.repo.Payoffs(ctx, run.ID)
		if err != nil {
			return Step{}, fmt.Errorf("ladder: read payoffs: %w", err)
		}
		// HALF the budget, because the certificate publishes a two-sided interval and the
		// upper half spends the other half. Passing the full delta here — as an earlier
		// version did — makes the total spend 1.5*delta, so the interval does NOT hold at
		// the confidence it states. The bundle's self-verification caught it: the runner
		// published 8.8106 while a recompute at delta/2 gave 8.7707.
		if cert, err = exploit.SequentialCertify(cfgs[0], payoffs, spec.Delta/2,
			spec.FirstCheckpoint); err != nil {
			return Step{}, err
		}
	}

	action, err := Next(run, spec, cert)
	if err != nil {
		return Step{}, err
	}

	switch action.Kind {
	case ActionNone:
		return Step{Action: action, Run: run, Done: true}, nil

	case ActionPlayFit:
		obs, census, err := s.driver.PlayFit(ctx, agentPublicID, spec, action.Matches, run.FitDone)
		if err != nil {
			return Step{}, fmt.Errorf("ladder: play fit: %w", err)
		}
		if err := s.repo.AddFitCounts(ctx, run.ID, exploit.TallyOrdered(obs), action.Matches, census); err != nil {
			return Step{}, fmt.Errorf("ladder: record fit: %w", err)
		}
		run.FitDone += action.Matches
		run.FitKept += census.Kept
		run.FitOffered += census.Total()
		return Step{Action: action, Run: run}, nil

	case ActionComputeProber:
		prober, err := s.prober(ctx, run, spec, cfgs)
		if err != nil {
			return Step{}, err
		}
		digest := prober.Digest(cfgs, ProberDigest)
		if err := s.repo.PinProber(ctx, run.ID, digest); err != nil {
			return Step{}, fmt.Errorf("ladder: pin prober: %w", err)
		}
		if run, err = EnterCertify(run, digest); err != nil {
			return Step{}, err
		}
		return Step{Action: action, Run: run}, nil

	case ActionPlayCertify:
		prober, err := s.prober(ctx, run, spec, cfgs)
		if err != nil {
			return Step{}, err
		}
		payoffs, err := s.driver.PlayCertify(ctx, agentPublicID, spec, prober, action.Matches, run.CertifyDone)
		if err != nil {
			return Step{}, fmt.Errorf("ladder: play certify: %w", err)
		}
		if err := s.repo.AddPayoffs(ctx, run.ID, payoffs); err != nil {
			return Step{}, fmt.Errorf("ladder: record payoffs: %w", err)
		}
		run.CertifyDone += len(payoffs)
		return Step{Action: action, Run: run}, nil

	case ActionFinish:
		// The upper half comes from the Phase A confidence region, so it is computed here
		// rather than carried through Phase B. Without it the certificate would publish a
		// lower bound alone, which cannot order agents.
		counts, err := s.repo.FitCounts(ctx, run.ID)
		if err != nil {
			return Step{}, err
		}
		upper, err := exploit.MultiUpperBound(cfgs, counts, spec.Delta/2)
		if err != nil {
			return Step{}, err
		}
		separable := (upper - cert.LowerBound) <= exploit.PayoffRange(cfgs[0])/3
		out, closed, err := Finish(run, spec, cert, upper, separable, action.Reason,
			run.KeptFraction())
		if err != nil {
			return Step{}, err
		}
		if err := s.repo.SaveCertificate(ctx, out); err != nil {
			return Step{}, fmt.Errorf("ladder: save certificate: %w", err)
		}
		return Step{Action: action, Run: closed, Certificate: &out, Done: true}, nil

	default:
		return Step{}, fmt.Errorf("ladder: unhandled action %q", action.Kind)
	}
}

// prober recomputes the MIXTURE from the stored fit counts and verifies it against the pinned
// digest.
//
// RECOMPUTED, NEVER STORED. The prober is a deterministic function of (spec, counts) and the
// solve is milliseconds, so persisting it would add a large blob that can drift from the
// inputs it claims to summarise. Recomputing and checking the digest instead means a solver
// change between two steps of one run is caught loudly, rather than quietly changing the
// strategy the agent is measured against halfway through Phase B.
//
// During the fit->certify transition there is no digest yet, so the check is skipped — that
// call is what establishes it.
func (s *Service) prober(ctx context.Context, run Run, spec Spec, cfgs []gops.Config) (exploit.MultiProber, error) {
	counts, err := s.repo.FitCounts(ctx, run.ID)
	if err != nil {
		return exploit.MultiProber{}, fmt.Errorf("ladder: read fit counts: %w", err)
	}
	// The mixture seed is derived from the run, not drawn: an auditor recomputing from the
	// published counts must get the same strategies in the same order, or they cannot check
	// which probers the agent actually faced.
	mix, err := exploit.BootstrapMultiProbers(cfgs, counts, spec.Alpha, spec.ProberMixture,
		uint64(run.ID)*0x9E3779B97F4A7C15+uint64(spec.Version))
	if err != nil {
		return exploit.MultiProber{}, err
	}
	if run.ProberDigest != "" {
		if got := mix.Digest(cfgs, ProberDigest); got != run.ProberDigest {
			return exploit.MultiProber{}, fmt.Errorf(
				"ladder: prober digest mismatch on run %d (pinned %s, recomputed %s) — the "+
					"solver or the stored fit changed mid-run; this certificate is void",
				run.ID, run.ProberDigest, got)
		}
	}
	return mix, nil
}
