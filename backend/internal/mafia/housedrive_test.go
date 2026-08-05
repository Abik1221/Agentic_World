package mafia

import (
	"context"
	"sync"
	"testing"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// driveRepo serves one live match to the house driver and records the engine writes the
// driver's actions produce. Concurrency-safe: the driver runs on its own goroutine while
// the test reads counters.
type driveRepo struct {
	*fakeRepo
	mu       sync.Mutex
	m        Match
	advances int
	finished bool
}

func (r *driveRepo) Get(context.Context, string) (Match, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m, nil
}

func (r *driveRepo) Advance(_ context.Context, _ string, st mf.State, dl *time.Time, _ map[int]bool, _ []mf.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advances++
	r.m.State = st // let the match actually progress, as the real repo would
	r.m.RoundDeadline = dl
	return nil
}

func (r *driveRepo) Finish(_ context.Context, _ string, st mf.State, _, _ string, _ []Player, _ []mf.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished, r.m.State, r.m.Status = true, st, StatusFinished
	return nil
}

func (r *driveRepo) advanceCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.advances
}

func (r *driveRepo) setStatus(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m.Status = s
}

// newDriveFixture builds a live 12-seat table: seat 1 a real agent, seats 2..12 house
// bots, with a genuine engine state so the driver's moves are validated for real.
func newDriveFixture(t *testing.T, interval time.Duration) (*Service, *driveRepo, []string) {
	t.Helper()
	bots := houseIDs(mf.RosterSize - 1)

	players := []Player{{AgentPublicID: "ag_dev", OwnerPublicID: "usr_dev", Seat: 1}}
	botIDs := make([]string, 0, len(bots))
	for i, b := range bots {
		players = append(players, Player{
			AgentPublicID: b.PublicID, OwnerPublicID: b.OwnerPublicID, Seat: i + 2, IsHouse: true,
		})
		botIDs = append(botIDs, b.PublicID)
	}

	repo := &driveRepo{fakeRepo: &fakeRepo{}}
	svc := NewService(repo, fakeLock{}, nil, &recordingWallet{}, fakeBcast{}, nil, nil,
		fakeClock{t: time.Unix(1_700_000_000, 0)},
		Config{HouseDriveInterval: interval})
	svc.EnablePushPlay(nil, nil, bots, nil)

	seats := make([]int, 0, mf.RosterSize)
	for i := 1; i <= mf.RosterSize; i++ {
		seats = append(seats, i)
	}
	state, _ := svc.eng.Init(make([]byte, 32), seats)
	deadline := time.Unix(1_700_009_999, 0)

	repo.m = Match{
		PublicID: "mf_drive", Status: StatusActive, EntryFee: 0, RakePct: 10,
		Seed: make([]byte, 32), Players: players, State: state, RoundDeadline: &deadline,
	}
	return svc, repo, botIDs
}

// The driver must actually move the game. Without it, house seats sit silent until each
// phase's deadline expires and ForceTimeout resolves it — a bot-filled table that runs at
// the pace of its own timeouts and never votes, speaks, or kills.
func TestDriveHouseSeatsAdvancesTheMatch(t *testing.T) {
	svc, repo, botIDs := newDriveFixture(t, time.Millisecond)

	done := make(chan struct{})
	go func() {
		svc.driveHouseSeats("mf_drive", botIDs)
		close(done)
	}()

	// The night phase needs the mafia/special seats to act; the bots hold eleven of the
	// twelve seats, so their submissions must drive at least one engine write.
	deadline := time.After(5 * time.Second)
	for repo.advanceCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("the house driver produced no engine writes — bot seats never acted")
		case <-done:
			t.Fatal("the driver exited before advancing an active match")
		case <-time.After(2 * time.Millisecond):
		}
	}

	// It must also STOP once the table is no longer active, rather than spinning for the
	// full two-hour backstop against a settled match.
	repo.setStatus(StatusFinished)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the driver did not exit after the match left the active state")
	}
}

// An empty filler list means the table filled with real agents: DriveHouseSeats must be a
// no-op rather than starting a goroutine that polls a table it has no seats at.
func TestDriveHouseSeatsIgnoresEmptySeatList(t *testing.T) {
	svc, repo, _ := newDriveFixture(t, time.Millisecond)

	svc.DriveHouseSeats("mf_drive", nil)
	time.Sleep(20 * time.Millisecond) // long enough for ~20 passes had one started

	if n := repo.advanceCount(); n != 0 {
		t.Fatalf("no filler seats means nothing to drive, got %d engine writes", n)
	}
}

// DriveHouseSeats copies the caller's slice: main.go builds it in a loop and must be free
// to reuse or mutate the backing array afterwards.
func TestDriveHouseSeatsCopiesSeatList(t *testing.T) {
	svc, repo, botIDs := newDriveFixture(t, time.Millisecond)

	ids := append([]string(nil), botIDs...)
	svc.DriveHouseSeats("mf_drive", ids)
	for i := range ids {
		ids[i] = "ag_not_a_house_bot" // would be refused by Act if the driver aliased it
	}

	deadline := time.After(5 * time.Second)
	for repo.advanceCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("driver stopped working after the caller mutated its slice — it aliased the input")
		case <-time.After(2 * time.Millisecond):
		}
	}
	repo.setStatus(StatusFinished)
}
