package matchmaking

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// memRepo is an in-memory matchmaking.Repo that models the real status machine
// (waiting -> claimed -> matched) so we can assert the claim-before-escrow order.
type memRepo struct {
	status       map[string]string // agent -> waiting|claimed|matched
	matchID      map[string]string
	claimCalls   int
	relCalls     int
	forceNoClaim bool // simulate "another instance won the claim"
}

func newMemRepo(agents ...string) *memRepo {
	r := &memRepo{status: map[string]string{}, matchID: map[string]string{}}
	for _, a := range agents {
		r.status[a] = "waiting"
	}
	return r
}

func (r *memRepo) Upsert(context.Context, Entry) error        { return nil }
func (r *memRepo) Get(context.Context, string) (Entry, error) { return Entry{}, nil }
func (r *memRepo) Delete(context.Context, string) error       { return nil }

func (r *memRepo) Waiting(_ context.Context, _ int) ([]Entry, error) {
	var out []Entry
	for a, s := range r.status {
		if s == "waiting" {
			out = append(out, Entry{AgentPublicID: a, OwnerPublicID: "owner-" + a, Bid: 100, Elo: 1500})
		}
	}
	return out, nil
}

func (r *memRepo) ClaimPair(_ context.Context, a, b string) (bool, error) {
	r.claimCalls++
	if r.forceNoClaim || r.status[a] != "waiting" || r.status[b] != "waiting" {
		return false, nil
	}
	r.status[a], r.status[b] = "claimed", "claimed"
	return true, nil
}

func (r *memRepo) ReleasePair(_ context.Context, a, b string) error {
	r.relCalls++
	for _, x := range []string{a, b} {
		if r.status[x] == "claimed" {
			r.status[x] = "waiting"
		}
	}
	return nil
}

func (r *memRepo) MarkMatched(_ context.Context, a, b, matchID string) error {
	for _, x := range []string{a, b} {
		if r.status[x] != "claimed" {
			return errors.New("mark-matched on a non-claimed entry: " + x)
		}
		r.status[x] = "matched"
		r.matchID[x] = matchID
	}
	return nil
}

type memPairer struct {
	calls int
	err   error
}

func (p *memPairer) CreatePaired(context.Context, string, string, string, string, int64) (string, error) {
	p.calls++
	if p.err != nil {
		return "", p.err
	}
	return "match-1", nil
}

type fixedRating struct{}

func (fixedRating) Elo(context.Context, string) (int, error) { return 1500, nil }

func testMatcher(t *testing.T, repo Repo, pairer Pairer) *Matcher {
	t.Helper()
	svc := New(repo, pairer, fixedRating{}, systemClk{}, Config{Interval: time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	return svc.NewMatcher()
}

type systemClk struct{}

func (systemClk) Now() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

// T1: a normal pairing claims first, escrows exactly once, then marks matched.
func TestPairClaimsBeforeEscrow(t *testing.T) {
	repo := newMemRepo("a", "b")
	pairer := &memPairer{}
	if err := testMatcher(t, repo, pairer).tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if pairer.calls != 1 {
		t.Fatalf("CreatePaired calls = %d, want 1", pairer.calls)
	}
	if repo.claimCalls != 1 {
		t.Fatalf("ClaimPair calls = %d, want 1", repo.claimCalls)
	}
	if repo.status["a"] != "matched" || repo.status["b"] != "matched" {
		t.Fatalf("both should be matched, got a=%s b=%s", repo.status["a"], repo.status["b"])
	}
}

// T2: if escrow fails, the claim is released so both re-enter the pool — and the
// stake is escrowed exactly once (never twice).
func TestEscrowFailureReleasesClaim(t *testing.T) {
	repo := newMemRepo("a", "b")
	pairer := &memPairer{err: errors.New("insufficient balance")}
	if err := testMatcher(t, repo, pairer).tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if pairer.calls != 1 {
		t.Fatalf("CreatePaired calls = %d, want 1 (no double escrow)", pairer.calls)
	}
	if repo.relCalls != 1 {
		t.Fatalf("ReleasePair calls = %d, want 1", repo.relCalls)
	}
	if repo.status["a"] != "waiting" || repo.status["b"] != "waiting" {
		t.Fatalf("both should be back to waiting, got a=%s b=%s", repo.status["a"], repo.status["b"])
	}
}

// T3: if the claim is denied (another instance won it), nothing is escrowed.
func TestNoEscrowWithoutClaim(t *testing.T) {
	repo := newMemRepo("a", "b")
	repo.forceNoClaim = true
	pairer := &memPairer{}
	if err := testMatcher(t, repo, pairer).tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if pairer.calls != 0 {
		t.Fatalf("CreatePaired calls = %d, want 0 (must never escrow an unclaimed pair)", pairer.calls)
	}
}
