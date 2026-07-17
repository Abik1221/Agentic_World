package rating

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// rollFakeRepo records RollSeason calls and serves a champion per season.
type rollFakeRepo struct {
	lastRolled int
	champions  map[int]string // season -> champion agent id
	rolled     []int          // seasons passed to RollSeason, in order
	champSeen  map[int]string // season -> champion passed to RollSeason
	models     []ModelStat    // canned ModelBenchmark result
	standing   *Standing      // canned AgentStanding result (nil = not found)
}

func newRollFakeRepo(lastRolled int) *rollFakeRepo {
	return &rollFakeRepo{lastRolled: lastRolled, champions: map[int]string{}, champSeen: map[int]string{}}
}

func (f *rollFakeRepo) ApplyMatch(context.Context, ApplyInput) (bool, error)       { return false, nil }
func (f *rollFakeRepo) AgentElo(context.Context, string, string, int) (int, error) { return 1500, nil }
func (f *rollFakeRepo) LastRolledSeason(context.Context) (int, error)              { return f.lastRolled, nil }
func (f *rollFakeRepo) Leaderboard(_ context.Context, _ string, season, _, _ int) ([]LeaderRow, error) {
	if champ, ok := f.champions[season]; ok {
		return []LeaderRow{{AgentPublicID: champ}}, nil
	}
	return nil, nil
}
func (f *rollFakeRepo) RollSeason(_ context.Context, season int, champion string) (bool, error) {
	f.rolled = append(f.rolled, season)
	f.champSeen[season] = champion
	return true, nil
}
func (f *rollFakeRepo) ModelBenchmark(context.Context, int, string, int) ([]ModelStat, error) {
	return f.models, nil
}
func (f *rollFakeRepo) AgentStanding(context.Context, int, string, string) (Standing, bool, error) {
	if f.standing == nil {
		return Standing{}, false, nil
	}
	return *f.standing, true, nil
}

// svcAtSeason builds a Service whose current season is `season` (30-day windows).
func svcAtSeason(repo Repo, season int) *Service {
	length := 30 * 24 * time.Hour
	now := seasonEpoch.Add(time.Duration(season)*length + time.Hour) // just into `season`
	return New(repo, platform.FixedClock{T: now}, Config{SeasonLength: length}, prometheus.NewRegistry())
}

func TestRollCompleted_RollsPriorSeasonsWithChampions(t *testing.T) {
	repo := newRollFakeRepo(-1)     // nothing rolled yet
	repo.champions[0] = "ag_champ0" // season 0 had a winner
	repo.champions[2] = "ag_champ2" // season 2 had a winner; season 1 had none
	svc := svcAtSeason(repo, 3)     // current season = 3 → roll 0,1,2

	if err := svc.RollCompleted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.rolled) != 3 || repo.rolled[0] != 0 || repo.rolled[1] != 1 || repo.rolled[2] != 2 {
		t.Fatalf("expected to roll seasons [0 1 2], got %v", repo.rolled)
	}
	if repo.champSeen[0] != "ag_champ0" || repo.champSeen[2] != "ag_champ2" {
		t.Fatalf("wrong champions: %v", repo.champSeen)
	}
	if repo.champSeen[1] != "" {
		t.Fatalf("season 1 had no matches → champion must be empty, got %q", repo.champSeen[1])
	}
}

func TestRollCompleted_NothingToRoll(t *testing.T) {
	repo := newRollFakeRepo(2) // seasons 0..2 already rolled
	svc := svcAtSeason(repo, 3)
	if err := svc.RollCompleted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.rolled) != 0 {
		t.Fatalf("expected no rolls, got %v", repo.rolled)
	}
}

func TestCurrentSeasonInfo_Bounds(t *testing.T) {
	repo := newRollFakeRepo(-1)
	svc := svcAtSeason(repo, 5)
	info := svc.CurrentSeasonInfo()
	if info.Season != 5 {
		t.Fatalf("expected season 5, got %d", info.Season)
	}
	if !info.EndsAt.After(info.StartsAt) || !info.Now.Before(info.EndsAt) {
		t.Fatalf("bad bounds: start=%v now=%v end=%v", info.StartsAt, info.Now, info.EndsAt)
	}
}
