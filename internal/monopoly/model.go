package monopoly

import (
	"context"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

const (
	StatusActive   = "active"
	StatusFinished = "finished"
	StatusAborted  = "aborted"
	GameName       = "monopoly"

	DefaultEntryFee       int64 = 0 // practice tables by default; stake >0 for pooled play
	DefaultPlatformFeePct int   = 10
	DefaultPlayers        int   = 4
	MinPlayers            int   = 2
	MaxPlayers            int   = 8
)

// Player is one REAL (agent-controlled) seat. Bot seats are NOT persisted as
// players; the service derives them as the seats in [0,Players) that no agent
// holds. This lets a single agent play a full table against server bots without
// seeding synthetic agent rows.
type Player struct {
	AgentPublicID string
	OwnerPublicID string
	Seat          int
	CoinsDelta    int64
}

// Match is the Monopoly aggregate. Players is the total number of seats
// (agents + bots); Agents holds only the real, agent-controlled seats.
type Match struct {
	PublicID      string
	Title         string
	Status        string
	EntryFee      int64
	RakePct       int
	EngineVersion string
	Commit        string
	Seed          []byte
	Players       int
	State         mono.State
	RoundDeadline *time.Time
	Agents        []Player
	WinnerSeat    int
	ReplayHash    string
}

func (m *Match) agentByAgentID(agent string) *Player {
	for i := range m.Agents {
		if m.Agents[i].AgentPublicID == agent {
			return &m.Agents[i]
		}
	}
	return nil
}

func (m *Match) agentBySeat(seat int) *Player {
	for i := range m.Agents {
		if m.Agents[i].Seat == seat {
			return &m.Agents[i]
		}
	}
	return nil
}

// botSeats returns the set of seats controlled by server bots (every seat not
// held by a real agent).
func (m *Match) botSeats() map[int]bool {
	out := make(map[int]bool, m.Players)
	for seat := 0; seat < m.Players; seat++ {
		out[seat] = true
	}
	for _, p := range m.Agents {
		delete(out, p.Seat)
	}
	return out
}

// LiveMatch is one row of the public Monopoly arena list.
type LiveMatch struct {
	MatchID  string   `json:"match_id"`
	Title    string   `json:"title"`
	Agents   []string `json:"agents"`
	Players  int      `json:"players"`
	Active   int      `json:"active"` // solvent players remaining
	Round    int      `json:"round"`  // turn count
	Phase    string   `json:"phase"`
	Winner   int      `json:"winner"`
	Watchers int      `json:"watchers"`
}

// AgentView is the state returned to an agent. Monopoly is perfect-information
// except future randomness, so the whole board is exposed — but State is always
// the REDACTED state (deck orders stripped) so future cards never leak.
type AgentView struct {
	MatchID  string          `json:"match_id"`
	Status   string          `json:"status"`
	YourSeat int             `json:"your_seat"`
	Turn     int             `json:"turn"`      // seat the engine is waiting on
	YourTurn bool            `json:"your_turn"`
	Phase    string          `json:"phase"`
	Legal    []string        `json:"legal,omitempty"`
	State    *mono.State     `json:"state"`
	Deadline *time.Time      `json:"deadline,omitempty"`
	EntryFee int64           `json:"entry_fee"`
	Economy  EconomySnapshot `json:"economy"`
	Result   *Result         `json:"result,omitempty"`
}

type Result struct {
	WinnerSeat int         `json:"winner_seat"`
	Rewards    []RewardRow `json:"rewards"`
}

type CreateMatchInput struct {
	PublicID string
	Title    string
	EntryFee int64
	RakePct  int
	Players  int
	Seed     []byte
	Commit   string
	State    mono.State
	Deadline time.Time
	Events   []mono.Event
	Creator  Player
}

// Repo persists Monopoly matches on the shared matches/match_players/match_events
// tables (game='monopoly'), storing the full engine State as JSONB.
type Repo interface {
	Create(ctx context.Context, in CreateMatchInput) (Match, error)
	Get(ctx context.Context, matchPublicID string) (Match, error)
	Advance(ctx context.Context, matchPublicID string, state mono.State, deadline *time.Time, events []mono.Event) error
	Finish(ctx context.Context, matchPublicID string, state mono.State, winnerSeat int, replayHash string, agents []Player, events []mono.Event) error
	ListActiveExpired(ctx context.Context, game string, now time.Time, limit int) ([]string, error)
	LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mono.Event, error)
	LiveMatches(ctx context.Context) ([]LiveMatch, error)
}

// Locker serializes per-match mutations (same contract as the Mafia service).
type Locker interface {
	Lock(ctx context.Context, key string, ttl time.Duration) (release func(), ok bool, err error)
}

// Broadcaster fans events to SSE watchers (implemented by Hub).
type Broadcaster interface {
	Broadcast(matchPublicID string, events []mono.Event)
}

// Wallet escrows entry fees and settles payouts. Optional: a nil Wallet means a
// practice table with no ledger movement.
type Wallet interface {
	StakeTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error
	SettleTable(ctx context.Context, matchPublicID string, platformFee int64, payouts map[string]int64) error
}

// FinishHook fires after settlement (clips, notifications). Optional.
type FinishHook interface {
	MatchFinished(ctx context.Context, matchPublicID string)
}

type NoopFinishHook struct{}

func (NoopFinishHook) MatchFinished(context.Context, string) {}
