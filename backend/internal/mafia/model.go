package mafia

import (
	"context"
	"sort"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

const (
	StatusWaiting  = "waiting"
	StatusActive   = "active"
	StatusFinished = "finished"
	StatusAborted  = "aborted"
	GameName       = "mafia"

	// DefaultMaxDays bounds a table so it MUST terminate.
	//
	// Mafia was the only unbounded game: mf.New() leaves MaxDays = 0 (unlimited) and
	// ForceTimeout is a pure abstain — it eliminates nobody. So if every agent went
	// silent on a staked table, the sweeper timed out each phase, nobody died, the day
	// advanced, and it looped forever: the match never finished, settlement never ran,
	// and the escrowed stakes (12 seats × the tier) were locked permanently with no
	// operator path to release them.
	//
	// Monopoly caps dice rolls at 1000 explicitly "to guarantee termination" and
	// Goofspiel is bounded by its round count; this is Mafia's equivalent. On reaching
	// the cap the engine decides by surviving majority (finalByMajority), so the table
	// settles on the state of play rather than being voided.
	//
	// 14 days is far beyond a real 12-seat game (which ends in a handful of days once
	// agents act) and only ever bites an abandoned or pathological table.
	DefaultMaxDays = 14
)

// Player is one seat at the table (seats are 1..12).
type Player struct {
	AgentPublicID string
	OwnerPublicID string
	Seat          int
	Role          string
	Team          string
	Alive         bool
	CoinsDelta    int64
	// IsHouse marks a seat held by a kind='house' engine bot rather than a
	// developer's agent. House seats exist so a table can reach its roster when the
	// queue is thin, and they are excluded from EVERY money and ranking path: they
	// do not stake, are not counted in the pool, cannot be paid, and their presence
	// makes the whole table unrated. Sourced from agents.kind, so it cannot drift
	// from the seeded bot set (migration 0063).
	IsHouse bool
	// Display identity. The agents/users rows were already joined to load a seat —
	// these columns were simply never selected, which is why every surface could
	// only ever say "Seat 7" instead of naming the agent behind it.
	Name      string
	OwnerName string
	AvatarURL string
}

// HumanPlayers returns the seats that belong to real developers' agents — the only
// seats that stake, settle, or rate. Use this anywhere a count of "how many agents
// are in this game" feeds money or ranking, because len(Match.Players) now includes
// house fillers.
func HumanPlayers(players []Player) []Player {
	out := make([]Player, 0, len(players))
	for _, p := range players {
		if !p.IsHouse {
			out = append(out, p)
		}
	}
	return out
}

// HasHouseSeat reports whether any seat at this table is a house bot, which is what
// makes a table unrated.
func HasHouseSeat(players []Player) bool {
	for _, p := range players {
		if p.IsHouse {
			return true
		}
	}
	return false
}

// RosterSeat is one seat's public identity. Roles are NEVER included — that is
// hidden information and lives in the redacted per-seat view.
type RosterSeat struct {
	Seat      int    `json:"seat"`
	AgentID   string `json:"agent_id"`
	Name      string `json:"name"`
	Owner     string `json:"owner,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Alive     bool   `json:"alive"`
}

// RosterOf projects the seated players into their public identities, ordered by
// seat so a client can index it directly.
func RosterOf(players []Player, alive map[int]bool) []RosterSeat {
	out := make([]RosterSeat, 0, len(players))
	for _, p := range players {
		a := p.Alive
		if alive != nil {
			if v, ok := alive[p.Seat]; ok {
				a = v
			}
		}
		out = append(out, RosterSeat{
			Seat: p.Seat, AgentID: p.AgentPublicID, Name: p.Name,
			Owner: p.OwnerName, AvatarURL: p.AvatarURL, Alive: a,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seat < out[j].Seat })
	return out
}

// Match is the full Mafia aggregate.
type Match struct {
	PublicID      string
	Title         string
	Status        string
	EntryFee      int64
	RakePct       int
	EngineVersion string
	Commit        string
	Seed          []byte
	State         mf.State
	RoundDeadline *time.Time
	// StartsAt is the ABSOLUTE instant play begins, set when the roster fills and the table
	// starts. Null on a table that has not started, and on any match that predates it.
	//
	// Absolute, never a duration, for the reason internal/readycheck states: a terminal and a
	// browser each counting down from ten drift apart within seconds, and two surfaces
	// disagreeing about when a staked match begins is worse than showing no countdown at all.
	// Every surface counts TO this.
	StartsAt *time.Time
	Players  []Player
	WinnerTeam    string
	ReplayHash    string
}

func (m *Match) playerByAgent(agent string) *Player {
	for i := range m.Players {
		if m.Players[i].AgentPublicID == agent {
			return &m.Players[i]
		}
	}
	return nil
}

func (m *Match) playerBySeat(seat int) *Player {
	for i := range m.Players {
		if m.Players[i].Seat == seat {
			return &m.Players[i]
		}
	}
	return nil
}

// LobbyItem summarizes an open Mafia table.
type LobbyItem struct {
	PublicID             string    `json:"match_id"`
	EntryFee             int64     `json:"entry_fee"`
	SeatsFilled          int       `json:"seats_filled"`
	SeatsTotal           int       `json:"seats_total"`
	CreatorAgentPublicID string    `json:"creator_agent"`
	CreatedAt            time.Time `json:"created_at"`
}

// AgentView is the redacted state returned to a seated agent. It carries exactly
// what the seat is legally allowed to know: its own role, its fellow-Mafia allies
// (Mafia only), the action kinds it may submit now, the public transcript, and
// its OWN private night results (e.g. a detective's finding). Other seats' roles
// and night secrets are never included.
type AgentView struct {
	MatchID  string       `json:"match_id"`
	Status   string       `json:"status"`
	Day      int          `json:"day"`
	Phase    string       `json:"phase"`
	YourSeat int          `json:"your_seat"`
	YourRole string       `json:"your_role,omitempty"`
	Alive    map[int]bool `json:"alive"`
	Allies   []int        `json:"allies,omitempty"`  // fellow Mafia seats (Mafia agents only)
	Legal    []string     `json:"legal,omitempty"`   // action kinds valid for this seat now
	Public   []mf.Event   `json:"public,omitempty"`  // shared transcript this seat may see
	Private  []mf.Event   `json:"private,omitempty"` // this seat's own night results only
	Deadline *time.Time   `json:"deadline,omitempty"`
	// StartsAt is the ABSOLUTE instant play begins, present once the table has started.
	// Clients render a countdown from it; see internal/readycheck for why it is an instant
	// and not a duration.
	StartsAt *time.Time `json:"starts_at,omitempty"`
	// ServerNow is the platform's clock when this view was built.
	//
	// Shipped with every view so a client can measure its own offset and render any absolute
	// instant correctly, rather than trusting a device clock that may be minutes out. Without
	// it StartsAt is unusable on a skewed machine, which is most of them.
	ServerNow time.Time `json:"server_now"`
	// CannotProtect (doctors) and AllyKills (mafia) come from the engine's own redaction and
	// must be carried through every layer between it and the agent.
	//
	// There are THREE hand-built views on that path — the engine's, this one, and
	// MafiaPushView — and a field added to the first is invisible to agents until it is
	// copied into the other two. Both of these were added to the engine and forwarded
	// nowhere, so the rules they encode existed and no agent was ever told.
	CannotProtect int         `json:"cannot_protect"`
	AllyKills     map[int]int `json:"ally_kills,omitempty"`
	// Live voting state for the current round (present only during the voting
	// phase) so an agent can reason about bandwagons / saving an ally without
	// reconstructing it from raw vote events.
	Votes      map[int]int `json:"votes,omitempty"`       // voter seat -> target seat
	VoteTally  map[int]int `json:"vote_tally,omitempty"`  // target seat -> number of votes
	DeadlineMs int64       `json:"deadline_ms,omitempty"` // ms left on the shot clock (0 once elapsed)
	// PhaseDurationMs is the FULL length of the current phase, so a client can draw
	// a countdown ring (elapsed vs remaining) rather than just a shrinking number.
	PhaseDurationMs int64 `json:"phase_duration_ms,omitempty"`
	// WarnAt is when "this phase is nearly over" should fire, as an ABSOLUTE instant, or
	// absent when the phase is too short to warn about (see deadline.WarnLead).
	//
	// This is the last-seconds cue for DISCUSSION above all: a table talking its way toward a
	// vote needs to know when to stop arguing and commit, and an agent that only discovers the
	// phase ended by having its message refused has effectively been cut off mid-sentence.
	//
	// A FRACTION of the phase length, for the same reason as everywhere else: Mafia's phases
	// differ in length by design, so a fixed lead would be most of a short phase and a
	// rounding error on a long one.
	//
	// RECOMPUTED from phaseWindow here, and that is safe in a way it is NOT for Goofspiel.
	// Mafia's window is a pure function of the phase — config or mf.PhaseDuration, no latency
	// samples — so recomputing returns exactly the value that was applied when the deadline
	// was set. Goofspiel's window is adaptive, so it must be read back as
	// deadline-minus-round-start instead of recomputed. Same rule, deliberately different
	// source; do not "unify" these without checking which side is deterministic.
	WarnAt *time.Time `json:"warn_at,omitempty"`
	// WarnInMs is the same instant as ms remaining. 0 once elapsed, or when there is none.
	WarnInMs int64 `json:"warn_in_ms,omitempty"`
	// CanSpeak states the table-talk rule for the current phase up front: the town
	// is asleep at night, so nobody may speak. Without this an agent only learns it
	// by having a message rejected, and the UI cannot grey the composer out.
	CanSpeak bool `json:"can_speak"`
	// Roster maps every seat to the agent sitting in it. Without this the whole
	// spectator surface can only render bare seat numbers — no names, no avatars,
	// nothing to label a speaker or a vote line with.
	Roster []RosterSeat `json:"roster,omitempty"`
	// Pending is every seat the table is currently waiting on — the "thinking…" set.
	// DERIVED from engine state (mf.PendingActors), never invented, so it cannot
	// drift from the game and is correct immediately after a reconnect or a replay.
	//
	// It is a LIST because thinking here is genuinely concurrent: at night the mafia,
	// doctor, detective and sheriff all decide at once, and in discussion every living
	// seat owes a statement. Showing one name at a time would misreport the table —
	// this is the "A, B and C are typing…" case, not a single spinner.
	Pending  []int           `json:"pending,omitempty"`
	EntryFee int64           `json:"entry_fee"`
	Economy  EconomySnapshot `json:"economy"`
	Result   *EconomyResult  `json:"result,omitempty"`
}

type EconomyResult struct {
	Winner  string      `json:"winner"`
	Rewards []RewardRow `json:"rewards"`
}

// Repo persists Mafia matches.
type Repo interface {
	CreateWaiting(ctx context.Context, in CreateMatchInput) (Match, error)
	ListWaiting(ctx context.Context, entryFee int64, excludeOwnerPublicID string, limit int) ([]LobbyItem, error)
	Get(ctx context.Context, matchPublicID string) (Match, error)
	JoinSeat(ctx context.Context, matchPublicID string, p Player) error
	Start(ctx context.Context, matchPublicID string, roles map[int]string, state mf.State, startsAt, deadline time.Time, events []mf.Event) error
	// MarkUnrated flags a match as excluded from ranked statistics (matches.rated =
	// false). Called at start time for a table that house bots had to fill. Idempotent.
	MarkUnrated(ctx context.Context, matchPublicID string) error
	Advance(ctx context.Context, matchPublicID string, state mf.State, deadline *time.Time, alive map[int]bool, events []mf.Event) error
	Finish(ctx context.Context, matchPublicID string, state mf.State, winnerTeam, replayHash string, players []Player, events []mf.Event) error
	ListActiveExpired(ctx context.Context, game string, now time.Time, limit int) ([]string, error)
	LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mf.Event, error)
	// LoadEventsTimed returns the full log with each event's write time, for a
	// replay that reproduces the original pacing.
	LoadEventsTimed(ctx context.Context, matchPublicID string) ([]TimedEvent, error)
	LiveMatches(ctx context.Context) ([]LiveMatch, error)
	CancelWaiting(ctx context.Context, matchPublicID, creatorAgentPublicID string) error
	// ExpireStaleWaiting aborts up to limit waiting tables created at/before cutoff
	// (tables that never gathered a full roster), returning how many were aborted. No
	// stakes are escrowed until a table starts, so nothing is refunded — this just
	// frees agents stuck in a lobby that can never fill. System-driven (no creator).
	ExpireStaleWaiting(ctx context.Context, cutoff time.Time, limit int) (int, error)

	// AgentSigningKey returns the agent's registered Ed25519 public key (base64),
	// or "" if unregistered (then moves are unsigned/trusted, like Goofspiel).
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

type CreateMatchInput struct {
	PublicID string
	Title    string
	EntryFee int64
	RakePct  int
	Seed     []byte
	Commit   string
	Creator  Player
}

// Locker serializes match mutations.
type Locker interface {
	Lock(ctx context.Context, key string, ttl time.Duration) (release func(), ok bool, err error)
}

// Wallet escrows entry fees and settles multi-winner payouts.
type Wallet interface {
	StakeTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error
	SettleTable(ctx context.Context, matchPublicID string, platformFee int64, payouts map[string]int64) error
	// RefundTable unwinds a taken stake (each seat's entry fee returned from escrow),
	// used to compensate a stake-then-start dual-write failure. Idempotent via the
	// per-match disburse key.
	RefundTable(ctx context.Context, matchPublicID string) error
}

// Broadcaster fans events to SSE watchers.
// TimedEvent is a logged event plus the instant it was written.
//
// This is what makes a replay faithful rather than merely correct: the gaps between
// lines ARE the evidence. "Seat 4 answered that accusation 12 seconds later" reads
// completely differently from an instant reply, and a viewer that replays every event
// back-to-back destroys exactly the information a spectator is trying to judge.
type TimedEvent struct {
	Event mf.Event
	At    time.Time
}

type Broadcaster interface {
	Broadcast(matchPublicID string, events []mf.Event)
	// BroadcastPending pushes the live "thinking…" set (ephemeral, unsequenced —
	// never persisted, never replayed).
	BroadcastPending(matchPublicID string, seats []int)
}

// Limits checks spending limits at join.
type Limits interface {
	CheckJoin(ctx context.Context, agentPublicID string, entryFee int64) error
}

// Verifier gates agent eligibility.
type Verifier interface {
	CheckEligible(ctx context.Context, agentPublicID string) error
}

// FinishHook fires after settlement (clips, notifications).
type FinishHook interface {
	MatchFinished(ctx context.Context, matchPublicID string)
}
