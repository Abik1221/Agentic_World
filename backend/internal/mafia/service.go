package mafia

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-arena/arena/internal/httpx"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/deadline"
	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/integrity"
	"github.com/agent-arena/arena/internal/liveness"
	"github.com/agent-arena/arena/internal/movebind"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/agent-arena/arena/internal/readycheck"
	"github.com/agent-arena/arena/internal/turnproof"
)

// Config tunes phase windows and table economics.
type Config struct {
	EntryFee       int64
	PlatformFeePct int
	// PhaseWindow forces every phase to the same length. Zero (the default) uses
	// the engine's per-phase clock — see Service.phaseWindow.
	PhaseWindow time.Duration
	LockTTL     time.Duration
	RosterSize  int
	// MaxDays bounds the game so an abandoned staked table cannot loop forever and
	// strand its escrow. Zero uses DefaultMaxDays; see that constant.
	MaxDays int
	// WaitingTTL is how long a waiting table (not yet full) may sit before the
	// sweeper aborts it. A Mafia table needs a full 12 distinct-owner roster to
	// start, so an unfillable lobby is the likely default, not an edge case.
	WaitingTTL time.Duration
	// HouseDriveInterval paces the house-bot driver (see DriveHouseSeats). Zero uses
	// DefaultHouseDriveInterval. Exists as a knob mainly so tests can run the loop fast;
	// in production the default is what keeps bots from answering faster than any
	// LLM-backed agent could.
	HouseDriveInterval time.Duration
}

// Service drives the Mafia match lifecycle.
// SetHouseRoster records the agent ids the platform itself runs.
//
// # Why an explicit list and not a pattern
//
// This is an exemption inside a fraud control, so it must be impossible to fall into by
// accident. Matching on a slug prefix or a framework label would mean any agent that came to
// look house-shaped would inherit the exemption; an id set, built at boot from the seeder's own
// return value, cannot be joined by anything a user creates.
//
// # What the exemption is, exactly
//
// A house agent skips the LLM-certification check on ZERO-FEE tables only. It can never skip it
// on a paid one, because a house agent must never be at a paid table at all — that is enforced
// separately by houseStake() == 0 and its regression guards. So the widest this can ever open is
// "the platform may seat its own deterministic bots at practice tables", which is precisely the
// intent: our bots may fill a seat, a user's may not.
type Service struct {
	house  map[string]bool // agent ids the PLATFORM runs; see SetHouseRoster
	stakes StakeFloor      // rejects an entry fee mafia does not offer; see StakeFloor
	repo   Repo
	lock   Locker
	limits Limits
	wallet Wallet
	bcast  Broadcaster
	ver    Verifier
	finish FinishHook
	// actDecisions instruments the request path. Nil leaves Act uninstrumented.
	actDecisions ActDecisionRecorder
	clock        platform.Clock
	cfg          Config
	// defaultStake supplies the admin's cheapest enabled tier for lobby browsing.
	defaultStake func(context.Context) (int64, bool)
	// rake, when set, supplies the LIVE platform commission for a new match, so the
	// admin's fee control actually moves money instead of being decorative. Nil ⇒ the
	// static config value. Read at creation only; the result is persisted on the match
	// and settlement reads it back, so a mid-match change never re-prices a live table.
	rake func() int
	eng  *mf.Engine
	// pusher is set by EnablePushPlay to enable POST /v1/mafia/pushplay
	// (manifest push model with bot-filled seats). Nil ⇒ push-play returns 501.
	pusher *pushPlayer
	// houseAgents is the allowlist of seeded house-bot ids that JoinHouseSeat will seat,
	// populated by EnablePushPlay from the same bot list. It exists because JoinHouseSeat
	// bypasses the distinct-owner, spending-limit, and certification gates: without an
	// allowlist, one mistaken call with a real agent's id would seat that agent free of
	// every check. Empty ⇒ no house seating at all, which fails closed.
	houseAgents map[string]bool
	// notify wakes long-polling State callers on a state change. Nil ⇒ no long-poll.
	notify Notifier
	// rater applies per-arena skill ratings when a paid table finalizes. Nil ⇒
	// ratings skipped (tests / notifier-less builds). rating.Service satisfies it.
	rater Rater
	// integrity withholds a payout from a seat that proved no LLM-backed decision on a
	// PAID table. Nil ⇒ no check, which is the correct default for tests and for any
	// deployment without the proof pipeline. See internal/integrity.
	integrity integrity.Checker
	// turns mints per-turn proofs for push-play views. Nil ⇒ no proofs.
	turns TurnMinter
	// liveness suppresses forfeits during the grace window after a detected platform
	// outage. Nil is valid and means "no grace".
	liveness *liveness.Tracker
	// chatTracer records table talk to Lens. Nil ⇒ telemetry off.
	chatTracer ChatTracer
	// decisionTracer records each resolved agent turn. Nil ⇒ telemetry off.
	decisionTracer DecisionTracer
	// boundMoves reports the move the MODEL produced for a turn, as the gateway observed
	// it. Nil ⇒ completion binding is not enforced, which is the pre-existing behaviour.
	boundMoves movebind.Reader
}

// SetBoundMoveReader installs completion-binding enforcement (called once at wiring time).
func (s *Service) SetBoundMoveReader(r movebind.Reader) { s.boundMoves = r }

// ChatTracer records agent table talk to the observability pipeline. Satisfied by
// *telemetry.Client; nil means telemetry is off and every call is a no-op.
type ChatTracer interface {
	EmitAgentSaid(ev telemetry.ChatEvent)
	EmitAgentSayRejected(ev telemetry.ChatEvent)
}

// SetChatTracer installs the chat tracer (called once at wiring time).
func (s *Service) SetChatTracer(t ChatTracer) { s.chatTracer = t }

// DecisionTracer records one resolved agent turn. Satisfied by *telemetry.Client.
type DecisionTracer interface {
	EmitAgentDecision(ev telemetry.DecisionEvent)
}

// SetDecisionTracer installs the per-decision tracer (called once at wiring time).
func (s *Service) SetDecisionTracer(t DecisionTracer) { s.decisionTracer = t }

// ActDecision is one decision made through the REQUEST path.
//
// Declared here rather than reusing store.ActDecision so this package does not depend on the
// persistence layer — a game service defines what it needs and main.go adapts.
type ActDecision struct {
	MatchID       string
	AgentPublicID string
	Seq           int
	Round         int
	Action        string
	Outcome       string
	InputJSON     []byte
}

// ActDecisionRecorder durably records request-path decisions and builds the per-seat facts at
// match end.
//
// Mafia's instrumentation lived only in its push-play driver, so an agent that polls state and
// posts actions produced no benchmark fact, no decision log and no board presence. See
// OBSERVABILITY_COVERAGE_GAP.md.
type ActDecisionRecorder interface {
	RecordActDecision(ctx context.Context, d ActDecision) error
	AggregateSeatBenchmark(ctx context.Context, matchID, game string, results map[string]string) error
}

// SetActDecisionRecorder wires request-path instrumentation. Optional.
func (s *Service) SetActDecisionRecorder(r ActDecisionRecorder) { s.actDecisions = r }

// SetLiveness installs the post-outage grace tracker (called once at wiring time).
func (s *Service) SetLiveness(t *liveness.Tracker) { s.liveness = t }

// Rater applies a finished ranked table's TrueSkill change to the Mafia arena.
// Satisfied directly by *rating.Service.
type Rater interface {
	Rate(ctx context.Context, res rating.MatchResult) error
}

// SetRater installs the rating hook (called once at wiring time).
func (s *Service) SetRater(r Rater) { s.rater = r }

// SetIntegrityChecker installs the proof-of-LLM check applied to PAID tables. Optional:
// nil settles exactly as before. See internal/integrity for why the rule is relative and
// therefore safe to leave switched on before any SDK sends proofs.
func (s *Service) SetIntegrityChecker(c integrity.Checker) { s.integrity = c }

// SetTurnMinter installs the per-turn proof minter used by push-play views.
//
// MUST be called BEFORE EnablePushPlay, which copies it onto the push player — the same
// ordering constraint Goofspiel documents. Without a minter, Mafia views ship with no
// proof, no decision can be shown to be LLM-backed, and the integrity check on paid
// tables can never arm however it is configured.
func (s *Service) SetTurnMinter(m TurnMinter) {
	s.turns = m
	// ALSO push onto an already-built pushPlayer, because EnablePushPlay copies s.turns by value.
	//
	// Monopoly's wiring called SetTurnMinter AFTER EnablePushPlay, so the pusher captured nil and
	// every Monopoly view shipped with no turn proof — meaning no Monopoly decision could ever be
	// bound, no Monopoly agent could earn Verified, and nothing anywhere said so. Mafia happened to
	// be wired in the opposite order and worked, which is the worst kind of correctness: identical
	// code, opposite behaviour, decided by a line number.
	//
	// Propagating here makes the order irrelevant instead of merely fixing today's order.
	if s.pusher != nil {
		s.pusher.turns = m
	}
}

func NewService(repo Repo, lock Locker, limits Limits, wallet Wallet, bcast Broadcaster, ver Verifier, finish FinishHook, clock platform.Clock, cfg Config) *Service {
	// PhaseWindow is left at zero on purpose when unset: that selects the engine's
	// per-phase clock (night short, discussion long, voting tight) instead of one
	// flat window for every phase. A non-zero value is an explicit operator override.
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = 15 * time.Second
	}
	if cfg.WaitingTTL <= 0 {
		cfg.WaitingTTL = 10 * time.Minute
	}
	if cfg.EntryFee <= 0 {
		cfg.EntryFee = DefaultEntryFee
	}
	if cfg.PlatformFeePct <= 0 {
		cfg.PlatformFeePct = DefaultPlatformFeePct
	}
	// Roles are dealt from a FIXED pool (mf.RoleSetup); the roster MUST equal the
	// pool size, or assignRoles would panic (too many seats) or silently skew the
	// faction balance (too few). Pin it so a Config misconfig can't corrupt role
	// assignment on a real (money) table.
	if cfg.RosterSize != len(mf.RoleSetup) {
		cfg.RosterSize = len(mf.RoleSetup)
	}
	// Never leave the engine unbounded: mf.New() means MaxDays 0 = unlimited, and with
	// ForceTimeout being a pure abstain an all-silent table would loop forever with its
	// stakes locked in escrow. A non-positive value is a misconfiguration, not a request
	// for an infinite game.
	if cfg.MaxDays <= 0 {
		cfg.MaxDays = DefaultMaxDays
	}
	if cfg.HouseDriveInterval <= 0 {
		cfg.HouseDriveInterval = DefaultHouseDriveInterval
	}
	if finish == nil {
		finish = NoopFinishHook{}
	}
	if ver == nil {
		ver = AllowAllVerifier{}
	}
	if limits == nil {
		limits = NoopLimits{}
	}
	return &Service{repo: repo, lock: lock, limits: limits, wallet: wallet, bcast: bcast, ver: ver, finish: finish, clock: clock, cfg: cfg, eng: mf.NewWithMaxDays(cfg.MaxDays)}
}

func lockKey(id string) string { return "mafia:lock:" + id }

// agentJoinLockKey shares the "agent:join:lock:" namespace with Goofspiel so a single
// agent's concurrent joins serialize across both games. (M6)
func agentJoinLockKey(agentPublicID string) string { return "agent:join:lock:" + agentPublicID }

// SetDefaultStakeSource wires the admin's cheapest configured tier as the lobby's
// default browse stake, so "show me the tables" lands on a stake the operator
// actually offers instead of a constant compiled in months ago. Nil ⇒ static config.
func (s *Service) SetDefaultStakeSource(f func(context.Context) (int64, bool)) {
	s.defaultStake = f
}

func (s *Service) Lobby(ctx context.Context, entryFee int64, ownerPublicID string) ([]LobbyItem, error) {
	if entryFee <= 0 {
		// The admin's lowest enabled tier, falling back to config only when no tiers
		// are configured at all.
		if s.defaultStake != nil {
			if coins, ok := s.defaultStake(ctx); ok {
				entryFee = coins
			}
		}
	}
	if entryFee <= 0 {
		entryFee = s.cfg.EntryFee
	}
	return s.repo.ListWaiting(ctx, entryFee, ownerPublicID, 50)
}

// StakeFloor validates that a coin amount is a stake the game actually offers.
//
// Same port, same reason, as match.StakeFloor: the handler resolves tiers correctly, but
// CreateTable is also reached directly — by the house-bot runner, and by the service's own
// DefaultEntryFee of 100 substituted whenever a fee is absent. Both sat below the configured
// 500-coin floor, so mafia had a second, independent route to a stake the game does not offer.
type StakeFloor interface {
	ValidStake(ctx context.Context, game string, coins int64) (ok bool, lowest int64, err error)
}

// SetStakeFloor wires tier enforcement into the service that escrows.
func (s *Service) SetStakeFloor(f StakeFloor) { s.stakes = f }

// checkStake rejects a stake mafia does not offer. Fails CLOSED — an unreadable tier table is
// not permission to escrow an arbitrary amount. A zero fee is a practice table and is exempt.
func (s *Service) checkStake(ctx context.Context, entryFee int64) error {
	if s.stakes == nil || entryFee <= 0 {
		return nil
	}
	ok, lowest, err := s.stakes.ValidStake(ctx, GameName, entryFee)
	if err != nil {
		return httpx.NewError(http.StatusServiceUnavailable, "stakes_unavailable",
			"Stake tiers could not be read, so the stake cannot be verified. Try again shortly.")
	}
	if !ok {
		return httpx.NewError(http.StatusBadRequest, "stake_not_offered",
			fmt.Sprintf("An entry fee of %d coins is not offered for mafia. The lowest available stake is %d coins.", entryFee, lowest))
	}
	return nil
}

func (s *Service) CreateTable(ctx context.Context, agentPublicID, ownerPublicID string, entryFee int64) (string, error) {
	if entryFee < 0 {
		entryFee = 0
	}
	if err := s.checkStake(ctx, entryFee); err != nil {
		return "", err
	}
	// A zero-fee table is a no-stakes practice/sandbox table (nothing staked, no
	// payout, no rating change): skip the spending-limit and certification gates,
	// matching the Goofspiel sandbox and Monopoly's zero-fee tables. Paid tables
	// enforce both.
	if entryFee > 0 {
		if err := s.limits.CheckJoin(ctx, agentPublicID, entryFee); err != nil {
			return "", err
		}
	}
	// Certification runs on EVERY table, paid or free — outside the fee branch on purpose.
	// A practice match writes decision and benchmark rows that feed the P-Index, the model board
	// and the deception index, so a scripted agent farming free tables earns a public record it
	// did not deserve. Only the platform's own bots are exempt, and only here where nothing is
	// staked; see mustCertify.
	if err := s.certify(ctx, agentPublicID, entryFee); err != nil {
		return "", err
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	m, err := s.repo.CreateWaiting(ctx, CreateMatchInput{
		PublicID: platform.NewID(platform.PrefixMafia),
		Title:    "Mafia AI Arena",
		EntryFee: entryFee,
		RakePct:  s.rakePct(),
		Seed:     seed,
		Commit:   mf.Commit(seed),
		Creator:  Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: 1},
	})
	if err != nil {
		return "", err
	}
	return m.PublicID, nil
}

// PrivateRoomMinHumans is how many distinct-owner human seats a Play-a-friend
// Mafia room needs before the house fills the rest and the table starts.
//
// Host + one friend is the invite product; the engine still deals a fixed
// 12-seat roster, so the remaining seats are house bots (same spirit as a
// thin ranked queue). More friends can join an open public lobby table; a
// private room starts as soon as the second human sits.
const PrivateRoomMinHumans = 2

// CreateRoom opens a PRIVATE staked Mafia table: unlisted, invite-by-id.
//
// The host takes seat 1. When a second distinct-owner human joins, house bots
// fill the remaining seats and the match starts (see Join). Local CLI socket
// or hosted verify is enough to sit — same playable gate as Goofspiel rooms.
func (s *Service) CreateRoom(ctx context.Context, agentPublicID, ownerPublicID string, entryFee int64) (string, error) {
	if entryFee <= 0 {
		return "", httpx.NewError(http.StatusBadRequest, "invalid_request", "bid must be > 0")
	}
	if err := s.checkStake(ctx, entryFee); err != nil {
		return "", err
	}
	if err := s.checkSeat(ctx, agentPublicID, entryFee, true); err != nil {
		return "", err
	}
	if err := s.checkPlayable(ctx, agentPublicID, entryFee, true); err != nil {
		return "", err
	}
	needBots := s.cfg.RosterSize - PrivateRoomMinHumans
	if s.pusher == nil || len(s.pusher.bots) < needBots {
		return "", httpx.NewError(http.StatusServiceUnavailable, "room_fill_unavailable",
			"Mafia private rooms need house bots to fill the remaining seats after your friend joins. They are not configured on this server.")
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	m, err := s.repo.CreateWaiting(ctx, CreateMatchInput{
		PublicID: platform.NewID(platform.PrefixMafia),
		Title:    "Mafia AI Arena",
		EntryFee: entryFee,
		RakePct:  s.rakePct(),
		Seed:     seed,
		Commit:   mf.Commit(seed),
		Creator:  Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: 1},
		Private:  true,
	})
	if err != nil {
		return "", err
	}
	return m.PublicID, nil
}

// checkSeat runs spending limits. Private rooms use covering-stake when the
// limiter knows it (wallet covers the bid; no stacked reserve) — same rule as
// Goofspiel Play-a-friend.
func (s *Service) checkSeat(ctx context.Context, agentPublicID string, entryFee int64, private bool) error {
	if entryFee <= 0 {
		return nil
	}
	if private {
		type covering interface {
			CheckJoinCoveringStake(context.Context, string, int64) error
		}
		if c, ok := s.limits.(covering); ok {
			return c.CheckJoinCoveringStake(ctx, agentPublicID, entryFee)
		}
	}
	return s.limits.CheckJoin(ctx, agentPublicID, entryFee)
}

// checkPlayable is who may sit. Open lobby / ranked uses CheckEligible (via
// certify). Private rooms prefer CheckPrivateRoom when the verifier exposes it
// (local CLI or hosted URL — not ranked-endpoint-only copy).
func (s *Service) checkPlayable(ctx context.Context, agentPublicID string, fee int64, private bool) error {
	if private {
		type roomGate interface {
			CheckPrivateRoom(context.Context, string) error
		}
		if g, ok := s.ver.(roomGate); ok {
			return g.CheckPrivateRoom(ctx, agentPublicID)
		}
	}
	return s.certify(ctx, agentPublicID, fee)
}

// Join seats a developer's agent at a waiting table, enforcing every competitive and
// money gate (distinct owner, spending limit, certification).
//
// A private invite room starts once PrivateRoomMinHumans distinct owners are seated:
// house bots fill the remaining roster (outside the join lock — JoinHouseSeat takes
// its own locks) and the matched-seat / house drivers are kicked off.
func (s *Service) Join(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string) (AgentView, error) {
	view, fill, err := s.join(ctx, agentPublicID, ownerPublicID, matchPublicID, false)
	if err != nil {
		return view, err
	}
	if fill {
		if err := s.fillPrivateRoom(ctx, matchPublicID); err != nil {
			return AgentView{}, err
		}
		m, err := s.repo.Get(ctx, matchPublicID)
		if err != nil {
			return AgentView{}, ErrNotFound
		}
		return s.viewFor(ctx, m, agentPublicID), nil
	}
	return view, nil
}

// JoinHouseSeat seats a kind='house' engine bot to fill a roster the queue could not
// fill with real agents. It is NOT reachable from any HTTP route — only from
// server-side fill paths (push-play sandbox, group-matchmaking backfill) — and it must
// only ever be called with an agent from the seeded house set (migration 0063).
//
// It exists because the ordinary Join path cannot seat these bots at all: all eleven
// share the single `usr_system` owner, and Join rejects a second seat from an owner who
// already holds one (the M5 anti-collusion rule). That rule is exactly right for real
// developers and must not be relaxed for them, so the fill path gets its own entry
// point rather than a weakened check. Before this existed, filling a Mafia table failed
// on the SECOND bot with ErrSameOwner, which is why sandbox push-play returned a 409 in
// production.
//
// House seats also skip the spending-limit and certification gates: they have
// zero-balance wallets by construction (0063) and stake nothing, so asking whether they
// can afford the entry fee would fail on every paid table.
//
// Because it bypasses those gates, it will only seat an agent on the declared house
// allowlist (see Service.houseAgents). Anything else is a programming error and is
// refused rather than quietly seated without checks.
func (s *Service) JoinHouseSeat(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string) (AgentView, error) {
	if !s.houseAgents[agentPublicID] {
		return AgentView{}, ErrNotHouseAgent
	}
	view, _, err := s.join(ctx, agentPublicID, ownerPublicID, matchPublicID, true)
	return view, err
}

func (s *Service) join(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string, house bool) (AgentView, bool, error) {
	// Serialize this agent's concurrent joins (agent lock FIRST, then table lock) so
	// it can't race joins into different tables/games and bypass the per-agent limits
	// via TOCTOU. Shared "agent:join:lock:" namespace with Goofspiel. (M6)
	relAgent, okA, err := s.lock.Lock(ctx, agentJoinLockKey(agentPublicID), s.cfg.LockTTL)
	if err != nil {
		return AgentView{}, false, err
	}
	if !okA {
		return AgentView{}, false, ErrBusy
	}
	defer relAgent()

	release, ok, err := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL)
	if err != nil {
		return AgentView{}, false, err
	}
	if !ok {
		return AgentView{}, false, ErrBusy
	}
	defer release()

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, false, ErrNotFound
	}
	if m.Status != StatusWaiting {
		return AgentView{}, false, ErrNotWaiting
	}
	if m.playerByAgent(agentPublicID) != nil {
		return AgentView{}, false, ErrAlreadyJoined
	}
	if len(m.Players) >= s.cfg.RosterSize {
		return AgentView{}, false, ErrTableFull
	}
	// Reject if this owner already holds ANY seat, not just the creator's seat (m.Players[0]).
	// A 12-seat Mafia table lets one owner who controls a coordinated majority force
	// their team to win and funnel honest players' entry fees to their own agents;
	// checking only the creator let one owner take the other 11 chairs. (M5)
	//
	// House fillers are the one exemption (see JoinHouseSeat): they all share
	// `usr_system`, they never stake and can never be paid, so seating several of them
	// cannot funnel anybody's entry fee anywhere. The exemption is keyed on the
	// server-side call path, never on anything a request can set.
	if !house {
		for i := range m.Players {
			if m.Players[i].OwnerPublicID == ownerPublicID {
				return AgentView{}, false, ErrSameOwner
			}
		}
	}
	// No-stakes practice table (see CreateTable): skip the spending-limit and
	// certification gates when nothing is staked. House seats skip them on every
	// table, staked or not — they hold zero-balance wallets and stake nothing.
	if m.EntryFee > 0 && !house {
		if err := s.checkSeat(ctx, agentPublicID, m.EntryFee, m.Private); err != nil {
			return AgentView{}, false, err
		}
	}
	// As in CreateTable: certify every seat, free tables included, with only the platform's own
	// bots exempt at zero fee. Private rooms use the friend-invite playable gate.
	if !house {
		if err := s.checkPlayable(ctx, agentPublicID, m.EntryFee, m.Private); err != nil {
			return AgentView{}, false, err
		}
	}

	nextSeat := len(m.Players) + 1
	p := Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: nextSeat}
	if err := s.repo.JoinSeat(ctx, matchPublicID, p); err != nil {
		return AgentView{}, false, err
	}

	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, false, err
	}
	if len(m.Players) < s.cfg.RosterSize {
		// Private invite room: host + friend is enough — ask the caller to fill
		// bots after releasing these locks (JoinHouseSeat takes its own).
		if m.Private && !house && len(HumanPlayers(m.Players)) >= PrivateRoomMinHumans {
			return s.viewFor(ctx, m, agentPublicID), true, nil
		}
		return s.viewFor(ctx, m, agentPublicID), false, nil
	}
	if err := s.startMatch(ctx, m); err != nil {
		return AgentView{}, false, err
	}
	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, false, err
	}
	return s.viewFor(ctx, m, agentPublicID), false, nil
}

// fillPrivateRoom seats house bots into the remaining seats of an invite room
// and starts play drivers. Called after Join releases its locks.
func (s *Service) fillPrivateRoom(ctx context.Context, matchPublicID string) error {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return ErrNotFound
	}
	if m.Status != StatusWaiting {
		return nil // already started (or aborted) — join race, not an error
	}
	need := s.cfg.RosterSize - len(m.Players)
	if need <= 0 {
		return nil
	}
	if s.pusher == nil || len(s.pusher.bots) < need {
		return httpx.NewError(http.StatusServiceUnavailable, "room_fill_unavailable",
			"Mafia private rooms need house bots to fill the remaining seats. They are not configured on this server.")
	}
	real := make([]string, 0, len(m.Players))
	for _, p := range HumanPlayers(m.Players) {
		real = append(real, p.AgentPublicID)
	}
	filled := make([]string, 0, need)
	for i := 0; i < need; i++ {
		b := s.pusher.bots[i]
		if _, err := s.JoinHouseSeat(ctx, b.PublicID, b.OwnerPublicID, matchPublicID); err != nil {
			return err
		}
		filled = append(filled, b.PublicID)
	}
	all := append(append([]string{}, real...), filled...)
	s.DriveHouseSeats(matchPublicID, filled)
	s.DriveMatchedSeats(ctx, matchPublicID, real, all)
	return nil
}

func (s *Service) startMatch(ctx context.Context, m Match) error {
	seats := make([]int, len(m.Players))
	for i, p := range m.Players {
		seats[i] = p.Seat
	}
	// Only real agents stake. A house filler holds a zero-balance wallet by
	// construction (migration 0063), so handing it to StakeTable would either fail the
	// whole start on insufficient funds or, worse, overdraw a system wallet.
	stakers := make([]string, 0, len(m.Players))
	for _, p := range HumanPlayers(m.Players) {
		stakers = append(stakers, p.AgentPublicID)
	}
	state, events := s.eng.Init(m.Seed, seats)
	roles := state.Roles

	// A bot-filled table is not ranked evidence. Recorded BEFORE any coins move so a
	// failure here leaves a still-waiting, unstaked table rather than a live staked one
	// that analytics would read as fully human. Rating itself is skipped in finalize
	// from the same seat data, so this flag is the durable record for the model board,
	// not the enforcement point.
	if HasHouseSeat(m.Players) {
		if err := s.repo.MarkUnrated(ctx, m.PublicID); err != nil {
			return err
		}
	}

	// Only paid tables move coins; a zero-fee practice table stakes nothing.
	if m.EntryFee > 0 {
		if err := s.wallet.StakeTable(ctx, m.PublicID, stakers, m.EntryFee); err != nil {
			return err
		}
	}

	// The start countdown, and the reason the first phase window opens at startsAt rather
	// than now.
	//
	// A table filled its last seat and is about to play. Until now it went live in the same
	// instant, so a developer watching a terminal — or the console — saw a lobby become a
	// running match with no moment in between, and an agent that was still finishing its
	// startup lost the front of its first phase to a game already in progress.
	//
	// startsAt is ABSOLUTE and persisted, never a duration, for the reason internal/readycheck
	// gives: two surfaces each counting down from ten drift apart within seconds, and visibly
	// disagreeing about when a staked match begins is worse than showing nothing. Every
	// surface counts to this one value.
	//
	// Play is NOT gated on it — the match is active immediately and the driver runs, exactly
	// as goofspiel's startAfterReady does. What the countdown buys is that the first phase's
	// window opens when play does, so the countdown does not eat the first phase's thinking
	// time. Gating the engine on a wall-clock instant would be a second scheduler to keep
	// correct, and this needs none.
	now := s.clock.Now()
	startsAt := readycheck.StartsAt(now, readycheck.DefaultPolicy("mafia").Countdown)
	deadline := startsAt.Add(s.phaseWindow(state.Phase))
	if err := s.repo.Start(ctx, m.PublicID, roles, state, startsAt, deadline, events); err != nil {
		// Compensate the stake-then-start dual-write: the stake committed (ledger tx)
		// but flipping the match to active failed, so the coins would be stranded in a
		// full 'waiting' table with no retry. Refund immediately (idempotent disburse
		// key) so escrow is never orphaned. If the refund ALSO fails (rare double
		// fault), surface a combined error so it's visible in the request log. (G3)
		if m.EntryFee > 0 {
			if refErr := s.wallet.RefundTable(ctx, m.PublicID); refErr != nil {
				return fmt.Errorf("mafia start failed (%w) and stake refund failed (%v) — escrow stranded", err, refErr)
			}
		}
		return err
	}
	s.publish(m.PublicID, state, events)
	return nil
}

// Act applies a seat's action. expectedDay/expectedPhase are the (day, phase) the
// CLIENT computed the action for (from the state it read); when provided (day != 0)
// they are rejected if the game has since advanced — so a late action for an
// already-resolved phase is refused rather than absorbed into the current same-kind
// phase. Pass 0/"" to skip the check (internal/bot callers). (G1)
// mafiaCanonAction is the deterministic string an agent signs for one move,
// binding the current phase and the game-affecting decision (kind + target). The
// cosmetic Tone/Text (discussion voice) are deliberately excluded — the proof is
// over the move that changes the game, and both signer and verifier compute it
// identically from the same (phase, kind, target).
func mafiaCanonAction(phase string, act mf.Action) string {
	return fmt.Sprintf("%s|%s|%d", phase, act.Kind, act.Target)
}

// Act applies an agent's move. signature is the agent's Ed25519 signature over
// the canonical (match, day, seat, action) message; it is REQUIRED when the agent
// has a registered signing key and the move is not platformDriven (push-play over
// the authenticated socket/endpoint), mirroring Goofspiel.
// Act applies one action for the calling agent.
//
// Concurrency model (mirrors Goofspiel + Monopoly): the Redis lock is a FAST PATH
// that avoids wasted retries when held. Correctness comes from optimistic
// concurrency — persist's UNIQUE(match_id, seq) event-log constraint rejects a
// racing writer (ErrConcurrentUpdate) and we re-read + retry. So a Redis outage
// degrades to a few extra retries, never a stuck/lost/double-applied move.
// (Previously Mafia hard-failed on any lock error and had no retry — the one game
// out of the three that couldn't survive a Redis blip on the request path.)
func (s *Service) Act(ctx context.Context, agentPublicID, matchPublicID string, act mf.Action, expectedDay int, expectedPhase string, signature string, platformDriven bool) (AgentView, error) {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless, relying on the OCC retry.)

	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.tryAct(ctx, agentPublicID, matchPublicID, act, expectedDay, expectedPhase, signature, platformDriven)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue // another writer advanced first; re-read and retry
		}
		return view, err
	}
	return AgentView{}, ErrBusy // retries exhausted under heavy contention
}

// Say posts one line of free-form table talk during discussion.
//
// Separate from Act on purpose: this does not consume the seat's formal statement,
// does not advance the phase, and may be called as often as the agent likes while
// the floor is open. In Mafia the talking IS the game — an agent accused on the
// floor has to be able to answer immediately, not wait for a turn.
//
// persist() broadcasts the resulting message event, so spectators see the line on
// the same SSE stream, in the same tick, as everything else.
func (s *Service) Say(ctx context.Context, agentPublicID, matchPublicID, text, tone string, target int) (AgentView, error) {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.trySay(ctx, agentPublicID, matchPublicID, text, tone, target)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue
		}
		return view, err
	}
	return AgentView{}, ErrBusy
}

func (s *Service) trySay(ctx context.Context, agentPublicID, matchPublicID, text, tone string, target int) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusActive {
		return AgentView{}, ErrNotActive
	}
	p := m.playerByAgent(agentPublicID)
	if p == nil {
		return AgentView{}, ErrNotPlayer
	}
	state, events, err := s.eng.Say(m.State, p.Seat, text, tone, target)
	if err != nil {
		// Trace the REJECTION too: "tried to speak and was silenced by the rules" is a
		// different fact from "stayed quiet", and only one of them means a broken agent.
		if s.chatTracer != nil {
			reason := "illegal"
			switch {
			case errors.Is(err, mf.ErrFinished):
				reason = "match_finished"
			case !m.State.Alive[p.Seat]:
				reason = "not_alive"
			case !mf.CanSpeak(m.State.Phase):
				reason = "closed_floor"
			case strings.TrimSpace(text) == "":
				reason = "empty"
			}
			s.chatTracer.EmitAgentSayRejected(telemetry.ChatEvent{
				Game: GameName, MatchID: matchPublicID, AgentID: agentPublicID,
				Seat: p.Seat, Kind: "say", Phase: m.State.Phase, Text: text, Reason: reason,
			})
		}
		if errors.Is(err, mf.ErrFinished) {
			return AgentView{}, ErrNotActive
		}
		// Closed floor (night/voting), dead seat, or empty text.
		return AgentView{}, ErrIllegalAction
	}
	if s.chatTracer != nil {
		s.chatTracer.EmitAgentSaid(telemetry.ChatEvent{
			Game: GameName, MatchID: matchPublicID, AgentID: agentPublicID,
			Seat: p.Seat, Kind: "say", Phase: m.State.Phase, Text: text,
		})
	}
	if err := s.persist(ctx, m, state, events); err != nil {
		return AgentView{}, err
	}
	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	return s.viewFor(ctx, m, agentPublicID), nil
}

// tryAct is one optimistic-concurrency attempt: read the snapshot, validate, apply,
// persist. A racing writer surfaces as ErrConcurrentUpdate for Act's retry loop.
func (s *Service) tryAct(ctx context.Context, agentPublicID, matchPublicID string, act mf.Action, expectedDay int, expectedPhase string, signature string, platformDriven bool) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusActive {
		return AgentView{}, ErrNotActive
	}
	// Read the state under the lock, THEN reject a stale phase: if the client told us
	// which (day, phase) it acted on and the match has moved past it, that phase is
	// done — refuse the late action. (G1)
	if expectedDay != 0 && (expectedDay != m.State.Day || expectedPhase != m.State.Phase) {
		return AgentView{}, ErrStalePhase
	}
	p := m.playerByAgent(agentPublicID)
	if p == nil {
		return AgentView{}, ErrNotPlayer
	}

	// Move authenticity: if the agent registered a signing key, a request-path move
	// must carry a valid Ed25519 signature over the canonical (match, day, seat,
	// action) message — verified BEFORE the move is applied, so a forged/altered
	// move never enters the log. Platform-driven moves (push-play over the agent's
	// authenticated socket/endpoint) skip it, exactly as Goofspiel does.
	canonAction := mafiaCanonAction(m.State.Phase, act)
	signSeq, signSeat := m.State.Day, p.Seat
	var signPubkey string
	if !platformDriven {
		pubkey, kerr := s.repo.AgentSigningKey(ctx, agentPublicID)
		if kerr != nil {
			return AgentView{}, kerr
		}
		if pubkey != "" {
			if signature == "" {
				return AgentView{}, ErrSignatureRequired
			}
			if !movesig.VerifyAction(movesig.DomainMafia, pubkey, matchPublicID, signSeq, signSeat, canonAction, signature) {
				return AgentView{}, ErrBadSignature
			}
			signPubkey = pubkey
		}
	}

	// Completion binding: the action must be the one this seat's MODEL chose, whenever the
	// gateway observed a model choosing one. Checked before the engine applies it.
	//
	// Keyed by turnproof.MafiaTurn(day, phase), NOT by day — the same number the proof and
	// the decision log use. A player acts in both the night and the voting phase of one day,
	// so keying by day alone would compare a vote against the model's kill.
	//
	// Bound on the VERB AND TARGET only, not on canonAction: that string carries the phase,
	// which is the server's state rather than the model's choice, and binding it would
	// reject an honest turn over a field the model never selected.
	//
	// Runs for platform-driven moves too, unlike the signature check above — an authenticated
	// socket proves authorship, not that a model chose the action.
	if err := movebind.Enforce(ctx, s.boundMoves, slog.Default(), "mafia",
		matchPublicID, agentPublicID, turnproof.MafiaTurn(m.State.Day, m.State.Phase),
		movebind.CanonMafia(string(act.Kind), act.Target)); err != nil {
		return AgentView{}, err
	}

	// The turn this decision belongs to, and the state it was made FROM — both captured
	// BEFORE the engine acts.
	//
	// The view has to be snapshotted here rather than reusing the one built for the response
	// below: that one is the POST-move state, so storing it against the action just taken
	// files a decision under the board that resulted from it. The decision inspector then
	// shows a developer a state their move could not have been chosen from, and any scorer
	// reading these rows is scoring the wrong pair — the same defect the Monopoly path had,
	// where it drove the skill dimension to 549 scores out of 1.2M decisions.
	//
	// decisionTurn is the same expression movebind.Enforce keys on a few lines above. It was
	// being recomputed from the POST-move day/phase, so a decision's identity in the log
	// disagreed with the identity of the proof that verified it — and a night action, which
	// resolves the phase, was filed under the turn AFTER the one it was made in.
	//
	// Costs a second LoadEvents on the Act path when instrumentation is enabled. That is the
	// honest price of recording the state a decision was actually made against; the cheaper
	// version was recording the wrong one.
	decisionTurn := turnproof.MafiaTurn(m.State.Day, m.State.Phase)
	decisionDay := m.State.Day
	var decisionInput []byte
	if s.actDecisions != nil {
		decisionInput = s.marshalDecisionView(ctx, m, agentPublicID)
	}

	state, events, err := s.eng.Act(m.State, p.Seat, act)
	if err != nil {
		return AgentView{}, mapEngineErr(err)
	}
	// A night action that is NOT the phase resolver returns no events but still
	// records the submission in state (MafiaKill/NightActs). That state MUST be
	// persisted so a multi-actor night can complete across separate Act calls —
	// otherwise every submission is lost and the phase never resolves. Only a true
	// no-op (duplicate submit, no state change) is skipped.
	if len(events) == 0 && !nightSubmissionAdded(m.State, state) {
		return s.viewFor(ctx, m, agentPublicID), nil
	}
	if err := s.persist(ctx, m, state, events); err != nil {
		return AgentView{}, err
	}
	// Persist the authorship proof (best-effort; off the correctness path — the
	// move is already in the authoritative log). Enables replay re-verification.
	if signPubkey != "" {
		_ = s.repo.RecordMoveSignature(ctx, matchPublicID, signSeq, signSeat, canonAction, signature, signPubkey)
	}
	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	view := s.viewFor(ctx, m, agentPublicID)
	// Instrument AFTER the move commits: an action the engine refused returned above and is not
	// a decision the agent got to make.
	//
	// Keyed by turnproof.MafiaTurn(day, phase), NOT by day. A player acts in both the night and
	// the voting phase of the same day, so day alone would collide and each decision would
	// overwrite the previous one — Monopoly can use its engine NextSeq for this, Mafia cannot.
	// Using the same encoding the turn proof binds to also keeps a decision's identity
	// consistent with the proof that verifies it.
	s.recordActDecision(ctx, m, agentPublicID, decisionTurn, decisionDay, act, decisionInput)
	return view, nil
}

// recordActDecision persists one request-path decision, best-effort.
//
// Never blocks the move: the action is committed and any coins already moved, so a bookkeeping
// failure is logged rather than surfaced to the agent.
//
// Latency is deliberately not reported — on the request path the platform never observed the
// agent thinking, it received a finished action, so any figure would be OUR apply time. Zero
// reads as "not measured", which is true.
// marshalDecisionView renders the view an agent decided from, as stored bytes.
//
// nil rather than an error on a marshal failure: a view that cannot be serialised is one
// no scorer or inspector must see, and nil persists as NULL input_json, which every reader
// already treats as "no state recorded". A half-written view would be worse than none,
// because it would be read.
func (s *Service) marshalDecisionView(ctx context.Context, m Match, agentPublicID string) []byte {
	b, err := json.Marshal(s.viewFor(ctx, m, agentPublicID))
	if err != nil {
		return nil
	}
	return b
}

// recordActDecision persists one request-path decision.
//
// turn, day and input are all PRE-move and are passed in rather than derived from m: by the
// time this runs, m has been re-read at the post-move state. Deriving them here is exactly
// the bug this signature exists to make impossible to reintroduce.
func (s *Service) recordActDecision(ctx context.Context, m Match, agentPublicID string, turn, day int, act mf.Action, input []byte) {
	if s.actDecisions == nil {
		return
	}
	if err := s.actDecisions.RecordActDecision(ctx, ActDecision{
		MatchID: m.PublicID, AgentPublicID: agentPublicID,
		Seq:   turn,
		Round: day, Action: string(act.Kind),
		Outcome:   string(benchmark.OutcomeOK),
		InputJSON: input,
	}); err != nil {
		slog.Default().Warn("mafia: could not record act decision",
			"match", m.PublicID, "agent", agentPublicID, "err", err)
	}
}

// aggregateSeatBenchmark builds the per-seat facts the boards read, once the match is over.
//
// Called from finalize — the one point every ending passes through — and NOT from the Act path.
// Mafia ends by phase timeout at least as often as by a player's action (a night nobody answers,
// a vote that expires), so tying this to Act would have lost precisely the matches where agents
// were least responsive. Same mistake as attaching instrumentation to a driver: the hook belongs
// on the event, not on one way of reaching it.
func (s *Service) aggregateSeatBenchmark(ctx context.Context, m Match, state mf.State) {
	if s.actDecisions == nil || !state.Finished {
		return
	}
	results := make(map[string]string, len(m.Players))
	for _, p := range m.Players {
		results[p.AgentPublicID] = string(mafiaSeatResult(state, p.Seat))
	}
	if err := s.actDecisions.AggregateSeatBenchmark(ctx, m.PublicID, GameName, results); err != nil {
		slog.Default().Warn("mafia: could not aggregate seat benchmark", "match", m.PublicID, "err", err)
	}
}

// nightSubmissionAdded reports whether `after` recorded a new night submission
// (a Mafia kill vote or a Detective/Doctor/Sheriff action) versus `before`. Such
// a submission produces no events until the night resolves, so the service must
// still persist it — otherwise the multi-actor night phase can never complete.
func nightSubmissionAdded(before, after mf.State) bool {
	return len(after.MafiaKill)+len(after.NightActs) > len(before.MafiaKill)+len(before.NightActs)
}

// phaseWindow is how long the given phase may run before the sweeper forces it on.
//
// Each phase gets its own length (night is short and secret, discussion is the long
// one where the game is actually played, voting is tighter) — a single flat window
// either rushed the debate or left the table asleep for the same 45s. The lengths
// come from the engine so the countdown a spectator runs off the phase event and
// the deadline the server enforces are the same number by construction.
//
// cfg.PhaseWindow is still honoured as an explicit override when an operator sets
// one, so existing deployments and tests can pin a fixed window.
func (s *Service) phaseWindow(phase string) time.Duration {
	if s.cfg.PhaseWindow > 0 {
		return s.cfg.PhaseWindow
	}
	return mf.PhaseDuration(phase)
}

func (s *Service) persist(ctx context.Context, m Match, state mf.State, events []mf.Event) error {
	if state.Finished {
		return s.finalize(ctx, m, state, events)
	}
	deadline := s.clock.Now().Add(s.phaseWindow(state.Phase))
	if err := s.repo.Advance(ctx, m.PublicID, state, &deadline, state.Alive, events); err != nil {
		return err
	}
	s.publish(m.PublicID, state, events)
	return nil
}

func (s *Service) finalize(ctx context.Context, m Match, state mf.State, events []mf.Event) error {
	// The pool is what the STAKING seats paid in. A table that the group matcher filled
	// with house bots has more seats than stakers, and counting the fillers here would
	// compute a reward pool bigger than the escrow that backs it — settling a deficit
	// against platform revenue on every bot-filled table.
	humans := HumanPlayers(m.Players)
	econ := ComputeEconomy(len(humans), m.EntryFee, m.RakePct)
	seats := seatsFromPlayers(m.Players, state)
	rewards := ComputeRewards(state.Winner, seats, econ)

	payouts := map[string]int64{}
	for _, r := range rewards {
		if r.Eligible && r.Payout > 0 {
			if p := m.playerBySeat(r.Seat); p != nil {
				payouts[p.AgentPublicID] = r.Payout
			}
		}
	}
	// NO WINNER must refund the table, not confiscate it.
	//
	// ComputeRewards only matches a seat that is alive AND on the winning team, so a
	// finish with no such seat leaves `payouts` EMPTY — and settleMafia posts whatever
	// is unpaid as floor-division "remainder" to platform_revenue. That would move
	// every player's stake to the house on a drawn/degenerate table.
	//
	// Narrowly reachable today (checkWin ends on parity and finalByMajority always
	// names a team), but this is the exact defect that WAS live in Monopoly, on the
	// same code shape — one engine tweak or MaxDays change away from firing. Guard it
	// here rather than relying on the engine never producing the state.
	//
	// No rake on a refund: the platform fee is for settling a result, and there is none.
	platformFee := econ.PlatformFee
	if len(payouts) == 0 && m.EntryFee > 0 {
		platformFee = 0
		// Refund the stakers only. A house seat paid nothing in, so "refunding" it would
		// mint coins into the system wallet out of the other players' escrow.
		for _, p := range humans {
			payouts[p.AgentPublicID] += m.EntryFee
		}
	}

	// A zero-fee practice table staked nothing, so there is nothing to settle.
	// Hoisted so the RATING path below reads the SAME verdict this settlement used.
	// Evaluating twice could reach two different answers for one table, leaving a seat
	// unpaid but rated, or paid but unrated. Zero value is inert, so a table that never
	// reaches the evaluation below rates exactly as it did before.
	var verdict integrity.Verdict

	if m.EntryFee > 0 {
		// Money is at stake, so a seat that cannot show a single LLM-backed decision is
		// not paid from it — provided some OTHER seat at this table could. The table
		// still settles for everyone else: voiding an eleven-seat game over one seat
		// would hand a cheater a way to cancel honest players' matches.
		//
		// Applied AFTER the no-winner refund above, deliberately. A refund is returning
		// people their own stake, not paying out a result, and withholding somebody's own
		// money because of a proof they were never asked for would be theft.
		if s.integrity != nil && len(payouts) > 0 && platformFee > 0 {
			// House fillers are engine-driven and make no LLM calls, so they can never
			// show an LLM-backed decision. Including them would make every bot-filled
			// table look like it contains unproven seats, and — since the rule only
			// withholds when some OTHER seat at the table did prove itself — could flip
			// the verdict for the humans depending on who else was seated.
			agents := make([]string, 0, len(humans))
			// A seat that went dark is exempt: its zero proofs are explained by never
			// having answered, not by playing without a model. It has already been
			// punished where it counts — it abstained through every vote and its team
			// lost ground for it — and if it won ANYWAY, that win is real and gets paid.
			absent := make(map[string]bool, len(humans))
			for _, p := range humans {
				agents = append(agents, p.AgentPublicID)
				if state.SeatWasAbsent(p.Seat) {
					absent[p.AgentPublicID] = true
				}
			}
			verdict = integrity.Evaluate(ctx, s.integrity, m.PublicID, agents, absent, slog.Default())
			payouts, _ = integrity.FilterPayable(payouts, verdict, m.PublicID, slog.Default())
		}
		if err := s.wallet.SettleTable(ctx, m.PublicID, platformFee, payouts); err != nil {
			return err
		}
	}

	players := finalizePlayers(m.Players, state, rewards, m.EntryFee)
	hash := replayHash(events)
	if err := s.repo.Finish(ctx, m.PublicID, state, state.Winner, hash, players, events); err != nil {
		return err
	}
	s.publish(m.PublicID, state, events)

	// Paid tables update the per-arena skill rating (TrueSkill, N-player). The
	// result is faction-based and server-authoritative: the whole winning team ranks
	// 1, everyone else ranks 2. Idempotent per match, retried a few times since the
	// match is already durably finished + settled (nothing re-drives finalize).
	//
	// A table the matcher had to fill with house bots is NOT rated at all — not even
	// for its human seats. Beating engine bots is not the achievement that beating
	// eleven other developers' agents is, and rating it would let anyone farm skill
	// rating (and through it P-Index) by queueing when the pool is empty. Skipping the
	// rater is also what keeps such a table out of P-Index entirely, since P-Index reads
	// match_rating_changes and none are written.
	if s.rater != nil && m.EntryFee > 0 && !HasHouseSeat(m.Players) {
		// The same verdict that withheld payouts also withholds rating. On a twelve-seat
		// table only the unproven seats are dropped and the rest are rated, matching
		// FilterPayable rather than voiding everyone's game.
		res := rating.MatchResult{
			MatchPublicID: m.PublicID, Game: rating.GameMafia, Integrity: verdict,
		}
		for _, p := range players {
			placement := 2
			if p.Team == state.Winner {
				placement = 1
			}
			res.Players = append(res.Players, rating.PlayerResult{
				AgentPublicID: p.AgentPublicID, Seat: p.Seat, Placement: placement, CoinsDelta: p.CoinsDelta,
			})
		}
		if err := rateWithRetry(ctx, s.rater, res, 3, 50*time.Millisecond); err != nil {
			return err
		}
	}

	// The seat facts every board reads. Beside the settlement because this is where the result is
	// known for EVERY way a match can end, including the timeouts Mafia ends on constantly.
	s.aggregateSeatBenchmark(ctx, m, state)

	s.finish.MatchFinished(ctx, m.PublicID)
	return nil
}

// rateWithRetry applies the (idempotent) rating with a bounded retry so a transient
// error right after the match commits doesn't lose the rating update. Honors context
// cancellation between attempts.
func rateWithRetry(ctx context.Context, rater Rater, res rating.MatchResult, attempts int, backoff time.Duration) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		if err = rater.Rate(ctx, res); err == nil {
			return nil
		}
	}
	return err
}

func (s *Service) HandleTimeout(ctx context.Context, matchPublicID string) error {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return nil // another instance holds it; skip this round
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless so the timeout still fires
	// and a paid table's escrow can't stay wedged. ForceTimeout always produces
	// events, so the persist below is protected by the UNIQUE(match_id,seq) OCC:
	// a racing writer surfaces as ErrConcurrentUpdate, treated as a no-op.)

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil || m.Status != StatusActive {
		return nil
	}
	if m.RoundDeadline == nil || s.clock.Now().Before(*m.RoundDeadline) {
		return nil
	}

	state, events, err := s.eng.ForceTimeout(m.State, m.Seed)
	if err != nil {
		return err
	}
	// Deliberately NOT gated on len(events) > 0. Monopoly carried the identical guard and it
	// wedged five staked tables for up to two days: its engine advanced the state while
	// emitting nothing observable, the service read "no events" as "nothing happened", and
	// the advance was discarded on every sweep. No Mafia table has been found stuck this
	// way, so this is preventive rather than a reproduced defect — but the failure mode is
	// silent, holds escrow, and the guard buys nothing, since persist() is OCC and re-arms
	// the deadline.

	if err := s.persist(ctx, m, state, events); err != nil && !errors.Is(err, ErrConcurrentUpdate) {
		return err
	}
	return nil
}

func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
	// Post-outage grace: a missed turn forfeits the seat's stake, which is right when
	// an agent quits but wrong when WE were unreachable. During the grace window after
	// a detected platform outage we skip the sweep, so deadlines that lapsed while
	// nobody could play are not turned into losses. Nil tracker ⇒ unchanged behaviour.
	if s.liveness.InGrace() {
		return 0, nil
	}

	ids, err := s.repo.ListActiveExpired(ctx, GameName, s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	// Best-effort per table: one wedged/corrupt table (e.g. a persist error) must not
	// block the timeout — and therefore the escrow release — of every OTHER expired
	// table in the batch. Collect and continue, mirroring match.Service.SweepExpired.
	var swept int
	var errs error
	for _, id := range ids {
		if err := s.HandleTimeout(ctx, id); err != nil {
			errs = errors.Join(errs, fmt.Errorf("timeout %s: %w", id, err))
			continue
		}
		swept++
	}
	return swept, errs
}

// SweepStaleWaiting aborts waiting tables that have sat past WaitingTTL without
// filling their roster, so an agent that joined a lobby that can never gather 12
// distinct-owner players isn't stuck forever. No stakes are escrowed before a table
// starts, so nothing is refunded — the table is simply aborted and the agents freed.
func (s *Service) SweepStaleWaiting(ctx context.Context, limit int) (int, error) {
	cutoff := s.clock.Now().Add(-s.cfg.WaitingTTL)
	return s.repo.ExpireStaleWaiting(ctx, cutoff, limit)
}

// Notifier wakes long-polling State callers when the match changes. Satisfied by
// *store.Notifier (structural). Nil ⇒ State returns immediately (no long-poll).
type Notifier interface {
	Notify(matchPublicID string)
	Subscribe(matchPublicID string) (events <-chan struct{}, cancel func())
}

// SetNotifier installs the wake-up channel (called once at wiring time).
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// publish broadcasts events to spectators AND wakes any long-polling State callers.
// publish pushes game events AND the resulting "thinking…" set.
//
// The pending set travels with the events that changed it, in the same tick, so a
// seat that just spoke stops being pending exactly as its message appears rather
// than a frame later. Taking state here (instead of a separate call) means no future
// call site can emit events and silently leave a stale indicator behind.
func (s *Service) publish(matchID string, state mf.State, events []mf.Event) {
	s.bcast.Broadcast(matchID, events)
	if state.Finished {
		s.bcast.BroadcastPending(matchID, nil) // nobody is thinking any more
	} else {
		s.bcast.BroadcastPending(matchID, mf.PendingActors(state))
	}
	if s.notify != nil {
		s.notify.Notify(matchID)
	}
}

func (s *Service) State(ctx context.Context, matchPublicID, viewerAgent string, wait bool, timeout time.Duration) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if !wait || s.notify == nil || m.Status == StatusFinished {
		return s.viewFor(ctx, m, viewerAgent), nil
	}
	// Long-poll: return as soon as the match state changes (a seat acted, a night
	// submission or vote landed, chat advanced, a phase/day rolled, or the match
	// finished) or the timeout elapses. A publish wakes us immediately; a 2s
	// safety-tick backstop + a state-version compare means a missed/coalesced wake
	// (or a Redis blip) still returns within ~2s instead of blocking the full
	// timeout. The version includes the within-phase counts (night acts, votes,
	// messages), so it catches sub-phase progress a plain day/phase key would miss.
	wake, cancel := s.notify.Subscribe(matchPublicID)
	defer cancel()
	startVer := stateVersion(m.State)
	deadline := s.clock.Now().Add(timeout)
	for {
		remaining := deadline.Sub(s.clock.Now())
		if remaining <= 0 {
			break
		}
		tick := remaining
		if tick > 2*time.Second {
			tick = 2 * time.Second // backstop in case a wake-up is missed
		}
		select {
		case <-ctx.Done():
			return s.viewFor(ctx, m, viewerAgent), nil
		case <-wake:
		case <-time.After(tick):
		}
		m, err = s.repo.Get(ctx, matchPublicID)
		if err != nil {
			return AgentView{}, ErrNotFound
		}
		if stateVersion(m.State) != startVer || m.Status == StatusFinished {
			break
		}
	}
	return s.viewFor(ctx, m, viewerAgent), nil
}

// stateVersion is a cheap change key over the fields already loaded by repo.Get.
// It advances on ANY progress — event seq, night submissions, votes, chat, and
// phase/day rolls — so a long-poll backstop can detect within-phase moves (e.g. it
// becoming your turn to vote), not just coarse phase changes.
func stateVersion(st mf.State) string {
	return fmt.Sprintf("%d/%s/%d/%d/%d/%d/%t",
		st.Day, st.Phase, st.NextSeq, len(st.NightActs), len(st.Votes), st.Messages, st.Finished)
}

// Roster returns the public seat → agent identities for a match. Public data only
// (name, owner, avatar, alive) — roles are hidden information and never included,
// so this is safe to serve to any spectator at any point in a live match.
func (s *Service) Roster(ctx context.Context, matchPublicID string) ([]RosterSeat, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return nil, ErrNotFound
	}
	return RosterOf(m.Players, m.State.Alive), nil
}

func (s *Service) Economy(ctx context.Context, matchPublicID string) (EconomySnapshot, []RewardRow, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return EconomySnapshot{}, nil, ErrNotFound
	}
	// Staking seats only — the same count finalize settles against. Reporting the roster
	// here instead would publish a gross pool bigger than the coins actually escrowed on
	// any bot-filled table, and the spectator economy view is exactly where a fabricated
	// pool figure becomes something people believe.
	econ := ComputeEconomy(len(HumanPlayers(m.Players)), m.EntryFee, m.RakePct)
	if m.Status != StatusFinished {
		return econ, nil, nil
	}
	seats := seatsFromPlayers(m.Players, m.State)
	return econ, ComputeRewards(m.WinnerTeam, seats, econ), nil
}

// Replay returns the full, unredacted event log. Internal use only (audit,
// existence checks); do NOT serve this directly to public clients mid-match —
// use ReplayPublic.
func (s *Service) Replay(ctx context.Context, matchPublicID string) ([]mf.Event, error) {
	return s.repo.LoadEvents(ctx, matchPublicID, -1)
}

// ReplayPublic returns the event log for public/spectator consumption. While the
// match is live it is redacted so hidden information never leaves the server;
// once the match is finished the full log — including the night actions that
// reveal every role — is returned for auditing, exactly as the spec requires
// ("Role Assignment (hidden until match end)").
func (s *Service) ReplayPublic(ctx context.Context, matchPublicID string) ([]mf.Event, error) {
	events, err := s.repo.LoadEvents(ctx, matchPublicID, -1)
	if err != nil {
		return nil, err
	}
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return nil, ErrNotFound
	}
	if m.Status == StatusFinished {
		return events, nil
	}
	return mf.RedactLog(events), nil
}

// ReplayTimed is ReplayPublic with each event's original timestamp and the roster,
// i.e. everything needed to replay a past match exactly as it happened — who said
// what, to whom, and with the same pauses between lines.
//
// Redaction is identical to ReplayPublic: night secrets stay hidden until the match
// is finished. Timing must never become a side channel that leaks them.
func (s *Service) ReplayTimed(ctx context.Context, matchPublicID string) ([]TimedEvent, []RosterSeat, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	timed, err := s.repo.LoadEventsTimed(ctx, matchPublicID)
	if err != nil {
		return nil, nil, err
	}
	roster := RosterOf(m.Players, m.State.Alive)
	if m.Status == StatusFinished {
		return timed, roster, nil
	}
	// Mid-match: drop the same events RedactLog would, keeping their timestamps.
	keep := make(map[int]bool, len(timed))
	for _, ev := range mf.RedactLog(eventsOf(timed)) {
		keep[ev.Seq] = true
	}
	out := make([]TimedEvent, 0, len(timed))
	for _, te := range timed {
		if keep[te.Event.Seq] {
			out = append(out, te)
		}
	}
	return out, roster, nil
}

func eventsOf(timed []TimedEvent) []mf.Event {
	out := make([]mf.Event, 0, len(timed))
	for _, te := range timed {
		out = append(out, te.Event)
	}
	return out
}

func (s *Service) Live(ctx context.Context) ([]LiveMatch, error) {
	return s.repo.LiveMatches(ctx)
}

func (s *Service) Cancel(ctx context.Context, agentPublicID, matchPublicID string) error {
	release, ok, err := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	defer release()

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return ErrNotFound
	}
	if m.Status != StatusWaiting {
		return ErrNotWaiting
	}
	if len(m.Players) == 0 || m.Players[0].AgentPublicID != agentPublicID {
		return ErrNotCreator
	}
	return s.repo.CancelWaiting(ctx, matchPublicID, agentPublicID)
}

// viewFor builds the redacted per-seat view. For a seated agent in a live or
// finished match it loads the event log and delegates to the engine's BuildView,
// which decides exactly what slice of the transcript and which private night
// results the seat is entitled to. Non-seated callers (and waiting matches) get
// only the public envelope.
func (s *Service) viewFor(ctx context.Context, m Match, viewerAgent string) AgentView {
	v := s.baseView(m, viewerAgent)
	p := m.playerByAgent(viewerAgent)
	if p == nil || (m.Status != StatusActive && m.Status != StatusFinished) {
		return v
	}
	log, err := s.repo.LoadEvents(ctx, m.PublicID, -1)
	if err != nil {
		return v // fail safe: never reveal more than the base view on error
	}
	bv := mf.BuildView(m.State, p.Seat, log)
	v.Allies = bv.Allies
	v.Legal = bv.Legal
	v.Public = bv.Public
	v.Private = bv.Private
	// Role-scoped by BuildView itself — only a doctor's view carries one, only a mafia's the
	// other — so copying both unconditionally cannot leak either to a seat not entitled to it.
	v.CannotProtect = bv.CannotProtect
	v.AllyKills = bv.AllyKills
	return v
}

func (s *Service) baseView(m Match, viewerAgent string) AgentView {
	v := AgentView{
		MatchID: m.PublicID, Status: m.Status,
		Day: m.State.Day, Phase: m.State.Phase,
		Alive: cloneAlive(m.State.Alive), EntryFee: m.EntryFee,
		// Staking seats only, matching Economy() and finalize — an agent reading its own
		// view must see the pool it can actually win, not one inflated by house fillers.
		Economy: ComputeEconomy(len(HumanPlayers(m.Players)), m.EntryFee, m.RakePct),
		// Public identities only — roles stay in the redacted per-seat view.
		Roster: RosterOf(m.Players, m.State.Alive),
		// Shipped on EVERY view, in every status, so a client can measure its clock offset
		// once and render any absolute instant correctly. Only useful in company: StartsAt
		// alone is unreadable on a device whose clock is minutes out, which is most of them.
		ServerNow: s.clock.Now().UTC(),
		StartsAt:  m.StartsAt,
	}
	if m.Status == StatusActive {
		// Who the table is waiting on, straight from the rules. PendingActors was
		// already computed for the runner and simply never surfaced.
		v.Pending = mf.PendingActors(m.State)
	}
	if m.Status == StatusActive {
		v.Deadline = m.RoundDeadline
		// Full phase length + whether talking is allowed right now. Together with
		// DeadlineMs this is everything a client needs to render "NIGHT · 0:23" and
		// disable the composer, without hardcoding the rules on the client.
		window := s.phaseWindow(m.State.Phase)
		v.PhaseDurationMs = window.Milliseconds()
		v.CanSpeak = mf.CanSpeak(m.State.Phase)
		if m.RoundDeadline != nil {
			if rem := m.RoundDeadline.Sub(s.clock.Now()).Milliseconds(); rem > 0 {
				v.DeadlineMs = rem
			}
			// The last-seconds cue, counted back from the deadline rather than forward from a
			// phase start we do not store. Same instant either way — the deadline IS the start
			// plus this window, set together in persist() — and counting back needs one stored
			// value instead of two.
			//
			// Only on a phase where someone still owes an action, which is DERIVED from
			// PendingActors rather than a hardcoded phase list. Morning and result are
			// announcement beats — PendingActors has no case for them and returns nil — and
			// they are 8s long, so a "nearly over" notice there is pure noise on the wire.
			// Deriving it also means a rules change cannot leave this behind: a new decision
			// phase gets a warning automatically, and a phase that stops demanding an action
			// stops warning.
			if len(v.Pending) > 0 {
				if lead := deadline.WarnLead(window); lead > 0 {
					at := m.RoundDeadline.Add(-lead)
					v.WarnAt = &at
					if left := at.Sub(s.clock.Now()).Milliseconds(); left > 0 {
						v.WarnInMs = left
					}
				}
			}
		}
		// The engine clears Votes when voting opens, so this is exactly the
		// current round's ballots. Surface the raw votes + an aggregated tally.
		if len(m.State.Votes) > 0 {
			votes := make(map[int]int, len(m.State.Votes))
			tally := map[int]int{}
			for voter, target := range m.State.Votes {
				votes[voter] = target
				if target > 0 {
					tally[target]++
				}
			}
			v.Votes = votes
			v.VoteTally = tally
		}
	}
	if p := m.playerByAgent(viewerAgent); p != nil {
		v.YourSeat = p.Seat
		if m.Status == StatusActive || m.Status == StatusFinished {
			v.YourRole = m.State.Roles[p.Seat]
		}
	}
	if m.Status == StatusFinished {
		seats := seatsFromPlayers(m.Players, m.State)
		v.Result = &EconomyResult{
			Winner: m.WinnerTeam, Rewards: ComputeRewards(m.WinnerTeam, seats, v.Economy),
		}
	}
	return v
}

func mapEngineErr(err error) error {
	switch {
	case errors.Is(err, mf.ErrIllegalAction):
		return ErrIllegalAction
	case errors.Is(err, mf.ErrFinished):
		return ErrNotActive
	case errors.Is(err, mf.ErrNotAlive):
		return ErrSeatEliminated
	case errors.Is(err, mf.ErrNotPending):
		return ErrNoPendingAction
	default:
		// ErrUnknownSeat stays here deliberately: a seat index the engine does not recognise is
		// not an agent mistake to explain, and echoing it back would confirm which indices exist.
		return ErrIllegalAction
	}
}

func seatsFromPlayers(players []Player, st mf.State) []SeatInfo {
	out := make([]SeatInfo, len(players))
	for i, p := range players {
		out[i] = SeatInfo{
			Seat: p.Seat, AgentPublicID: p.AgentPublicID, OwnerPublicID: p.OwnerPublicID,
			Role: st.Roles[p.Seat], Team: mf.TeamOf(st.Roles[p.Seat]), Alive: st.Alive[p.Seat],
			IsHouse: p.IsHouse,
		}
	}
	return out
}

func finalizePlayers(players []Player, st mf.State, rewards []RewardRow, entryFee int64) []Player {
	bySeat := map[int]RewardRow{}
	for _, r := range rewards {
		bySeat[r.Seat] = r
	}
	out := make([]Player, len(players))
	copy(out, players)
	for i := range out {
		out[i].Role = st.Roles[out[i].Seat]
		out[i].Team = mf.TeamOf(out[i].Role)
		out[i].Alive = st.Alive[out[i].Seat]
		switch {
		case out[i].IsHouse:
			// A house filler neither paid an entry fee nor can be paid, so its coin
			// movement is exactly zero. Writing -entryFee here would persist a
			// fabricated loss to match_players.coins_delta, which is read back as fact
			// by the spectator economy view and the lifetime-winnings tile — the same
			// class of phantom-number bug the zero-fee guard in ComputeEconomy exists
			// to prevent.
			out[i].CoinsDelta = 0
		case bySeat[out[i].Seat].Eligible:
			out[i].CoinsDelta = bySeat[out[i].Seat].Payout - entryFee
		default:
			out[i].CoinsDelta = -entryFee
		}
	}
	return out
}

func cloneAlive(m map[int]bool) map[int]bool {
	if m == nil {
		return nil
	}
	out := make(map[int]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func replayHash(events []mf.Event) string {
	b, _ := json.Marshal(events)
	return mf.Commit(b)
}

type NoopLimits struct{}

func (NoopLimits) CheckJoin(context.Context, string, int64) error { return nil }

type AllowAllVerifier struct{}

func (AllowAllVerifier) CheckEligible(context.Context, string) error { return nil }

type NoopFinishHook struct{}

func (NoopFinishHook) MatchFinished(context.Context, string) {}

// SetRakeSource wires the live platform commission (Super Admin → config bus) into
// new-match pricing. Nil, or a value outside 0..50%, falls back to the static config
// — the bound guards against a corrupt or hostile publisher setting a 100% rake and
// taking the entire pot.
func (s *Service) SetRakeSource(f func() int) { s.rake = f }

// rakePct resolves the commission for a match about to be created.
func (s *Service) rakePct() int {
	if s.rake != nil {
		if p := s.rake(); p >= 0 && p <= 50 {
			return p
		}
	}
	return s.cfg.PlatformFeePct
}

// mafiaSeatResult maps a finished state onto one seat's outcome.
//
// Mafia is a TEAM game: the seat's result is its team's result, so a Mafioso and a Villager in
// the same match never share an outcome and two seats on the same team always do. That is also
// exactly why Mafia cannot enter the pairwise model board — see internal/modelboard/build.go —
// but the per-seat result is still needed for win rates, ratings and the P-Index.
func mafiaSeatResult(st mf.State, seat int) benchmark.Result {
	if st.Winner == "" {
		// Finished with no winner recorded: report a draw rather than inventing a loss for
		// everyone, which is what a zero value would have meant.
		return benchmark.ResultDraw
	}
	if mf.TeamOf(st.Roles[seat]) == st.Winner {
		return benchmark.ResultWin
	}
	return benchmark.ResultLoss
}

// SetHouseRoster injects the platform's own agent ids. Nil means no exemption at all, which is
// the safe default: without it every seat, house or not, must certify.
func (s *Service) SetHouseRoster(ids []string) {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			m[id] = true
		}
	}
	s.house = m
}

// mustCertify reports whether this seat has to prove it is LLM-backed.
//
// Paid tables: ALWAYS, without exception. A house agent reaching this with a fee is a bug
// somewhere else, and failing it here is the correct outcome rather than something to smooth
// over.
func (s *Service) mustCertify(agentPublicID string, fee int64) bool {
	if fee > 0 {
		return true
	}
	return !s.house[agentPublicID]
}

// certify enforces the LLM check unless this is one of the platform's own bots at a free table.
//
// The check used to sit INSIDE `if entryFee > 0`, so every practice table skipped it — for the
// user as well as the house. A practice match is not inert: it writes decision and benchmark
// rows that feed the P-Index, the model board and the deception index, so a scripted agent
// farming free tables builds a public record it did not earn. That is the same fraud as winning
// coins with one, paid in reputation instead of currency.
func (s *Service) certify(ctx context.Context, agentPublicID string, fee int64) error {
	if !s.mustCertify(agentPublicID, fee) {
		return nil
	}
	return s.ver.CheckEligible(ctx, agentPublicID)
}
