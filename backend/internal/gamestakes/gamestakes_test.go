package gamestakes_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/gamestakes"
	"github.com/agent-arena/arena/internal/platform"
)

type fakeRepo struct {
	tiers map[string][]gamestakes.Tier
}

func (r *fakeRepo) ListTiers(_ context.Context, game string) ([]gamestakes.Tier, error) {
	return r.tiers[game], nil
}
func (r *fakeRepo) ReplaceTiers(_ context.Context, game string, tiers []gamestakes.Tier) error {
	if r.tiers == nil {
		r.tiers = map[string][]gamestakes.Tier{}
	}
	r.tiers[game] = tiers
	return nil
}
func (r *fakeRepo) Audit(context.Context, string, string, string, []byte) error { return nil }

func newSvc(r *fakeRepo) *gamestakes.Service {
	return gamestakes.New(r, platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func mafiaTiers() []gamestakes.Tier {
	return []gamestakes.Tier{
		{Key: "low", Label: "Low", Coins: 100, Ordering: 0, Enabled: true},
		{Key: "mid", Label: "Mid", Coins: 500, Ordering: 1, Enabled: true},
		{Key: "high", Label: "High", Coins: 2000, Ordering: 2, Enabled: false}, // admin disabled High
	}
}

func TestResolve(t *testing.T) {
	svc := newSvc(&fakeRepo{tiers: map[string][]gamestakes.Tier{"mafia": mafiaTiers()}})
	ctx := context.Background()

	if c, err := svc.Resolve(ctx, "mafia", "mid"); err != nil || c != 500 {
		t.Fatalf("mid = %d,%v; want 500,nil", c, err)
	}
	if _, err := svc.Resolve(ctx, "mafia", "high"); err != gamestakes.ErrTierDisabled {
		t.Fatalf("disabled tier = %v; want ErrTierDisabled", err)
	}
	if _, err := svc.Resolve(ctx, "mafia", "nope"); err != gamestakes.ErrUnknownTier {
		t.Fatalf("unknown tier = %v; want ErrUnknownTier", err)
	}
	if _, err := svc.Resolve(ctx, "goofspiel", "low"); err != gamestakes.ErrGameNoTiers {
		t.Fatalf("no-tier game = %v; want ErrGameNoTiers", err)
	}
}

func TestListAndHasTiersOnlyEnabled(t *testing.T) {
	svc := newSvc(&fakeRepo{tiers: map[string][]gamestakes.Tier{"mafia": mafiaTiers()}})
	ctx := context.Background()

	list, _ := svc.List(ctx, "mafia")
	if len(list) != 2 { // low + mid enabled, high disabled
		t.Fatalf("enabled tiers = %d; want 2", len(list))
	}
	if has, _ := svc.HasTiers(ctx, "mafia"); !has {
		t.Fatal("mafia should have enabled tiers")
	}
	if has, _ := svc.HasTiers(ctx, "goofspiel"); has {
		t.Fatal("goofspiel should have no tiers")
	}
}

func TestResolveStakePolicy(t *testing.T) {
	svc := newSvc(&fakeRepo{tiers: map[string][]gamestakes.Tier{"mafia": mafiaTiers()}})
	ctx := context.Background()

	// tier chosen → tier's coins.
	if c, err := svc.ResolveStake(ctx, "mafia", "mid", 0); err != nil || c != 500 {
		t.Fatalf("tier mid = %d,%v; want 500,nil", c, err)
	}
	// no tier + free-form fee on a game WITH tiers → rejected.
	if _, err := svc.ResolveStake(ctx, "mafia", "", 250); err != gamestakes.ErrTierRequired {
		t.Fatalf("free-form on tiered game = %v; want ErrTierRequired", err)
	}
	// no tier + free-form fee on a game WITHOUT tiers → back-compat passthrough.
	if c, err := svc.ResolveStake(ctx, "goofspiel", "", 250); err != nil || c != 250 {
		t.Fatalf("free-form on untiered game = %d,%v; want 250,nil", c, err)
	}
	// no tier + no fee → practice (0), regardless of game tiers.
	if c, err := svc.ResolveStake(ctx, "mafia", "", 0); err != nil || c != 0 {
		t.Fatalf("practice = %d,%v; want 0,nil", c, err)
	}
	// disabled tier → rejected.
	if _, err := svc.ResolveStake(ctx, "mafia", "high", 0); err != gamestakes.ErrTierDisabled {
		t.Fatalf("disabled tier = %v; want ErrTierDisabled", err)
	}
}

func TestAdminPutValidation(t *testing.T) {
	svc := newSvc(&fakeRepo{})
	ctx := context.Background()

	bad := map[string][]gamestakes.Tier{
		"non-positive coins":      {{Key: "low", Coins: 0}},
		"duplicate key":           {{Key: "low", Coins: 100}, {Key: "low", Coins: 200}},
		"empty key":               {{Key: "", Coins: 100}},
		"not strictly-increasing": {{Key: "low", Coins: 500, Ordering: 0}, {Key: "mid", Coins: 500, Ordering: 1}},
	}
	for name, tiers := range bad {
		if err := svc.AdminPut(ctx, "admin", "mafia", tiers); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}

	// $5 / $20 / $50 at the default 1¢ peg. These were $1 / $5 / $20 before the
	// paid-tier floor existed; the case is about ordering and keys, not pricing, so
	// the amounts move up to stay valid without changing what is under test.
	good := []gamestakes.Tier{
		{Key: "high", Coins: 5000, Ordering: 2, Enabled: true},
		{Key: "low", Coins: 500, Ordering: 0, Enabled: true},
		{Key: "mid", Coins: 2000, Ordering: 1, Enabled: true},
	}
	if err := svc.AdminPut(ctx, "admin", "mafia", good); err != nil {
		t.Fatalf("valid put: %v", err)
	}
	// Reads reflect the write (cache busted), ordered, and Resolve works.
	if c, err := svc.Resolve(ctx, "mafia", "high"); err != nil || c != 5000 {
		t.Fatalf("resolve after put = %d,%v; want 5000,nil", c, err)
	}
	got, _ := svc.AdminGet(ctx, "mafia")
	if len(got.Tiers) != 3 || got.Tiers[0].Key != "low" {
		t.Fatalf("admin get after put = %+v; want 3 tiers ordered low-first", got.Tiers)
	}
}
