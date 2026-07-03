package match

import (
	"context"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// Repo is the match persistence port. The pgx implementation lives in
// internal/store. Each mutating method that appends events does so in the SAME
// transaction as the snapshot/status update, so the log and the cached snapshot
// never diverge.
type Repo interface {
	// CreateWaitingMatch inserts a waiting match plus its seat-A (creator) player.
	CreateWaitingMatch(ctx context.Context, in CreateMatchInput) (Match, error)

	// ListWaiting returns open matches for (game,bid), excluding the joiner's own
	// matches (same-owner pairing block), most recent first.
	ListWaiting(ctx context.Context, game string, bid int64, excludeOwnerPublicID string, limit int) ([]LobbyItem, error)

	// Get loads the full match aggregate (meta + players + state snapshot).
	Get(ctx context.Context, matchPublicID string) (Match, error)

	// Activate transitions waiting→active in one transaction: re-checks the match
	// is still waiting, inserts the seat-B player, writes the initial snapshot +
	// deadline, and appends the Init events. Returns ErrNotWaiting if it was taken.
	Activate(ctx context.Context, matchPublicID string, joiner Player, state gs.State, deadline time.Time, events []gs.Event) error

	// CreatePairedActive creates an ACTIVE match seating both agents at once (the
	// matchmaking path) in one transaction — the match row, both players, the
	// initial snapshot + deadline, and the Init events — with no waiting window, so
	// a server-paired match never surfaces in the open lobby.
	CreatePairedActive(ctx context.Context, in CreatePairedInput) error

	// Advance appends events and updates the snapshot + next deadline for an
	// in-progress match (one transaction).
	Advance(ctx context.Context, matchPublicID string, state gs.State, deadline *time.Time, events []gs.Event) error

	// Finish appends the final events, writes the terminal snapshot, sets status,
	// winner, per-player results, and the replay hash (one transaction). When
	// finishedEvent is non-nil it is also written to the domain event outbox in the
	// SAME transaction (the match.finished fact); pass nil to emit no event (e.g.
	// sandbox matches, which are off the growth path).
	Finish(ctx context.Context, matchPublicID string, state gs.State, winnerAgentPublicID, replayHash string, players []Player, events []gs.Event, finishedEvent []byte) error

	// ListActiveExpired returns public ids of active matches whose round deadline
	// has passed (drives the timeout sweeper).
	ListActiveExpired(ctx context.Context, now time.Time, limit int) ([]string, error)

	// LoadEvents returns the full event log for replay/verification.
	LoadEvents(ctx context.Context, matchPublicID string) ([]gs.Event, error)

	// AgentSigningKey returns the agent's registered Ed25519 public key (base64),
	// or "" if the agent has not registered one (then moves are unsigned/trusted).
	AgentSigningKey(ctx context.Context, agentPublicID string) (string, error)

	// RecordMoveSignature persists a verified move signature for replay-time proof.
	RecordMoveSignature(ctx context.Context, matchPublicID string, round, seat, card int, signature, pubkey string) error

	// LoadMoveSignatures returns all recorded move signatures for a match (replay).
	LoadMoveSignatures(ctx context.Context, matchPublicID string) ([]MoveSignature, error)

	// CancelWaiting aborts an open lobby entry before activation (no stakes locked).
	CancelWaiting(ctx context.Context, matchPublicID, creatorAgentPublicID string) error
}

// MoveSignature is one agent-authored move proof.
type MoveSignature struct {
	Round     int    `json:"round"`
	Seat      int    `json:"seat"`
	Card      int    `json:"card"`
	Signature string `json:"signature"`
	Pubkey    string `json:"pubkey"`
}

// Locker provides a short-lived, per-match mutual-exclusion lease (Redis), so a
// match's state is mutated by exactly one operation at a time across the fleet.
type Locker interface {
	// Lock attempts to acquire key for ttl. On success ok=true and release frees it.
	Lock(ctx context.Context, key string, ttl time.Duration) (release func(), ok bool, err error)
}

// CreateMatchInput is the data needed to open a waiting match.
type CreateMatchInput struct {
	PublicID      string
	Game          string
	Bid           int64
	RakePct       int
	TotalRounds   int
	EngineVersion string
	Commit        string
	FairnessMode  string
	Seed          []byte
	Creator       Player // seat 0
}

// CreatePairedInput is the data needed to open an already-active, two-seat match
// (matchmaking). Unlike CreateMatchInput it carries both players plus the dealt
// initial snapshot/deadline/events, since there is no separate join step.
type CreatePairedInput struct {
	PublicID      string
	Game          string
	Mode          string // ModeCompetitive (default) | ModeSandbox
	BotPolicy     string // house strategy when Mode == ModeSandbox; "" otherwise
	Bid           int64
	RakePct       int
	TotalRounds   int
	EngineVersion string
	Commit        string
	FairnessMode  string
	Seed          []byte
	SeatA         Player // seat 0
	SeatB         Player // seat 1
	State         gs.State
	Deadline      time.Time
	Events        []gs.Event
}

// LobbyItem is a summary of an open match.
type LobbyItem struct {
	PublicID             string    `json:"match_id"`
	Game                 string    `json:"game"`
	Bid                  int64     `json:"bid"`
	CreatorAgentPublicID string    `json:"creator_agent"`
	CreatedAt            time.Time `json:"created_at"`
}
