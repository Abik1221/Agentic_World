//go:build ladderlive

package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
	"github.com/agent-arena/arena/internal/ladder"
)

// Live repo test. Build-tagged so the default suite stays hermetic; run with
//
//	go test -tags ladderlive ./internal/store/ -run TestLadderRepoLive
//
// against a database carrying migration 0097.
//
// Worth running against the real thing rather than a fake: every idempotency guarantee this
// repo claims is enforced by a constraint, not by Go, so a fake would test the fake.
func TestLadderRepoLive(t *testing.T) {
	dsn := os.Getenv("LADDER_TEST_DSN")
	if dsn == "" {
		t.Skip("LADDER_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewLadderRepo(db)

	spec := ladder.DefaultSpec()
	if err := r.PublishSpec(ctx, spec, true); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Re-publishing the same version must be a no-op, not an error and not a rewrite.
	if err := r.PublishSpec(ctx, spec, true); err != nil {
		t.Fatalf("republish should be a no-op: %v", err)
	}
	got, err := r.ActiveSpec(ctx)
	if err != nil {
		t.Fatalf("active spec: %v", err)
	}
	if got.Version != spec.Version || got.N != spec.N || got.Delta != spec.Delta {
		t.Fatalf("round-trip lost fields: %+v", got)
	}

	run, err := r.OpenRun(ctx, "ag_test", spec.Version)
	if err != nil {
		t.Fatalf("open run: %v", err)
	}
	if run.Phase != ladder.PhaseFit || run.ProberDigest != "" {
		t.Fatalf("new run should be fitting with no prober: %+v", run)
	}
	// Idempotent: a second call returns the same run rather than creating another.
	again, err := r.OpenRun(ctx, "ag_test", spec.Version)
	if err != nil || again.ID != run.ID {
		t.Fatalf("OpenRun is not idempotent: %+v / %v", again, err)
	}

	// Counts ACCUMULATE across batches; they must never overwrite.
	n1 := gops.Node{Me: 0b11111, Opp: 0b11111}
	census := exploit.Census{Kept: 5, Dropped: map[exploit.RejectReason]int{exploit.RejectMalformed: 1}}
	for i := 0; i < 3; i++ {
		if err := r.AddFitCounts(ctx, run.ID, []exploit.OrderedCount{
			{Order: 0, Count: exploit.Count{Node: n1, Card: 3, N: 2}},
			{Order: 1, Count: exploit.Count{Node: n1, Card: 3, N: 5}},
		}, 20, census); err != nil {
			t.Fatalf("add fit counts: %v", err)
		}
	}
	counts, err := r.FitCounts(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Two orders must stay SEPARATE: merging them would fit a policy to a game nobody played.
	byOrder := map[int]int{}
	for _, c := range counts {
		if c.Node != n1 || c.Card != 3 {
			t.Fatalf("node round-trip lost fidelity: %+v", c)
		}
		byOrder[c.Order] += c.N
	}
	if len(byOrder) != 2 || byOrder[0] != 6 || byOrder[1] != 15 {
		t.Fatalf("per-order counts wrong or merged: %+v", byOrder)
	}
	run, _ = r.OpenRun(ctx, "ag_test", spec.Version)
	if run.FitDone != 60 || run.FitKept != 15 || run.FitOffered != 18 {
		t.Fatalf("progress/census not accumulated: done=%d kept=%d offered=%d",
			run.FitDone, run.FitKept, run.FitOffered)
	}

	if err := r.PinProber(ctx, run.ID, "digest-xyz"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	// Pinning twice must fail — that is what makes the strategy unswappable.
	if err := r.PinProber(ctx, run.ID, "digest-other"); err == nil {
		t.Fatal("a pinned prober was replaced")
	}
	run, _ = r.OpenRun(ctx, "ag_test", spec.Version)
	if run.Phase != ladder.PhaseCertify || run.ProberDigest != "digest-xyz" {
		t.Fatalf("transition not persisted: %+v", run)
	}

	// Payoffs: replaying a batch must be absorbed, not double-counted.
	batch := []ladder.MatchPayoff{{MatchID: "m1", Seq: 1, Payoff: 2.5}, {MatchID: "m2", Seq: 2, Payoff: -1.5}}
	for i := 0; i < 3; i++ {
		if err := r.AddPayoffs(ctx, run.ID, batch); err != nil {
			t.Fatalf("add payoffs: %v", err)
		}
	}
	pay, err := r.Payoffs(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pay) != 2 || pay[0] != 2.5 || pay[1] != -1.5 {
		t.Fatalf("payoffs wrong or out of order: %v", pay)
	}
	run, _ = r.OpenRun(ctx, "ag_test", spec.Version)
	if run.CertifyDone != 2 {
		t.Fatalf("certify_done = %d, want 2 after a replayed batch", run.CertifyDone)
	}

	hash, _ := spec.Hash()
	cert := ladder.Certificate{
		RunID: run.ID, AgentID: "ag_test", SpecVersion: spec.Version, SpecHash: hash,
		ProberDigest: "digest-xyz", Games: 2, MeanPayoff: 0.5, StdDev: 2.8,
		LowerBound: 0.1, Delta: spec.Delta, Informative: true, StopReason: "conclusive",
		FitMatches: 60, KeptFraction: 15.0 / 18.0,
	}
	if err := r.SaveCertificate(ctx, cert); err != nil {
		t.Fatalf("save certificate: %v", err)
	}
	if err := r.SaveCertificate(ctx, cert); err != nil {
		t.Fatalf("re-saving should be a no-op: %v", err)
	}
	// The certified run must be CLOSED, and its certificate stored exactly once.
	var phase string
	if err := db.QueryRow(ctx, `SELECT phase FROM lab_cert_run WHERE id = $1`, run.ID).
		Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != string(ladder.PhaseDone) {
		t.Fatalf("run phase after certification = %q, want done", phase)
	}
	var certs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM lab_certificate WHERE run_id = $1`, run.ID).
		Scan(&certs); err != nil {
		t.Fatal(err)
	}
	if certs != 1 {
		t.Fatalf("%d certificates for run %d, want exactly 1 — re-saving must not duplicate",
			certs, run.ID)
	}
	var stored float64
	if err := db.QueryRow(ctx, `SELECT lower_bound FROM lab_certificate WHERE run_id = $1`, run.ID).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != cert.LowerBound {
		t.Fatalf("stored lower bound %v, want %v", stored, cert.LowerBound)
	}

	// OpenRun is get-or-create, so an agent whose run has closed starts a FRESH one rather
	// than resurrecting the certified one. Re-certification after a model change is a real
	// use case; silently reopening a run that already published would not be.
	next, err := r.OpenRun(ctx, "ag_test", spec.Version)
	if err != nil {
		t.Fatalf("re-certification should start a new run: %v", err)
	}
	if next.ID == run.ID {
		t.Fatal("a closed, certified run was reopened")
	}
	if next.Phase != ladder.PhaseFit || next.FitDone != 0 || next.ProberDigest != "" {
		t.Fatalf("the new run inherited state from the old one: %+v", next)
	}
}
