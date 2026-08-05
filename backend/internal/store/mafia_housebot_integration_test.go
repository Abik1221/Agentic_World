package store

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/mafia"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These cover the two pieces of house-bot SQL that unit tests can only fake:
//
//  1. loadPlayers derives Player.IsHouse from agents.kind. Every money and rating decision
//     keys on that flag, so if the column stops being selected — or the join makes it NULL —
//     a bot-filled table silently becomes a fully staked, fully rated one.
//  2. MarkUnrated writes matches.rated = false (migration 0076), which is what keeps such a
//     table off the model board.
//
// Skipped unless a Postgres DSN is set; the harness migrates the schema itself.

// mkHouseAgent inserts a kind='house' agent owned by the system user, mirroring the
// fillers migration 0063 seeds.
func mkHouseAgent(t *testing.T, pool *pgxpool.Pool, suffix string) (agentPub string, ownerID int64) {
	t.Helper()
	ctx := context.Background()
	ownerPub := "u_sys_" + suffix
	agentPub = "ag_house_" + suffix
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id) VALUES ($1)
		 ON CONFLICT (public_id) DO UPDATE SET updated_at = now() RETURNING id`, ownerPub).Scan(&ownerID); err != nil {
		t.Fatalf("insert system user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, kind) VALUES ($1, $2, $3, $4, 'house')
		 ON CONFLICT (public_id) DO UPDATE SET kind = 'house'`,
		agentPub, ownerID, "House "+suffix, "house-"+suffix); err != nil {
		t.Fatalf("insert house agent: %v", err)
	}
	return agentPub, ownerID
}

// A seat held by a kind='house' agent must come back with IsHouse set, and a seat held by
// an ordinary agent must not. HumanPlayers/HasHouseSeat — and therefore staking, payouts,
// and rating — are only correct if this hydration is.
func TestMafiaLoadPlayersHydratesIsHouseFromAgentKind(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewMafiaRepo(pool)

	run := time.Now().Format("150405.000")
	realAg, realOw := mkUserAgent(t, pool, "mh-real-"+run)
	houseAg, _ := mkHouseAgent(t, pool, "mh-bot-"+run)

	// A real waiting table created through the repo, then a house seat joined onto it.
	matchPub, err := repo.CreateWaiting(ctx, mafia.CreateMatchInput{
		PublicID: "mf_house_" + run, Title: "Mafia test", EntryFee: 100, RakePct: 10,
		Seed: make([]byte, 32), Commit: "test",
		Creator: mafia.Player{AgentPublicID: realAg, OwnerPublicID: realOw, Seat: 1},
	})
	if err != nil {
		t.Fatalf("CreateWaiting: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM matches WHERE public_id = $1`, matchPub.PublicID)
	})

	var houseOwnerPub string
	if err := pool.QueryRow(ctx,
		`SELECT u.public_id FROM users u JOIN agents a ON a.owner_user_id = u.id WHERE a.public_id = $1`,
		houseAg).Scan(&houseOwnerPub); err != nil {
		t.Fatalf("resolve house owner: %v", err)
	}
	if err := repo.JoinSeat(ctx, matchPub.PublicID, mafia.Player{
		AgentPublicID: houseAg, OwnerPublicID: houseOwnerPub, Seat: 2,
	}); err != nil {
		t.Fatalf("JoinSeat(house): %v", err)
	}

	m, err := repo.Get(ctx, matchPub.PublicID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(m.Players) != 2 {
		t.Fatalf("roster = %d seats, want 2", len(m.Players))
	}
	bySeat := map[int]mafia.Player{}
	for _, p := range m.Players {
		bySeat[p.Seat] = p
	}
	if bySeat[1].IsHouse {
		t.Errorf("seat 1 is an ordinary agent and must NOT be flagged house")
	}
	if !bySeat[2].IsHouse {
		t.Errorf("seat 2 is a kind='house' agent and MUST be flagged house")
	}

	// The derived helpers are what the money path actually calls.
	if got := len(mafia.HumanPlayers(m.Players)); got != 1 {
		t.Errorf("HumanPlayers = %d, want 1 (only the real agent stakes)", got)
	}
	if !mafia.HasHouseSeat(m.Players) {
		t.Error("HasHouseSeat should be true — this table would otherwise be rated")
	}
}

// MarkUnrated must flip matches.rated to false for a live mafia table, and must not touch
// a finished one (it is only ever applied at start time).
func TestMafiaMarkUnratedIntegration(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewMafiaRepo(pool)

	run := time.Now().Format("150405.000")
	_, ownerPub := mkUserAgent(t, pool, "mu-"+run)
	var ownerID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, ownerPub).Scan(&ownerID); err != nil {
		t.Fatalf("owner id: %v", err)
	}

	live := "mf_unrated_live_" + run
	done := "mf_unrated_done_" + run
	mkWaitingMatch(t, pool, live, "mafia", ownerID, time.Now())
	mkWaitingMatch(t, pool, done, "mafia", ownerID, time.Now())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM matches WHERE public_id = ANY($1)`, []string{live, done})
	})

	// Every match starts rated — that is the default that keeps historical rows meaningful.
	if r := matchRated(t, pool, live); !r {
		t.Fatalf("a new match should default to rated=true, got %v", r)
	}

	if err := repo.MarkUnrated(ctx, live); err != nil {
		t.Fatalf("MarkUnrated: %v", err)
	}
	if r := matchRated(t, pool, live); r {
		t.Error("MarkUnrated should have set rated = false")
	}

	// Idempotent: the start path may retry, and a second call must not error.
	if err := repo.MarkUnrated(ctx, live); err != nil {
		t.Fatalf("MarkUnrated (second call): %v", err)
	}

	// A finished table is out of scope: rating already happened (or was skipped) and
	// rewriting the flag afterwards would let a settled result be reclassified.
	if _, err := pool.Exec(ctx, `UPDATE matches SET status = 'finished', finished_at = now() WHERE public_id = $1`, done); err != nil {
		t.Fatalf("finish match: %v", err)
	}
	if err := repo.MarkUnrated(ctx, done); err != nil {
		t.Fatalf("MarkUnrated(finished): %v", err)
	}
	if r := matchRated(t, pool, done); !r {
		t.Error("a finished match must keep its rated flag — MarkUnrated is start-time only")
	}
}

func matchRated(t *testing.T, pool *pgxpool.Pool, pub string) bool {
	t.Helper()
	var rated bool
	if err := pool.QueryRow(context.Background(), `SELECT rated FROM matches WHERE public_id = $1`, pub).Scan(&rated); err != nil {
		t.Fatalf("read rated for %s: %v", pub, err)
	}
	return rated
}
