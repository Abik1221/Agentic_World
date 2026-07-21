package monopoly

import (
	"context"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

const (
	StatusWaiting  = "waiting"
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
	// TargetPlayers is the number of real agents a WAITING (staked, agent-vs-agent)
	// table waits for before it starts. Zero for practice/active tables, where the
	// seat count is derived from len(State.Players).
	TargetPlayers int
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

// LobbyItem summarizes an open (waiting) staked Monopoly table.
type LobbyItem struct {
	PublicID             string    `json:"match_id"`
	EntryFee             int64     `json:"entry_fee"`
	SeatsFilled          int       `json:"seats_filled"`
	SeatsTotal           int       `json:"seats_total"`
	CreatorAgentPublicID string    `json:"creator_agent"`
	CreatedAt            time.Time `json:"created_at"`
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
	Turn     int             `json:"turn"` // seat the engine is waiting on
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
	// TargetPlayers is stored on a WAITING table so a later Join knows when the
	// table is full and can start. Ignored for immediately-started tables.
	TargetPlayers int
	Seed          []byte
	Commit        string
	State         mono.State
	Deadline      time.Time
	Events        []mono.Event
	Creator       Player
}

// Repo persists Monopoly matches on the shared matches/match_players/match_events
// tables (game='monopoly'), storing the full engine State as JSONB.
type Repo interface {
	Create(ctx context.Context, in CreateMatchInput) (Match, error)
	// CreateWaiting opens a staked, agent-vs-agent table in the WAITING state with
	// only the creator seated and no engine state yet (started by Start on fill).
	CreateWaiting(ctx context.Context, in CreateMatchInput) (Match, error)
	// ListWaiting lists open waiting tables at the given entry fee (0 = any),
	// excluding tables the given owner already holds a seat at.
	ListWaiting(ctx context.Context, entryFee int64, excludeOwnerPublicID string, limit int) ([]LobbyItem, error)
	// JoinSeat seats a new agent at the given seat on a waiting table.
	JoinSeat(ctx context.Context, matchPublicID string, p Player) error
	// Start flips a waiting table to active with its initialized engine state.
	Start(ctx context.Context, matchPublicID string, state mono.State, deadline time.Time, events []mono.Event) error
	// CancelWaiting aborts a waiting table (creator-only; no-op if already started).
	CancelWaiting(ctx context.Context, matchPublicID, creatorAgentPublicID string) error
	Get(ctx context.Context, matchPublicID string) (Match, error)
	Advance(ctx context.Context, matchPublicID string, state mono.State, deadline *time.Time, events []mono.Event) error
	Finish(ctx context.Context, matchPublicID string, state mono.State, winnerSeat int, replayHash string, agents []Player, events []mono.Event) error
	ListActiveExpired(ctx context.Context, game string, now time.Time, limit int) ([]string, error)
	LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mono.Event, error)
	LiveMatches(ctx context.Context) ([]LiveMatch, error)

	// AgentSigningKey returns the agent's registered Ed25519 public key (base64),
	// or "" if unregistered (moves are then unsigned/trusted, like Goofspiel).
	AgentSigningKey(ctx context.Context, agentPublicID string) (string, error)
	// RecordMoveSignature persists a verified per-move authorship proof (audit trail).
	RecordMoveSignature(ctx context.Context, matchPublicID string, seq, seat int, action, signature, pubkey string) error
	// LoadMoveSignatures returns all recorded proofs for a match (replay re-verify).
	LoadMoveSignatures(ctx context.Context, matchPublicID string) ([]MoveSig, error)
}

// MoveSig is one persisted per-move authorship proof: the agent's Ed25519
// signature over the canonical (match, seq, seat, action) message.
type MoveSig struct {
	Seq       int
	Seat      int
	Action    string
	Signature string
	Pubkey    string
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
	// SettleTable pays the winner + platform from escrow. `gross` is the full staked
	// pool (agents × entryFee) so the escrow debit always balances.
	SettleTable(ctx context.Context, matchPublicID string, gross, platformFee int64, payouts map[string]int64) error
	// RefundTable returns every agent's entry fee from escrow when a staked table
	// fails to start after staking. Idempotent per match.
	RefundTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error
}

// Limits checks per-agent spending limits before a staked join. Optional: a nil
// Limits skips the check (practice tables never stake, so never call it).
type Limits interface {
	CheckJoin(ctx context.Context, agentPublicID string, entryFee int64) error
}

// Verifier gates agent eligibility for staked play (certification, suspension).
// Optional: a nil Verifier skips the check.
type Verifier interface {
	CheckEligible(ctx context.Context, agentPublicID string) error
}

// FinishHook fires after settlement (clips, notifications). Optional.
type FinishHook interface {
	MatchFinished(ctx context.Context, matchPublicID string)
}

type NoopFinishHook struct{}

func (NoopFinishHook) MatchFinished(context.Context, string) {}
