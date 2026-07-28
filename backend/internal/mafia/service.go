package mafia

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/liveness"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/rating"
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
}

// Service drives the Mafia match lifecycle.
type Service struct {
	repo   Repo
	lock   Locker
	limits Limits
	wallet Wallet
	bcast  Broadcaster
	ver    Verifier
	finish FinishHook
	clock  platform.Clock
	cfg    Config
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
	// notify wakes long-polling State callers on a state change. Nil ⇒ no long-poll.
	notify Notifier
	// rater applies per-arena skill ratings when a paid table finalizes. Nil ⇒
	// ratings skipped (tests / notifier-less builds). rating.Service satisfies it.
	rater Rater
	// liveness suppresses forfeits during the grace window after a detected platform
	// outage. Nil is valid and means "no grace".
	liveness *liveness.Tracker
	// chatTracer records table talk to Lens. Nil ⇒ telemetry off.
	chatTracer ChatTracer
	// decisionTracer records each resolved agent turn. Nil ⇒ telemetry off.
	decisionTracer DecisionTracer
}

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

// SetLiveness installs the post-outage grace tracker (called once at wiring time).
func (s *Service) SetLiveness(t *liveness.Tracker) { s.liveness = t }

// Rater applies a finished ranked table's TrueSkill change to the Mafia arena.
// Satisfied directly by *rating.Service.
type Rater interface {
	Rate(ctx context.Context, res rating.MatchResult) error
}

// SetRater installs the rating hook (called once at wiring time).
func (s *Service) SetRater(r Rater) { s.rater = r }

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

func (s *Service) CreateTable(ctx context.Context, agentPublicID, ownerPublicID string, entryFee int64) (string, error) {
	if entryFee < 0 {
		entryFee = 0
	}
	// A zero-fee table is a no-stakes practice/sandbox table (nothing staked, no
	// payout, no rating change): skip the spending-limit and certification gates,
	// matching the Goofspiel sandbox and Monopoly's zero-fee tables. Paid tables
	// enforce both.
	if entryFee > 0 {
		if err := s.limits.CheckJoin(ctx, agentPublicID, entryFee); err != nil {
			return "", err
		}
		if err := s.ver.CheckEligible(ctx, agentPublicID); err != nil {
			return "", err
		}
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

func (s *Service) Join(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string) (AgentView, error) {
	// Serialize this agent's concurrent joins (agent lock FIRST, then table lock) so
	// it can't race joins into different tables/games and bypass the per-agent limits
	// via TOCTOU. Shared "agent:join:lock:" namespace with Goofspiel. (M6)
	relAgent, okA, err := s.lock.Lock(ctx, agentJoinLockKey(agentPublicID), s.cfg.LockTTL)
	if err != nil {
		return AgentView{}, err
	}
	if !okA {
		return AgentView{}, ErrBusy
	}
	defer relAgent()

	release, ok, err := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL)
	if err != nil {
		return AgentView{}, err
	}
	if !ok {
		return AgentView{}, ErrBusy
	}
	defer release()

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusWaiting {
		return AgentView{}, ErrNotWaiting
	}
	if m.playerByAgent(agentPublicID) != nil {
		return AgentView{}, ErrAlreadyJoined
	}
	if len(m.Players) >= s.cfg.RosterSize {
		return AgentView{}, ErrTableFull
	}
	// Reject if this owner already holds ANY seat, not just the creator's seat (m.Players[0]).
	// A 12-seat Mafia table lets one owner who controls a coordinated majority force
	// their team to win and funnel honest players' entry fees to their own agents;
	// checking only the creator let one owner take the other 11 chairs. (M5)
	for i := range m.Players {
		if m.Players[i].OwnerPublicID == ownerPublicID {
			return AgentView{}, ErrSameOwner
		}
	}
	// No-stakes practice table (see CreateTable): skip the spending-limit and
	// certification gates when nothing is staked.
	if m.EntryFee > 0 {
		if err := s.limits.CheckJoin(ctx, agentPublicID, m.EntryFee); err != nil {
			return AgentView{}, err
		}
		if err := s.ver.CheckEligible(ctx, agentPublicID); err != nil {
			return AgentView{}, err
		}
	}

	nextSeat := len(m.Players) + 1
	p := Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: nextSeat}
	if err := s.repo.JoinSeat(ctx, matchPublicID, p); err != nil {
		return AgentView{}, err
	}

	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	if len(m.Players) < s.cfg.RosterSize {
		return s.viewFor(ctx, m, agentPublicID), nil
	}
	if err := s.startMatch(ctx, m); err != nil {
		return AgentView{}, err
	}
	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	return s.viewFor(ctx, m, agentPublicID), nil
}

func (s *Service) startMatch(ctx context.Context, m Match) error {
	seats := make([]int, len(m.Players))
	agents := make([]string, len(m.Players))
	for i, p := range m.Players {
		seats[i] = p.Seat
		agents[i] = p.AgentPublicID
	}
	state, events := s.eng.Init(m.Seed, seats)
	roles := state.Roles

	// Only paid tables move coins; a zero-fee practice table stakes nothing.
	if m.EntryFee > 0 {
		if err := s.wallet.StakeTable(ctx, m.PublicID, agents, m.EntryFee); err != nil {
			return err
		}
	}

	deadline := s.clock.Now().Add(s.phaseWindow(state.Phase))
	if err := s.repo.Start(ctx, m.PublicID, roles, state, deadline, events); err != nil {
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
	return s.viewFor(ctx, m, agentPublicID), nil
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
	econ := ComputeEconomy(len(m.Players), m.EntryFee, m.RakePct)
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
		for _, p := range m.Players {
			payouts[p.AgentPublicID] += m.EntryFee
		}
	}

	// A zero-fee practice table staked nothing, so there is nothing to settle.
	if m.EntryFee > 0 {
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
	if s.rater != nil && m.EntryFee > 0 {
		res := rating.MatchResult{MatchPublicID: m.PublicID, Game: rating.GameMafia}
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
	if err != nil || len(events) == 0 {
		return err
	}
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
	econ := ComputeEconomy(len(m.Players), m.EntryFee, m.RakePct)
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
	return v
}

func (s *Service) baseView(m Match, viewerAgent string) AgentView {
	v := AgentView{
		MatchID: m.PublicID, Status: m.Status,
		Day: m.State.Day, Phase: m.State.Phase,
		Alive: cloneAlive(m.State.Alive), EntryFee: m.EntryFee,
		Economy: ComputeEconomy(len(m.Players), m.EntryFee, m.RakePct),
		// Public identities only — roles stay in the redacted per-seat view.
		Roster: RosterOf(m.Players, m.State.Alive),
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
		v.PhaseDurationMs = s.phaseWindow(m.State.Phase).Milliseconds()
		v.CanSpeak = mf.CanSpeak(m.State.Phase)
		if m.RoundDeadline != nil {
			if rem := m.RoundDeadline.Sub(s.clock.Now()).Milliseconds(); rem > 0 {
				v.DeadlineMs = rem
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
	default:
		return ErrIllegalAction
	}
}

func seatsFromPlayers(players []Player, st mf.State) []SeatInfo {
	out := make([]SeatInfo, len(players))
	for i, p := range players {
		out[i] = SeatInfo{
			Seat: p.Seat, AgentPublicID: p.AgentPublicID, OwnerPublicID: p.OwnerPublicID,
			Role: st.Roles[p.Seat], Team: mf.TeamOf(st.Roles[p.Seat]), Alive: st.Alive[p.Seat],
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
		if r, ok := bySeat[out[i].Seat]; ok && r.Eligible {
			out[i].CoinsDelta = r.Payout - entryFee
		} else {
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
