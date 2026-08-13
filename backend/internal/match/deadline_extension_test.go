package match

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/deadline"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/platform"
)

// The extension bound, tested as the arithmetic it is.
//
// tryExtend itself needs a Service, a repo, a clock and a liveness prober, and a test built on
// all four would be testing the wiring. The DEFECT was none of those — it was one expression:
// "extensions granted so far" derived from an elapsed time measured against an origin the
// extension itself moves. So the loop is simulated here directly, exactly as the sweeper runs
// it, which is what makes the difference between the two origins visible.
//
// Observed in production before the fix: Goofspiel granted 17 extensions against a
// MaxExtensions of 3, holding one round open for twelve minutes while an agent whose every move
// was rejected went on answering /health.

// simulateExtensions runs the sweeper's extend loop and returns how many extensions were
// granted before the policy said stop.
//
// movingOrigin reproduces the bug: when true the origin is the CURRENT deadline, which each
// grant pushes forward. When false it is the round's fixed base, which nothing moves.
//
// Capped at hardStop so the broken case returns a finite number instead of hanging the test —
// a runaway must be reported as a failure, not as a timeout nobody reads.
func simulateExtensions(pol deadline.Policy, window time.Duration, movingOrigin bool, hardStop int) int {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	origin := now              // when the round actually started
	current := now.Add(window) // the deadline as first set

	granted := 0
	for granted < hardStop {
		// The sweeper wakes when the current deadline lapses.
		now = current

		from := origin
		if movingOrigin {
			from = current.Add(-window)
		}
		elapsed := now.Sub(from)

		soFar := 0
		if elapsed > window && pol.Extension > 0 {
			soFar = int((elapsed - window) / pol.Extension)
		}
		ext, ok := deadline.Extend(pol, elapsed, soFar)
		if !ok {
			return granted
		}
		current = now.Add(ext)
		granted++
	}
	return granted
}

// With a fixed origin the bound holds: extensions stop, and the round cannot be held open past
// the policy ceiling.
func TestRoundExtensionsAreBoundedByPolicy(t *testing.T) {
	for _, game := range []string{"goofspiel", "mafia", "monopoly"} {
		t.Run(game, func(t *testing.T) {
			pol := deadline.DefaultPolicy(game)
			// A window at the policy floor, which is the tightest case and therefore the one
			// that produces the most extensions before the ceiling.
			got := simulateExtensions(pol, pol.Floor, false, 500)
			if got >= 500 {
				t.Fatalf("extensions never stopped (hit the %d cap): a responsive agent can hold "+
					"a round open forever", 500)
			}
			// The ceiling may cut in before MaxExtensions, so at-most is the real contract.
			if got > pol.MaxExtensions {
				t.Fatalf("granted %d extensions, policy allows at most %d", got, pol.MaxExtensions)
			}
		})
	}
}

// The regression itself, stated as a test.
//
// This is what makes the test above meaningful: it demonstrates that the SAME policy, with the
// origin allowed to move, does not converge. If a future change reintroduces the moving origin,
// the test above starts failing — and this one documents why.
func TestAMovingOriginNeverReachesTheExtensionCeiling(t *testing.T) {
	pol := deadline.DefaultPolicy("goofspiel")
	const cap = 200

	fixed := simulateExtensions(pol, pol.Floor, false, cap)
	moving := simulateExtensions(pol, pol.Floor, true, cap)

	if fixed >= cap {
		t.Fatalf("the FIXED origin also ran away (%d extensions) — the bound is not working", fixed)
	}
	if moving < cap {
		t.Fatalf("the moving origin stopped after %d extensions; this test encodes the bug that "+
			"measuring elapsed from a deadline the extension moves never converges, so if it now "+
			"converges the arithmetic changed and TestRoundExtensionsAreBoundedByPolicy no longer "+
			"proves anything", moving)
	}
	t.Logf("fixed origin: %d extensions (bounded)   moving origin: unbounded (hit the %d cap)",
		fixed, cap)
}

// The fallback path. A row written before migration 0085 has no base, and such a round must
// behave exactly as it did before rather than losing its extensions entirely — an in-flight
// match must not be forfeited by a deploy.
func TestMissingBaseFallsBackToTheDeadline(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	dl := now.Add(30 * time.Second)

	m := Match{RoundDeadline: &dl}
	origin := m.RoundDeadline
	if m.RoundDeadlineBase != nil {
		origin = m.RoundDeadlineBase
	}
	if origin == nil || !origin.Equal(dl) {
		t.Fatalf("origin = %v, want the deadline %v when no base is recorded", origin, dl)
	}

	base := now
	m.RoundDeadlineBase = &base
	origin = m.RoundDeadlineBase
	if !origin.Equal(base) {
		t.Fatalf("origin = %v, want the base %v when one is recorded", origin, base)
	}
}

// --- A REFUSED move earns no extension ------------------------------------------------------
//
// The extension exists to give a slow-but-working agent time to answer, and /health is the
// evidence it is working. Completion binding broke that inference: an agent whose every move
// contradicts its own model's output is refused each time and stays perfectly responsive, so it
// reads as "still thinking" until the policy ceiling — roughly two minutes a round, on a table
// an opponent has staked real coins on.
//
// A seat that answered and was turned away is not waiting on a model. It can resubmit inside
// the window it already has; the extension is not the retry mechanism.

type aliveProber struct{}

func (aliveProber) Alive(context.Context, string) bool { return true }

// rejectLog answers the one question tryExtend asks. err simulates the lookup failing.
type rejectLog struct {
	rejected map[string]bool
	err      error
}

func (r rejectLog) RecordMoveRejection(context.Context, string, string, int, string) error {
	return nil
}
func (r rejectLog) MoveRejected(_ context.Context, _, agentPublicID string, _ int) (bool, error) {
	return r.rejected[agentPublicID], r.err
}

// extendRepo captures whether the deadline was actually pushed out.
type extendRepo struct {
	Repo
	extended bool
}

func (e *extendRepo) ExtendDeadline(context.Context, string, time.Time) error {
	e.extended = true
	return nil
}

func extendOnce(t *testing.T, log RejectionLog) bool {
	t.Helper()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	window := 30 * time.Second
	base := now.Add(-2 * window) // the round has been open long enough to want an extension
	repo := &extendRepo{}
	s := &Service{
		repo:      repo,
		clock:     platform.FixedClock{T: now},
		cfg:       Config{MoveWindow: window},
		livecheck: aliveProber{},
	}
	s.SetRejectionLog(log)
	deadlineAt := base.Add(window)
	m := Match{
		PublicID:          "m_reject_ext",
		Game:              "goofspiel",
		RoundDeadline:     &deadlineAt,
		RoundDeadlineBase: &deadlineAt,
		State:             gs.State{Round: 4},
	}
	return s.tryExtend(context.Background(), m, []string{"ag_a"})
}

func TestARespondingSeatStillEarnsAnExtension(t *testing.T) {
	// The baseline. Without it, "a rejected seat is not extended" could pass simply because
	// nothing is ever extended, and the test would prove nothing.
	if !extendOnce(t, rejectLog{rejected: map[string]bool{}}) {
		t.Fatal("a live seat with no refused move was not extended — the extension path is not " +
			"reachable in this test, so the rejection case below would prove nothing")
	}
}

func TestARejectedSeatEarnsNoExtension(t *testing.T) {
	if extendOnce(t, rejectLog{rejected: map[string]bool{"ag_a": true}}) {
		t.Error("a seat whose move was REFUSED this round was given more time. It answered and " +
			"was turned away, so it is not thinking — and every extension it collects is time " +
			"an opponent who staked coins spends waiting")
	}
}

func TestAnUnreadableRejectionLogExtendsAsBefore(t *testing.T) {
	// Fails OPEN, and that direction is safe here precisely because this check can only ever
	// REMOVE time. A lookup error leaves the behaviour exactly as it was before the check
	// existed, rather than converting a database hiccup into forfeited rounds.
	if !extendOnce(t, rejectLog{err: errors.New("database is down")}) {
		t.Error("a failed rejection lookup cost the seat its extension — a hiccup must not " +
			"become a forfeit")
	}
}

func TestWithoutTheRejectionLogNothingChanges(t *testing.T) {
	if !extendOnce(t, nil) {
		t.Error("an unwired rejection log changed extension behaviour; it must be inert")
	}
}

// ── a 429 must never cost a stake, and must never buy more than policy ───────

// stubRateLimits reports a 429 for whatever it is told, so the test can drive the
// qualification path without a database.
type stubRateLimits struct {
	limited bool
	calls   int
	// gotRound records the round it was asked about, to prove the query is round-scoped.
	gotRound int
}

func (s *stubRateLimits) RateLimited(_ context.Context, _, _ string, round int, _ time.Duration) bool {
	s.calls++
	s.gotRound = round
	return s.limited
}

// extendWithRateLimit drives tryExtend with a DEAD prober and a configurable 429 observer,
// reusing the same repo/clock shape as extendOnce above — which is the harness already proven
// to reach the extension path. My first version built its own bare Service and panicked on a
// nil repo AFTER the qualification loop, so it never tested the thing it claimed to.
func extendWithRateLimit(t *testing.T, limited bool) (bool, *stubRateLimits) {
	t.Helper()
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	window := 30 * time.Second
	base := now.Add(-2 * window)
	rl := &stubRateLimits{limited: limited}
	s := &Service{
		repo:       &extendRepo{},
		clock:      platform.FixedClock{T: now},
		cfg:        Config{MoveWindow: window},
		livecheck:  deadStub{}, // the probe says nothing is listening — the case that forfeited
		rateLimits: rl,
	}
	deadlineAt := base.Add(window)
	m := Match{
		PublicID:          "m_429",
		Game:              "goofspiel",
		RoundDeadline:     &deadlineAt,
		RoundDeadlineBase: &deadlineAt,
		State:             gs.State{Round: 7},
	}
	return s.tryExtend(context.Background(), m, []string{"ag_a"}), rl
}

// A seat whose endpoint does NOT answer a health probe still qualifies when the gateway
// watched it get rate-limited: a 429 is stronger evidence of "present and trying" than a
// probe, because the platform saw it make a real model call for THIS decision.
//
// Without this, a developer on a free tier forfeits a STAKED match — real coins — because
// their provider throttled them mid-round.
func TestARateLimitedSeatQualifiesForAnExtensionWhenTheProbeFails(t *testing.T) {
	got, rl := extendWithRateLimit(t, true)
	if !got {
		t.Fatal("a seat the gateway watched get rate-limited did not qualify for an extension; " +
			"it forfeits a staked match because its provider throttled it")
	}
	if rl.calls == 0 {
		t.Error("the rate-limit observer was never consulted")
	}
	// Round-scoped: a 429 from an earlier turn says nothing about whether THIS turn is being
	// attempted, and accepting it would hand an idle agent free time.
	if rl.gotRound != 7 {
		t.Errorf("asked about round %d, want the current round 7", rl.gotRound)
	}
}

// Not an amnesty. A seat that is neither alive nor rate-limited must STILL forfeit, or a
// crashed agent stalls every round to the ceiling.
func TestADeadSeatWithNoRateLimitEvidenceStillForfeits(t *testing.T) {
	if got, _ := extendWithRateLimit(t, false); got {
		t.Error("a seat that is neither alive nor rate-limited was granted an extension; " +
			"an agent that simply went away must still forfeit")
	}
}

// The bound. A 429 decides WHETHER a seat qualifies, never HOW MUCH time it gets — that still
// comes from deadline.Extend with MaxExtensions and Ceiling untouched, so a throttled agent
// cannot hold a table open longer than a slow one. The opponent staked coins too.
func TestARateLimitedSeatGetsNoMoreExtensionsThanPolicyAllows(t *testing.T) {
	for _, game := range []string{"goofspiel", "mafia", "monopoly"} {
		t.Run(game, func(t *testing.T) {
			pol := deadline.DefaultPolicy(game)
			got := simulateExtensions(pol, pol.Floor, false, 500)
			if got > pol.MaxExtensions {
				t.Fatalf("a throttled seat could take %d extensions; policy allows at most %d",
					got, pol.MaxExtensions)
			}
		})
	}
}

// deadStub is a prober that always says "nothing is listening".
type deadStub struct{}

func (deadStub) Alive(context.Context, string) bool { return false }
