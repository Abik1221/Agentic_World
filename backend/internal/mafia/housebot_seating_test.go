package mafia

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/rating"
)

// ── fakes for the seating path ────────────────────────────────────────────────

// seatingRepo models a real waiting table: JoinSeat appends a seat and Get reflects it,
// so a test can drive the actual Join → startMatch sequence instead of asserting on a
// hand-built Match. IsHouse is derived from the agent id prefix, mirroring production,
// where it comes from agents.kind rather than from anything the caller passes.
type seatingRepo struct {
	*fakeRepo
	m       Match
	started bool
	unrated []string
	roles   map[int]string
	// The start countdown, captured so a test can prove the first phase window opens when
	// PLAY does rather than when the table filled. See startcountdown_test.go.
	startsAt time.Time
	deadline time.Time
}

func newSeatingRepo(entryFee int64, creator Player) *seatingRepo {
	return &seatingRepo{
		fakeRepo: &fakeRepo{},
		m: Match{
			PublicID: "mf_test", Status: StatusWaiting, EntryFee: entryFee,
			RakePct: 10, Seed: make([]byte, 32), Players: []Player{creator},
		},
	}
}

func (r *seatingRepo) Get(context.Context, string) (Match, error) { return r.m, nil }

func (r *seatingRepo) JoinSeat(_ context.Context, _ string, p Player) error {
	// Production reads agents.kind; the seeded fillers are all named ag_house_*.
	p.IsHouse = strings.HasPrefix(p.AgentPublicID, "ag_house_")
	r.m.Players = append(r.m.Players, p)
	return nil
}

func (r *seatingRepo) Start(_ context.Context, _ string, roles map[int]string, st mf.State, startsAt, deadline time.Time, _ []mf.Event) error {
	r.started, r.roles = true, roles
	r.startsAt, r.deadline = startsAt, deadline
	r.m.Status, r.m.State = StatusActive, st
	return nil
}

func (r *seatingRepo) MarkUnrated(_ context.Context, id string) error {
	r.unrated = append(r.unrated, id)
	return nil
}

// recordingWallet captures exactly which agents were staked and settled.
type recordingWallet struct {
	staked      []string
	stakeFee    int64
	settled     map[string]int64
	platformFee int64
	stakeErr    error
}

func (w *recordingWallet) StakeTable(_ context.Context, _ string, agents []string, fee int64) error {
	if w.stakeErr != nil {
		return w.stakeErr
	}
	w.staked, w.stakeFee = append([]string(nil), agents...), fee
	return nil
}
func (w *recordingWallet) SettleTable(_ context.Context, _ string, platformFee int64, payouts map[string]int64) error {
	w.settled, w.platformFee = payouts, platformFee
	return nil
}
func (w *recordingWallet) RefundTable(context.Context, string) error { return nil }

// denyAll fails every spending-limit and certification check, so a test can prove which
// join paths consult them.
type denyAll struct{}

func (denyAll) CheckJoin(context.Context, string, int64) error {
	return errors.New("insufficient balance")
}
func (denyAll) CheckEligible(context.Context, string) error {
	return errors.New("not certified")
}

// recordingRater captures whether a finished table was rated at all.
type recordingRater struct{ calls []rating.MatchResult }

func (r *recordingRater) Rate(_ context.Context, res rating.MatchResult) error {
	r.calls = append(r.calls, res)
	return nil
}

// houseIDs returns n seeded-looking house bot ids, all under the one system owner.
func houseIDs(n int) []BotAgent {
	out := make([]BotAgent, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, BotAgent{
			PublicID:      "ag_house_mafia_" + string(rune('0'+i/10)) + string(rune('0'+i%10)),
			OwnerPublicID: "usr_system",
		})
	}
	return out
}

// newSeatingSvc wires a service that can seat house bots (the allowlist is installed by
// EnablePushPlay, which is also how production wires it).
func newSeatingSvc(repo Repo, wallet Wallet, limits Limits, ver Verifier, bots []BotAgent) *Service {
	svc := NewService(repo, fakeLock{}, limits, wallet, fakeBcast{}, ver, nil,
		fakeClock{t: time.Unix(1_700_000_000, 0)}, Config{})
	svc.EnablePushPlay(nil, nil, bots, nil)
	return svc
}

// ── the bug this whole path existed to fix ────────────────────────────────────

// Every house bot shares the single usr_system owner, and Join's anti-collusion rule
// rejects a second seat from one owner. Filling a Mafia roster therefore failed on the
// SECOND bot with ErrSameOwner — sandbox push-play was returning a 409 in production.
// JoinHouseSeat must seat all eleven and start the match.
func TestJoinHouseSeatFillsRosterDespiteOneSharedOwner(t *testing.T) {
	bots := houseIDs(mf.RosterSize - 1)
	repo := newSeatingRepo(0, Player{AgentPublicID: "ag_dev", OwnerPublicID: "usr_dev", Seat: 1})
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, bots)

	for i, b := range bots {
		if _, err := svc.JoinHouseSeat(context.Background(), b.PublicID, b.OwnerPublicID, "mf_test"); err != nil {
			t.Fatalf("house bot %d/%d (%s) failed to seat: %v", i+1, len(bots), b.PublicID, err)
		}
	}
	if len(repo.m.Players) != mf.RosterSize {
		t.Fatalf("roster = %d seats, want %d", len(repo.m.Players), mf.RosterSize)
	}
	if !repo.started {
		t.Fatal("the final house join should have started the match")
	}
	if len(repo.roles) != mf.RosterSize {
		t.Fatalf("every seat should be dealt a role, got %d", len(repo.roles))
	}
}

// The anti-collusion rule itself must be UNCHANGED for real developers: the exemption is
// keyed on the server-side call path, not on anything a request can influence.
func TestSameOwnerStillRejectedForRealAgents(t *testing.T) {
	repo := newSeatingRepo(0, Player{AgentPublicID: "ag_dev_1", OwnerPublicID: "usr_dev", Seat: 1})
	svc := newSeatingSvc(repo, &recordingWallet{}, nil, nil, houseIDs(11))

	_, err := svc.Join(context.Background(), "ag_dev_2", "usr_dev", "mf_test")
	if err != ErrSameOwner {
		t.Fatalf("a second agent of the same owner must be rejected, got %v", err)
	}
	if len(repo.m.Players) != 1 {
		t.Fatalf("no seat should have been taken, roster = %d", len(repo.m.Players))
	}
}

// JoinHouseSeat bypasses the spending-limit and certification gates, so it must refuse
// any agent that is not on the declared house allowlist — otherwise one mistaken call
// would seat a real agent free of every check.
func TestJoinHouseSeatRefusesNonHouseAgent(t *testing.T) {
	repo := newSeatingRepo(500, Player{AgentPublicID: "ag_dev", OwnerPublicID: "usr_dev", Seat: 1})
	svc := newSeatingSvc(repo, &recordingWallet{}, denyAll{}, denyAll{}, houseIDs(11))

	if _, err := svc.JoinHouseSeat(context.Background(), "ag_someone_real", "usr_attacker", "mf_test"); err != ErrNotHouseAgent {
		t.Fatalf("a non-allowlisted agent must be refused, got %v", err)
	}
	if len(repo.m.Players) != 1 {
		t.Fatalf("no seat should have been taken, roster = %d", len(repo.m.Players))
	}
}

// A house bot holds a zero-balance wallet by construction, so it must skip the
// affordability and certification gates that a real agent still faces on the same table.
func TestHouseSeatSkipsGatesThatStillBindRealAgents(t *testing.T) {
	bots := houseIDs(11)
	repo := newSeatingRepo(500, Player{AgentPublicID: "ag_dev", OwnerPublicID: "usr_dev", Seat: 1})
	svc := newSeatingSvc(repo, &recordingWallet{}, denyAll{}, denyAll{}, bots)

	// A real agent is rejected by the gates on this paid table...
	if _, err := svc.Join(context.Background(), "ag_dev_2", "usr_other", "mf_test"); err == nil {
		t.Fatal("a real agent that fails the spending-limit gate must not be seated")
	}
	// ...while a house filler seats regardless.
	if _, err := svc.JoinHouseSeat(context.Background(), bots[0].PublicID, bots[0].OwnerPublicID, "mf_test"); err != nil {
		t.Fatalf("house seat should skip the affordability/certification gates: %v", err)
	}
}

// ── staking, the unrated flag, and rating ─────────────────────────────────────

// Only real agents stake. Handing a house bot to StakeTable would fail the start on
// insufficient funds or overdraw a system wallet, and would compute a pool larger than
// the escrow backing it.
func TestStakeTableReceivesHumanSeatsOnly(t *testing.T) {
	bots := houseIDs(11)
	wallet := &recordingWallet{}
	repo := newSeatingRepo(500, Player{AgentPublicID: "ag_dev_1", OwnerPublicID: "usr_dev_1", Seat: 1})
	svc := newSeatingSvc(repo, wallet, nil, nil, bots)
	ctx := context.Background()

	// Three more real agents (four humans total), then bots fill to twelve.
	for _, id := range []string{"2", "3", "4"} {
		if _, err := svc.Join(ctx, "ag_dev_"+id, "usr_dev_"+id, "mf_test"); err != nil {
			t.Fatalf("real join %s: %v", id, err)
		}
	}
	for _, b := range bots[:mf.RosterSize-4] {
		if _, err := svc.JoinHouseSeat(ctx, b.PublicID, b.OwnerPublicID, "mf_test"); err != nil {
			t.Fatalf("house join %s: %v", b.PublicID, err)
		}
	}

	if !repo.started {
		t.Fatal("the twelfth seat should have started the match")
	}
	if len(wallet.staked) != 4 {
		t.Fatalf("staked %d agents (%v), want exactly the 4 humans", len(wallet.staked), wallet.staked)
	}
	for _, id := range wallet.staked {
		if strings.HasPrefix(id, "ag_house_") {
			t.Errorf("house bot %s must never be staked", id)
		}
	}
	if wallet.stakeFee != 500 {
		t.Errorf("stake fee = %d, want 500", wallet.stakeFee)
	}
	// The table is bot-filled, so it must be recorded as excluded from ranked stats.
	if len(repo.unrated) != 1 || repo.unrated[0] != "mf_test" {
		t.Errorf("bot-filled table should be marked unrated exactly once, got %v", repo.unrated)
	}
}

// A table that filled with twelve REAL agents stays rated and stakes everybody.
func TestAllHumanTableStaysRatedAndStakesEveryone(t *testing.T) {
	wallet := &recordingWallet{}
	repo := newSeatingRepo(500, Player{AgentPublicID: "ag_dev_01", OwnerPublicID: "usr_dev_01", Seat: 1})
	svc := newSeatingSvc(repo, wallet, nil, nil, houseIDs(11))
	ctx := context.Background()

	for i := 2; i <= mf.RosterSize; i++ {
		id := "ag_dev_" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		if _, err := svc.Join(ctx, id, "usr_"+id, "mf_test"); err != nil {
			t.Fatalf("join %s: %v", id, err)
		}
	}
	if !repo.started {
		t.Fatal("the twelfth real seat should have started the match")
	}
	if len(wallet.staked) != mf.RosterSize {
		t.Fatalf("staked %d, want all %d real seats", len(wallet.staked), mf.RosterSize)
	}
	if len(repo.unrated) != 0 {
		t.Errorf("an all-human table must NOT be marked unrated, got %v", repo.unrated)
	}
}

// A staked bot-filled table settles normally for its humans but is never rated — that is
// what keeps it out of P-Index, which reads match_rating_changes.
func TestBotFilledTableSettlesButIsNotRated(t *testing.T) {
	wallet := &recordingWallet{}
	rater := &recordingRater{}
	repo := newSeatingRepo(100, Player{AgentPublicID: "ag_dev_1", OwnerPublicID: "usr_dev_1", Seat: 1})
	svc := newSeatingSvc(repo, wallet, nil, nil, houseIDs(11))
	svc.SetRater(rater)

	// Two humans, two bots — a four-seat table is enough to exercise finalize.
	m := repo.m
	m.Players = []Player{
		{AgentPublicID: "ag_dev_1", OwnerPublicID: "usr_dev_1", Seat: 1},
		{AgentPublicID: "ag_dev_2", OwnerPublicID: "usr_dev_2", Seat: 2},
		{AgentPublicID: "ag_house_mafia_01", OwnerPublicID: "usr_system", Seat: 3, IsHouse: true},
		{AgentPublicID: "ag_house_mafia_02", OwnerPublicID: "usr_system", Seat: 4, IsHouse: true},
	}
	st := mf.State{
		Finished: true, Winner: mf.TeamTown,
		Alive: map[int]bool{1: true, 2: false, 3: true, 4: false},
		Roles: map[int]string{1: mf.RoleVillager, 2: mf.RoleVillager, 3: mf.RoleVillager, 4: mf.RoleMafia},
	}
	if err := svc.finalize(context.Background(), m, st, nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if len(rater.calls) != 0 {
		t.Errorf("a bot-filled table must not be rated, got %d rating calls", len(rater.calls))
	}
	// Pool is 2 humans x 100 = 200, rake 10% ⇒ 180 to the single surviving human winner.
	if got := wallet.settled["ag_dev_1"]; got != 180 {
		t.Errorf("surviving human winner payout = %d, want 180 (pool from 2 stakers, not 4 seats)", got)
	}
	if _, paid := wallet.settled["ag_house_mafia_01"]; paid {
		t.Error("a surviving house bot on the winning team must not be paid")
	}
	if wallet.platformFee != 20 {
		t.Errorf("platform fee = %d, want 20 (10%% of the 2-staker pool)", wallet.platformFee)
	}
	var total int64
	for _, v := range wallet.settled {
		total += v
	}
	if total+wallet.platformFee > 200 {
		t.Errorf("settled %d + fee %d exceeds the 200 coins escrowed", total, wallet.platformFee)
	}
}

// The same table with no bots IS rated, so the skip above is caused by the fillers and
// not by something incidental to the fixture.
func TestAllHumanTableIsRated(t *testing.T) {
	wallet := &recordingWallet{}
	rater := &recordingRater{}
	repo := newSeatingRepo(100, Player{AgentPublicID: "ag_dev_1", OwnerPublicID: "usr_dev_1", Seat: 1})
	svc := newSeatingSvc(repo, wallet, nil, nil, houseIDs(11))
	svc.SetRater(rater)

	m := repo.m
	m.Players = []Player{
		{AgentPublicID: "ag_dev_1", OwnerPublicID: "usr_dev_1", Seat: 1},
		{AgentPublicID: "ag_dev_2", OwnerPublicID: "usr_dev_2", Seat: 2},
	}
	st := mf.State{
		Finished: true, Winner: mf.TeamTown,
		Alive: map[int]bool{1: true, 2: false},
		Roles: map[int]string{1: mf.RoleVillager, 2: mf.RoleMafia},
	}
	if err := svc.finalize(context.Background(), m, st, nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if len(rater.calls) != 1 {
		t.Fatalf("an all-human paid table must be rated exactly once, got %d", len(rater.calls))
	}
	if n := len(rater.calls[0].Players); n != 2 {
		t.Errorf("rating should carry both seats, got %d", n)
	}
}

// A house filler's coins_delta must be 0, not -entryFee: it never paid in, and writing a
// fabricated loss would surface as fact in the spectator economy and lifetime-winnings
// views.
func TestHouseSeatRecordsZeroCoinsDelta(t *testing.T) {
	players := []Player{
		{AgentPublicID: "ag_dev_1", Seat: 1},
		{AgentPublicID: "ag_house_mafia_01", Seat: 2, IsHouse: true},
	}
	st := mf.State{
		Winner: mf.TeamTown,
		Alive:  map[int]bool{1: true, 2: true},
		Roles:  map[int]string{1: mf.RoleVillager, 2: mf.RoleVillager},
	}
	econ := ComputeEconomy(1, 100, 0)
	rewards := ComputeRewards(mf.TeamTown, seatsFromPlayers(players, st), econ)

	out := finalizePlayers(players, st, rewards, 100)
	for _, p := range out {
		if p.IsHouse && p.CoinsDelta != 0 {
			t.Errorf("house seat %d coins_delta = %d, want 0", p.Seat, p.CoinsDelta)
		}
		if !p.IsHouse && p.CoinsDelta != 0 { // staked 100, won the whole 100 pool
			t.Errorf("real winner coins_delta = %d, want 0 (100 back from a 100 stake)", p.CoinsDelta)
		}
	}
}
