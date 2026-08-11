package devprofile

import (
	"context"
	"testing"
)

// The landing page's featured developer must be the season's BEST, or nobody.
//
// # The defect
//
// Spotlight used to read the directory's "top" sort and take row one. That sort
// ends `ORDER BY … p_index DESC, matches DESC, created_at DESC`, so a season with
// no ranked play walked all the way down the tiebreakers to created_at and
// featured whoever registered most recently — relabelled "newest developer" and
// presented in the slot that says "leading the arena". On a pre-launch platform
// that is every load, and the face changed every time somebody signed up.
//
// # What this pins
//
// The selection is the leaderboard's, not a sort that happens to agree with it
// most of the time. Same publishedDeveloper gate, same ordering, same population
// — so "top developer" cannot come to mean two different things on two surfaces.
// And an empty board yields NO spotlight rather than a substitute, because a
// featured developer nobody earned is a claim, and a missing section is not.
type spotRepo struct {
	Repo
	board     []LeaderRow
	rows      map[string]DirectoryRow
	askedFor  string
	dirCalled bool
}

func (r *spotRepo) Leaderboard(_ context.Context, _ int, _ string, _, _, _ int) ([]LeaderRow, error) {
	return r.board, nil
}

func (r *spotRepo) DirectoryRowFor(_ context.Context, _ int, id string) (DirectoryRow, bool, error) {
	r.askedFor = id
	row, ok := r.rows[id]
	return row, ok, nil
}

// Directory is the OLD source. It must not be consulted at all — if it is, the
// selection can drift back to a sort that falls through to created_at.
func (r *spotRepo) Directory(_ context.Context, _ int, _, _ string, _, _ int, _ string) ([]DirectoryRow, error) {
	r.dirCalled = true
	return nil, nil
}

func (r *spotRepo) Stats(_ context.Context, _ string, _ int) (Stats, []ArenaStat, error) {
	return Stats{}, nil, nil
}

func (r *spotRepo) TopModel(_ context.Context, _ int) (SeasonModel, bool, error) {
	return SeasonModel{}, false, nil
}

func newSpotService(repo Repo) *Service {
	return &Service{repo: repo, season: func() int { return 7 }}
}

func TestSpotlightFeaturesTheBoardLeaderNotTheNewestSignup(t *testing.T) {
	repo := &spotRepo{
		// The board's own order. Rank 1 is the only candidate.
		board: []LeaderRow{{Developer: "usr_leader", PIndex: 812, GlobalRank: 1}},
		rows: map[string]DirectoryRow{
			"usr_leader": {Developer: "usr_leader", Username: "leader", PIndex: 812, Ranked: true, Matches: 40},
			"usr_newest": {Developer: "usr_newest", Username: "newest"},
		},
	}
	sp, found, err := newSpotService(repo).Spotlight(context.Background(), 0)
	if err != nil || !found {
		t.Fatalf("Spotlight: found=%v err=%v", found, err)
	}
	if sp.Developer.Developer != "usr_leader" {
		t.Errorf("featured %q, want usr_leader — the spotlight must be the board's rank 1",
			sp.Developer.Developer)
	}
	if repo.askedFor != "usr_leader" {
		t.Errorf("looked up %q, want usr_leader: the profile shown must be the developer the "+
			"board ranked, not one resolved by a second, independent sort", repo.askedFor)
	}
	if repo.dirCalled {
		t.Error("Spotlight consulted the directory sort — that is the path whose tiebreakers " +
			"fall through to created_at, which is how the newest signup got featured")
	}
	if sp.Reason != "top_p_index" {
		t.Errorf("reason = %q, want top_p_index — there is no longer a weaker basis to fall back to",
			sp.Reason)
	}
}

// An empty board must yield NOTHING. This is the half that actually regressed:
// the old code always had a row to return, so it always returned one.
func TestSpotlightFeaturesNobodyWhenNoDeveloperHasQualified(t *testing.T) {
	repo := &spotRepo{
		board: nil, // nobody has proven a model call this season
		rows: map[string]DirectoryRow{
			// A registered developer exists. Under the old selection this is
			// exactly who would have been featured.
			"usr_newest": {Developer: "usr_newest", Username: "newest"},
		},
	}
	sp, found, err := newSpotService(repo).Spotlight(context.Background(), 0)
	if err != nil {
		t.Fatalf("Spotlight: %v", err)
	}
	if found {
		t.Errorf("featured %q on an empty board — an unearned featured developer is a claim, "+
			"and the section rendering nothing is not", sp.Developer.Developer)
	}
}

// The season's leading model rides along, and its absence never costs the card.
func TestSpotlightCarriesTheTopModelAndSurvivesItsAbsence(t *testing.T) {
	base := map[string]DirectoryRow{
		"usr_leader": {Developer: "usr_leader", Ranked: true, Matches: 12},
	}

	withModel := &spotModelRepo{
		spotRepo: spotRepo{board: []LeaderRow{{Developer: "usr_leader"}}, rows: base},
		model:    SeasonModel{Provider: "anthropic", Model: "claude-opus-4-20260501", Matches: 8, Wins: 6, WinRate: 0.75, Verified: true},
		ok:       true,
	}
	sp, found, err := newSpotService(withModel).Spotlight(context.Background(), 0)
	if err != nil || !found {
		t.Fatalf("Spotlight: found=%v err=%v", found, err)
	}
	if sp.TopModel == nil {
		t.Fatal("top model missing from the spotlight")
	}
	if sp.TopModel.Model != "claude-opus-4-20260501" {
		t.Errorf("model = %q, want the EXACT id including its version tail — two builds of one "+
			"model are priced and capable differently", sp.TopModel.Model)
	}

	// No verified play yet: the developer card must still render.
	none := &spotModelRepo{
		spotRepo: spotRepo{board: []LeaderRow{{Developer: "usr_leader"}}, rows: base},
		ok:       false,
	}
	sp, found, err = newSpotService(none).Spotlight(context.Background(), 0)
	if err != nil || !found {
		t.Fatalf("Spotlight without a model: found=%v err=%v", found, err)
	}
	if sp.TopModel != nil {
		t.Error("a top model was reported when none qualified")
	}
}

type spotModelRepo struct {
	spotRepo
	model SeasonModel
	ok    bool
}

func (r *spotModelRepo) TopModel(_ context.Context, _ int) (SeasonModel, bool, error) {
	return r.model, r.ok, nil
}
