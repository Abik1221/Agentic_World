package match

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/liveness"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/replay"
)

// matchFinishedPayload is the match.finished event body (competitive matches).
// WinnerAgent is empty on a tie.
type matchFinishedPayload struct {
	MatchID     string `json:"match_id"`
	Game        string `json:"game"`
	WinnerAgent string `json:"winner_agent"`
}

// Config tunes the match loop.
type Config struct {
	MoveWindow time.Duration
	RakePct    int
	Rounds     int
	LockTTL    time.Duration
}

// Service drives the match lifecycle. It is stateless; all state lives in the
// repo (snapshot + event log) and mutations are serialized by a per-match lock.
type Service struct {
	repo   Repo
	lock   Locker
	limits Limits
	wallet Wallet
	bcast  Broadcaster
	notify Notifier
	ver    Verifier
	rater  Rater
	finish FinishHook
	bot    Bot
	style  StyleRecorder // nil ⇒ style aggregates not recorded (optional, best-effort)
	driver *driver       // nil ⇒ paired agents self-drive (auto-drive disabled)
	clock  platform.Clock
	cfg    Config
	// rake, when set, supplies the LIVE platform commission for a new match, so the
	// admin's fee control actually moves money instead of being decorative. Nil ⇒ the
	// static config value. Read at creation only; the result is persisted on the match
	// and settlement reads it back, so a mid-match change never re-prices a live table.
	rake func() int
	// liveness suppresses forfeits during the grace window after a detected platform
	// outage. Nil is valid and means "no grace" — see SweepExpired.
	liveness *liveness.Tracker
	// turns mints the per-turn proof token that binds a gateway LLM call to one
	// decision. Nil ⇒ no token is issued, so nothing can be proven LLM-backed.
	turns TurnMinter
	// integrity counts an agent's proven-LLM decisions. Nil ⇒ no ranked integrity
	// check at all.
	integrity IntegrityChecker
	// integrityMinPct is the share of a ranked match's decisions that must be proven
	// LLM-backed. INCLUSIVE ("at least this share"), which is what makes 100 a usable
	// setting — a strictly-greater rule could never be satisfied by 13 of 13. A
	// MAJORITY is therefore configured as 51, not 50.
	//
	// 0 DISABLES enforcement, which is the correct default until the proof has
	// actually shipped to developers: an SDK that sends no proof makes every honest
	// agent look deterministic, and enforcing then would void real matches wholesale.
	integrityMinPct int
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
// Without it the service forfeits exactly as before, which is the safe default.
func (s *Service) SetLiveness(t *liveness.Tracker) { s.liveness = t }

// SetTurnMinter installs the per-turn proof minter. Must be called BEFORE
// EnableRankedDrive, which copies it onto the driver — otherwise views ship without
// a token and no decision can be proven LLM-backed.
func (s *Service) SetTurnMinter(m TurnMinter) { s.turns = m }

// SetIntegrityCheck enables the ranked LLM-backing check. minPct is the share of a
// match's decisions that must be PROVEN LLM-backed; 0 leaves enforcement off and only
// the measurement (recorded by the gateway) accumulates.
//
// Turn this on only after looking at what honest agents actually score. Batching,
// caching and retry patterns are all legitimate and produce fewer proofs than
// decisions, so the right threshold is an observation, not a guess.
func (s *Service) SetIntegrityCheck(c IntegrityChecker, minPct int) {
	s.integrity, s.integrityMinPct = c, minPct
}

// rankedIntegrityFailed reports whether a finished ranked match should be VOIDED
// because a seat cannot show it was played by an LLM, and names the seat if so.
//
// Pyyol is an arena for AI agents and ranked carries real money, so a hand-written
// script taking stakes from developers who are genuinely paying for inference is the
// thing this exists to stop. decisions is how many moves each seat actually made.
//
// FAILS OPEN. If the count cannot be read the match settles normally, because the
// alternative — voiding on a database hiccup — would cancel legitimate matches in
// bulk during an outage. A cheat that slips through is still recorded and reviewable;
// a wrongly voided match is a broken product for everyone playing at that moment.
func (s *Service) rankedIntegrityFailed(ctx context.Context, m Match, decisions int) (bool, string) {
	if s.integrity == nil || s.integrityMinPct <= 0 || decisions <= 0 {
		return false, ""
	}
	for _, p := range m.Players {
		bound, err := s.integrity.BoundDecisions(ctx, m.PublicID, p.AgentPublicID)
		if err != nil {
			slog.Warn("match: integrity check unavailable; settling normally",
				"match", m.PublicID, "agent", p.AgentPublicID, "error", err)
			return false, ""
		}
		if bound*100 < decisions*s.integrityMinPct {
			return true, p.AgentPublicID
		}
	}
	return false, ""
}

// StyleRecorder accumulates per-agent behavioral style aggregates at match finish
// (read-only descriptive metrics; never affects play or money). Optional.
type StyleRecorder interface {
	RecordStyle(ctx context.Context, agentPublicID, game string, aggression, efficiency int) error
}

// SetStyleRecorder installs the (optional) style aggregator. Nil keeps it off.
func (s *Service) SetStyleRecorder(r StyleRecorder) {
	if r != nil {
		s.style = r
	}
}

// SetNotifier installs the low-latency wake-up channel for long-polling agents
// after construction (mirrors wallet.SetPayoutGate). Nil keeps the no-op default,
// so tests and notifier-less deployments still work (long-poll falls back to its
// timeout). Wired in main once Redis is available.
func (s *Service) SetNotifier(n Notifier) {
	if n != nil {
		s.notify = n
	}
}

// SetBot installs the house-agent move picker used by sandbox matches, after
// construction (mirrors SetNotifier). Nil keeps the NoopBot default, so existing
// callers and tests that don't use sandbox mode are unaffected.
func (s *Service) SetBot(b Bot) {
	if b != nil {
		s.bot = b
	}
}

func New(repo Repo, lock Locker, limits Limits, wallet Wallet, bcast Broadcaster, ver Verifier, rater Rater, finish FinishHook, clock platform.Clock, cfg Config) *Service {
	if cfg.MoveWindow <= 0 {
		cfg.MoveWindow = 20 * time.Second
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = 10 * time.Second
	}
	if cfg.Rounds <= 0 {
		cfg.Rounds = 13
	}
	if rater == nil {
		rater = NoopRater{}
	}
	if finish == nil {
		finish = NoopFinishHook{}
	}
	return &Service{repo: repo, lock: lock, limits: limits, wallet: wallet, bcast: bcast, notify: NoopNotifier{}, ver: ver, rater: rater, finish: finish, bot: NoopBot{}, clock: clock, cfg: cfg}
}

func lockKey(matchPublicID string) string { return "match:lock:" + matchPublicID }

// agentJoinLockKey serializes a single agent's concurrent join attempts across the
// whole platform (shared prefix with mafia so the two games can't race the same
// agent into one match each). (M6)
func agentJoinLockKey(agentPublicID string) string { return "agent:join:lock:" + agentPublicID }

// publish pushes game events AND the resulting "thinking…" set.
//
// Both seats seal simultaneously in Goofspiel, so the pending set is normally BOTH
// agents; a seat drops out of it the moment its card_sealed lands, in the same tick.
func (s *Service) publish(matchPublicID string, state gs.State, events []gs.Event) {
	s.bcast.Broadcast(matchPublicID, events)
	s.bcast.BroadcastPending(matchPublicID, unsealedSeats(state))
	s.notify.Notify(matchPublicID)
}

// unsealedSeats is who still owes a card — derived from state, never invented.
func unsealedSeats(state gs.State) []int {
	if state.Finished {
		return nil
	}
	var out []int
	for seat := 0; seat < 2; seat++ {
		if state.Sealed[seat] == nil {
			out = append(out, seat)
		}
	}
	return out
}

func (s *Service) engine(m Match) *gs.Engine {
	cfg := gs.DefaultConfig()
	cfg.Rounds = m.TotalRounds
	cfg.FairnessMode = m.FairnessMode
	return gs.New(cfg)
}

// Lobby lists open matches the caller may join.
func (s *Service) Lobby(ctx context.Context, game string, bid int64, ownerPublicID string) ([]LobbyItem, error) {
	if game == "" {
		game = "goofspiel"
	}
	return s.repo.ListWaiting(ctx, game, bid, ownerPublicID, 50)
}

// CreateOpen opens a new waiting match seated by the creator at seat A.
func (s *Service) CreateOpen(ctx context.Context, agentPublicID, ownerPublicID string, bid int64) (string, error) {
	if bid <= 0 {
		return "", httpx.NewError(400, "invalid_request", "bid must be > 0")
	}
	if err := s.limits.CheckJoin(ctx, agentPublicID, bid); err != nil {
		return "", err
	}
	if err := s.ver.CheckEligible(ctx, agentPublicID); err != nil {
		return "", err
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	m, err := s.repo.CreateWaitingMatch(ctx, CreateMatchInput{
		PublicID:      platform.NewID(platform.PrefixMatch),
		Game:          "goofspiel",
		Bid:           bid,
		RakePct:       s.rakePct(),
		TotalRounds:   s.cfg.Rounds,
		EngineVersion: gs.Version,
		Commit:        gs.Commit(seed),
		FairnessMode:  gs.FairnessShuffled,
		Seed:          seed,
		Creator:       Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: gs.SeatA},
	})
	if err != nil {
		return "", err
	}
	return m.PublicID, nil
}

// Cancel aborts a waiting lobby entry created by the caller. No stakes are locked
// until a second player joins, so cancellation is always safe.
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

// CreatePaired opens an already-active match between two server-matched agents
// (the matchmaking path): it runs both agents' join checks, escrows both stakes,
// deals the match, and persists it active in one step — no waiting window, so it
// never appears in the open lobby. Returns the new match's public id.
func (s *Service) CreatePaired(ctx context.Context, aAgent, aOwner, bAgent, bOwner string, bid int64) (string, error) {
	if bid <= 0 {
		return "", httpx.NewError(400, "invalid_request", "bid must be > 0")
	}
	// Both seats must clear the spending limits and verification gate.
	if err := s.limits.CheckJoin(ctx, aAgent, bid); err != nil {
		return "", err
	}
	if err := s.limits.CheckJoin(ctx, bAgent, bid); err != nil {
		return "", err
	}
	if err := s.ver.CheckEligible(ctx, aAgent); err != nil {
		return "", err
	}
	if err := s.ver.CheckEligible(ctx, bAgent); err != nil {
		return "", err
	}

	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	cfg := gs.DefaultConfig()
	cfg.Rounds = s.cfg.Rounds
	cfg.FairnessMode = gs.FairnessShuffled
	eng := gs.New(cfg)
	state, events := eng.Init(seed)

	publicID := platform.NewID(platform.PrefixMatch)
	// Escrow both stakes atomically before persisting the match (mirrors Join).
	if err := s.wallet.StakeMatch(ctx, publicID, aAgent, bAgent, bid); err != nil {
		return "", err
	}
	deadline := s.clock.Now().Add(s.cfg.MoveWindow)
	in := CreatePairedInput{
		PublicID: publicID, Game: "goofspiel", Bid: bid, RakePct: s.rakePct(),
		TotalRounds: s.cfg.Rounds, EngineVersion: gs.Version, Commit: gs.Commit(seed),
		FairnessMode: gs.FairnessShuffled, Seed: seed,
		SeatA: Player{AgentPublicID: aAgent, OwnerPublicID: aOwner, Seat: gs.SeatA},
		SeatB: Player{AgentPublicID: bAgent, OwnerPublicID: bOwner, Seat: gs.SeatB},
		State: state, Deadline: deadline, Events: events,
	}
	if err := s.repo.CreatePairedActive(ctx, in); err != nil {
		// Persisting failed after staking — return both bids so no coins are stuck.
		_ = s.wallet.RefundStakes(ctx, publicID, aAgent, bAgent, bid)
		return "", err
	}
	s.publish(publicID, state, events)
	// Auto-drive connected agents over their sockets (no-op unless enabled + at
	// least one seat is connected); a non-connected seat self-drives via HTTP.
	s.maybeDrive(publicID, aAgent, bAgent)
	return publicID, nil
}

// CreateSandbox opens an ACTIVE, risk-free practice match: the developer's agent
// at seat A versus a platform house agent at seat B, playing the given bot policy.
// Unlike the competitive paths it stakes NO coins, enforces NO spending limits,
// runs NO eligibility gate, and (on finish) updates NO rating — yet it deals,
// persists, broadcasts, and replays through the very same machinery, so the dev's
// client exercises the real wire contract. The house plays automatically (see
// commit). Returns the new match's public id.
func (s *Service) CreateSandbox(ctx context.Context, humanAgent, humanOwner, houseAgent, houseOwner, policy string) (string, error) {
	// Sandbox stakes nothing, so the money limits are skipped — but concurrency is a
	// THROUGHPUT limit, not a money one, and ignoring it multiplied an LLM agent's
	// inference bill by however many tables happened to be open. See CheckConcurrency.
	if err := s.limits.CheckConcurrency(ctx, humanAgent); err != nil {
		return "", err
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	cfg := gs.DefaultConfig()
	cfg.Rounds = s.cfg.Rounds
	cfg.FairnessMode = gs.FairnessShuffled
	eng := gs.New(cfg)
	state, events := eng.Init(seed)

	publicID := platform.NewID(platform.PrefixMatch)
	deadline := s.clock.Now().Add(s.cfg.MoveWindow)
	in := CreatePairedInput{
		PublicID: publicID, Game: "goofspiel", Mode: ModeSandbox, BotPolicy: policy,
		Bid: 0, RakePct: 0, TotalRounds: s.cfg.Rounds, EngineVersion: gs.Version,
		Commit: gs.Commit(seed), FairnessMode: gs.FairnessShuffled, Seed: seed,
		SeatA: Player{AgentPublicID: humanAgent, OwnerPublicID: humanOwner, Seat: gs.SeatA},
		SeatB: Player{AgentPublicID: houseAgent, OwnerPublicID: houseOwner, Seat: HouseSeat},
		State: state, Deadline: deadline, Events: events,
	}
	if err := s.repo.CreatePairedActive(ctx, in); err != nil {
		return "", err // no stake was taken, so nothing to unwind
	}
	s.publish(publicID, state, events)
	return publicID, nil
}

// Join seats the caller at seat B, deals the match, and starts round 1.
func (s *Service) Join(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string) (AgentView, error) {
	// Serialize an agent's joins so it can't race concurrent joins into DIFFERENT
	// matches and slip past the per-agent limits (CheckJoin, e.g. max-concurrent /
	// reserve) via TOCTOU — the per-match lock only serializes joins to the SAME
	// match. Agent lock FIRST, then match lock: a consistent global order that can't
	// deadlock. (M6)
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
	if len(m.Players) > 0 && m.Players[0].OwnerPublicID == ownerPublicID {
		return AgentView{}, ErrSameOwner
	}
	if err := s.limits.CheckJoin(ctx, agentPublicID, m.Bid); err != nil {
		return AgentView{}, err
	}
	if err := s.ver.CheckEligible(ctx, agentPublicID); err != nil {
		return AgentView{}, err
	}

	state, events := s.engine(m).Init(m.Seed)

	// Escrow BOTH stakes atomically as the match goes live. One balanced ledger
	// txn means we can never debit one seat and orphan its coins if the other
	// can't pay — it's all-or-nothing.
	creator := m.Players[0].AgentPublicID
	if err := s.wallet.StakeMatch(ctx, matchPublicID, creator, agentPublicID, m.Bid); err != nil {
		return AgentView{}, err
	}

	deadline := s.clock.Now().Add(s.cfg.MoveWindow)
	joiner := Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: gs.SeatB}
	if err := s.repo.Activate(ctx, matchPublicID, joiner, state, deadline, events); err != nil {
		// Activation failed after staking — return both bids so no coins are stuck.
		_ = s.wallet.RefundStakes(ctx, matchPublicID, creator, agentPublicID, m.Bid)
		return AgentView{}, err
	}
	s.publish(matchPublicID, state, events)

	updated, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(updated, agentPublicID), nil
}

// Act applies an agent's card for the current round. Idempotent per (match,round,
// seat). If the agent registered a signing key, `signature` is REQUIRED and verified
// (proving the agent authored this exact move); the proof is persisted for replay.
//
// Concurrency model: the Redis lock is a FAST PATH that avoids wasted retries when
// it's held. Correctness comes from optimistic concurrency — the UNIQUE(match_id,
// seq) event-log constraint rejects a racing writer, surfacing as ErrConcurrentUpdate,
// and we re-read + retry. So a Redis outage degrades to a few extra retries, never a
// stuck, lost, or double-applied move.
// Act submits a move over the authenticated request path. If the agent registered
// a signing key the move must carry a valid Ed25519 signature.
func (s *Service) Act(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string) (AgentView, error) {
	return s.act(ctx, agentPublicID, matchPublicID, round, card, signature, false)
}

// DriveAct submits a move the PLATFORM decided on the agent's behalf after asking
// it over its authenticated socket connection. The socket auth (the agent's API key
// verified at register) is the authenticity guarantee, so a per-move Ed25519
// signature is NOT required here — this is what lets the server drive a signing-key
// agent's seat in a live match instead of wedging on ErrSignatureRequired.
func (s *Service) DriveAct(ctx context.Context, agentPublicID, matchPublicID string, round, card int) (AgentView, error) {
	return s.act(ctx, agentPublicID, matchPublicID, round, card, "", true)
}

func (s *Service) act(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string, platformDriven bool) (AgentView, error) {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless, relying on the OCC retry.)

	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.tryAct(ctx, agentPublicID, matchPublicID, round, card, signature, platformDriven)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue // another writer advanced first; re-read and retry
		}
		return view, err
	}
	return AgentView{}, ErrBusy // retries exhausted under heavy contention
}

// tryAct is one optimistic-concurrency attempt: read the current snapshot, validate,
// seal, and commit. A lost race returns ErrConcurrentUpdate for Act to retry.
func (s *Service) tryAct(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string, platformDriven bool) (AgentView, error) {
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
	if round != m.State.Round {
		return AgentView{}, ErrWrongRound
	}
	// DB-backed idempotent replay: this seat already sealed this round → current
	// view, no error. Reads the committed snapshot, so it holds even with no lock.
	if m.State.Sealed[p.Seat] != nil {
		return s.view(m, agentPublicID), nil
	}

	// Move authenticity: if the agent registered a signing key, the move must carry
	// a valid Ed25519 signature over (match, round, seat, card). Verified BEFORE the
	// card is sealed, so a forged/altered move never enters the log.
	// Platform-driven moves (the server driving this seat over the agent's
	// authenticated socket) skip the per-move signature — the socket connection is
	// the authenticity guarantee. Self-drive (request-path) moves still require it.
	pubkey, err := s.repo.AgentSigningKey(ctx, agentPublicID)
	if err != nil {
		return AgentView{}, err
	}
	if pubkey != "" && !platformDriven {
		if signature == "" {
			return AgentView{}, ErrSignatureRequired
		}
		if !movesig.Verify(pubkey, matchPublicID, round, p.Seat, card, signature) {
			return AgentView{}, ErrBadSignature
		}
	}

	// Record think-time for verification (best-effort).
	if m.RoundDeadline != nil {
		started := m.RoundDeadline.Add(-s.cfg.MoveWindow)
		s.ver.Record(ctx, agentPublicID, &matchPublicID, int(s.clock.Now().Sub(started).Milliseconds()))
	}

	eng := s.engine(m)
	state, events, err := eng.Seal(m.State, p.Seat, card)
	if err != nil {
		return AgentView{}, mapEngineErr(err)
	}

	// Persist the authorship proof alongside the move (best-effort; off the
	// correctness path — the move is already sealed in the authoritative log).
	if pubkey != "" && !platformDriven {
		_ = s.repo.RecordMoveSignature(ctx, matchPublicID, round, p.Seat, card, signature, pubkey)
	}

	updated, err := s.commit(ctx, m, eng, state, events)
	if err != nil {
		return AgentView{}, err // ErrConcurrentUpdate bubbles to Act's retry loop
	}
	// P3: build the view from the just-committed state instead of re-reading the
	// full aggregate from Postgres — one fewer round trip on every move.
	return s.view(updated, agentPublicID), nil
}

// Roster returns the public seat → agent identities for a match. Public data only
// (name, owner, avatar) — a sealed card is the game's only secret and is never here,
// so this is safe to serve to any spectator mid-match.
func (s *Service) Roster(ctx context.Context, matchPublicID string) ([]RosterSeat, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return nil, ErrNotFound
	}
	return RosterOf(m.Players), nil
}

// Say posts one line of public table talk from a seated agent.
//
// Unlike Act this is NOT turn-gated and carries no round argument: an agent may
// speak whenever it likes during a live match — while the opponent is still
// deciding, between rounds, twice in a row. Talking never seals a card and never
// advances the round, so it cannot be used to stall or to skip a turn. Only the
// card itself is ordered.
//
// The line is appended to the authoritative event log (so it replays with the
// match) and broadcast to spectators, and it lands in State.Chat, which every
// agent view carries — that is what lets the other seat actually answer it.
func (s *Service) Say(ctx context.Context, agentPublicID, matchPublicID, text, kind string) (AgentView, error) {
	// Same lock + optimistic-concurrency retry as act: two agents talking at once
	// (or one talking while the other seals) race on the same match row.
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.trySay(ctx, agentPublicID, matchPublicID, text, kind)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue
		}
		return view, err
	}
	return AgentView{}, ErrBusy
}

func (s *Service) trySay(ctx context.Context, agentPublicID, matchPublicID, text, kind string) (AgentView, error) {
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

	eng := s.engine(m)
	state, events, err := eng.Say(m.State, p.Seat, text, kind)
	if err != nil {
		// Rejections are traced too — see the mafia equivalent.
		if s.chatTracer != nil {
			s.chatTracer.EmitAgentSayRejected(telemetry.ChatEvent{
				Game: "goofspiel", MatchID: matchPublicID, AgentID: agentPublicID,
				Seat: p.Seat, Kind: kind, Text: text, Reason: "illegal",
			})
		}
		return AgentView{}, mapEngineErr(err)
	}
	if s.chatTracer != nil {
		s.chatTracer.EmitAgentSaid(telemetry.ChatEvent{
			Game: "goofspiel", MatchID: matchPublicID, AgentID: agentPublicID,
			Seat: p.Seat, Kind: kind, Text: text,
		})
	}
	updated, err := s.commit(ctx, m, eng, state, events)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(updated, agentPublicID), nil
}

// commit resolves the round if both seats have sealed, persists, broadcasts, and
// returns the updated match aggregate (so callers avoid a redundant re-read). A
// lost optimistic-concurrency race propagates as ErrConcurrentUpdate.
func (s *Service) commit(ctx context.Context, m Match, eng *gs.Engine, state gs.State, events []gs.Event) (Match, error) {
	// Sandbox: the house agent auto-plays the instant the developer has sealed, so
	// the round resolves immediately and the dev always gets a complete round back
	// (no "waiting for opponent" stall). The house move is recorded in the event log
	// like any other, so the replay still verifies as a unit.
	if m.Mode == ModeSandbox {
		if hev, ok := s.playHouse(eng, m.BotPolicy, &state); ok {
			events = append(events, hev...)
		}
	}

	if state.Sealed[gs.SeatA] == nil || state.Sealed[gs.SeatB] == nil {
		// Still waiting on the opponent; same round, deadline unchanged.
		if err := s.repo.Advance(ctx, m.PublicID, state, m.RoundDeadline, events); err != nil {
			return Match{}, err
		}
		s.publish(m.PublicID, state, events)
		m.State = state
		return m, nil
	}

	resolved, resolveEvents, err := eng.Resolve(state)
	if err != nil {
		return Match{}, err // both seats sealed here, so unreachable in practice — but never advance on a failed resolve
	}
	all := make([]gs.Event, 0, len(events)+len(resolveEvents))
	all = append(all, events...)
	all = append(all, resolveEvents...)

	if resolved.Finished {
		// G4 — durably persist the finishing SEAL(S) before settling. finalize does
		// Settle-before-Finish deliberately (crash-safety), but the finishing seal was
		// previously made durable only inside Finish. So a crash between Settle and
		// Finish would leave the match 'active' with the finishing seat UNSEALED in the
		// DB; the sweeper would then re-drive via ForceTimeout (lowest card) and record
		// a winner / Elo / replay hash that DIVERGES from the winner actually PAID.
		// Persisting the real sealed cards here means a re-drive resolves the real cards
		// (both already sealed ⇒ HandleTimeout forces nothing) and reproduces the paid
		// outcome. Money was always protected by settle idempotency; this protects
		// recorded-result integrity. The seal events are now persisted by this Advance,
		// so finalize appends only the resolve events (no double-append).
		if err := s.repo.Advance(ctx, m.PublicID, state, m.RoundDeadline, events); err != nil {
			return Match{}, err
		}
		players, err := s.finalize(ctx, m, resolved, resolveEvents)
		if err != nil {
			return Match{}, err
		}
		s.publish(m.PublicID, resolved, all)
		m.State = resolved
		m.Players = players
		m.Status = StatusFinished
		m.RoundDeadline = nil
		return m, nil
	}

	next := s.clock.Now().Add(s.cfg.MoveWindow)
	if err := s.repo.Advance(ctx, m.PublicID, resolved, &next, all); err != nil {
		return Match{}, err
	}
	s.publish(m.PublicID, resolved, all)
	m.State = resolved
	m.RoundDeadline = &next
	return m, nil
}

// playHouse seals the house agent's card for the current round in a sandbox match.
// It no-ops (ok=false) if the match is finished or the house seat is already
// sealed (e.g. a timeout sweep forced both seats). If the bot somehow returns an
// illegal card, it falls back to the engine's deterministic timeout move so the
// match can never wedge on a buggy strategy.
func (s *Service) playHouse(eng *gs.Engine, policy string, state *gs.State) ([]gs.Event, bool) {
	if state.Finished || state.Sealed[HouseSeat] != nil {
		return nil, false
	}
	card := s.bot.Pick(*state, HouseSeat, policy)
	ns, ev, err := eng.Seal(*state, HouseSeat, card)
	if err != nil {
		ns, ev, err = eng.ForceTimeout(*state, HouseSeat)
		if err != nil {
			return nil, false
		}
	}
	*state = ns
	return ev, true
}

// finalize settles the match: compute winner + per-player deltas, settle coins,
// hash the full log, and persist the terminal state. Returns the players with their
// final scores + coin deltas so the caller can render the result without re-reading.
func (s *Service) finalize(ctx context.Context, m Match, state gs.State, newEvents []gs.Event) ([]Player, error) {
	winnerAgent := ""
	if state.Winner != gs.Tie {
		if wp := m.playerBySeat(state.Winner); wp != nil {
			winnerAgent = wp.AgentPublicID
		}
	}
	pool := m.Bid * 2
	// Sandbox matches move no coins: skip settlement entirely (bid is 0 anyway, so
	// this is also a belt-and-suspenders guard against any future money path).
	//
	// ORDER MATTERS (crash-safety): Settle runs BEFORE Finish deliberately. Settle is
	// idempotent on settle:{match}; Finish is the durable terminal marker. A crash
	// between them leaves the match 'active', so the sweeper (HandleTimeout →
	// SweepExpired) re-drives it and re-finalizes — Settle no-ops, Finish completes.
	// Do NOT reorder to Finish-first: that would strand escrow (winner never paid,
	// and Act/HandleTimeout early-return on a finished match, so nothing re-drives).
	if m.Mode != ModeSandbox {
		// A ranked match that cannot show it was played by an LLM is VOIDED rather
		// than settled: both stakes go back and nobody is paid. Refund and Settle
		// share one idempotency key, so exactly one of them can ever take effect —
		// a re-drive after a crash cannot pay out a match that was voided, or void
		// one that already paid.
		//
		// Voiding rather than forfeiting is deliberate. Detection is new and will
		// have false positives (batching, caching, a model timing out into a
		// deterministic fallback), and taking a real developer's stake on a false
		// positive is not recoverable in the way an un-played match is.
		if failed, agent := s.rankedIntegrityFailed(ctx, m, len(state.History)); failed {
			slog.Warn("match: VOIDED — seat could not prove its decisions were LLM-backed",
				"match", m.PublicID, "agent", agent, "decisions", len(state.History),
				"min_pct", s.integrityMinPct)
			if err := s.wallet.Refund(ctx, m.PublicID); err != nil {
				return nil, err
			}
		} else if err := s.wallet.Settle(ctx, m.PublicID, winnerAgent, pool, m.RakePct); err != nil {
			return nil, err
		}
	}

	// Compute the replay hash over the COMPLETE log (prior + new events).
	prior, err := s.repo.LoadEvents(ctx, m.PublicID)
	if err != nil {
		return nil, err
	}
	hash, err := replay.Hash(append(prior, newEvents...))
	if err != nil {
		return nil, err
	}

	players := finalizePlayers(m.Players, state, pool, m.Bid, m.RakePct)

	// Competitive matches emit a transactional match.finished fact (badges,
	// notifications, analytics project off it). Sandbox is off the growth path:
	// nil payload => no event.
	var finishedEvent []byte
	if m.Mode != ModeSandbox {
		finishedEvent, err = json.Marshal(matchFinishedPayload{
			MatchID: m.PublicID, Game: m.Game, WinnerAgent: winnerAgent,
		})
		if err != nil {
			return nil, err
		}
	}
	if err := s.repo.Finish(ctx, m.PublicID, state, winnerAgent, hash, players, newEvents, finishedEvent); err != nil {
		return nil, err
	}

	// Sandbox is unrated and off the growth path: skip ratings, clips, and
	// notifications. Competitive matches update skill ratings and fire the
	// engagement hooks off the hot path. The match is already durably finished +
	// settled here, and Rate is idempotent on the match id (rating_updates marker),
	// so a transient rater error must not silently drop the leaderboard update:
	// retry a few times before surfacing it (nothing re-drives finalize once the
	// match is Finished — Act/HandleTimeout early-return on non-active).
	if m.Mode != ModeSandbox {
		rr := RatingResult{MatchPublicID: m.PublicID, Game: m.Game, WinnerSeat: state.Winner}
		for _, p := range players {
			rr.Players = append(rr.Players, RatingPlayer{AgentPublicID: p.AgentPublicID, Seat: p.Seat, CoinsDelta: p.CoinsDelta})
		}
		if err := rateWithRetry(ctx, s.rater, rr, 3, 50*time.Millisecond); err != nil {
			return nil, err
		}
		s.finish.MatchFinished(ctx, m.PublicID)

		// Best-effort behavioral-style aggregates (read-only; never gate money or
		// completion). A recorder error is swallowed — the match is already durably
		// finished + settled + rated above.
		if s.style != nil {
			for _, p := range players {
				agg, eff := goofStyle(state, p.Seat)
				_ = s.style.RecordStyle(ctx, p.AgentPublicID, m.Game, agg, eff)
			}
		}
	}
	return players, nil
}

// goofStyle derives read-only behavioral metrics for one seat from the finished
// Goofspiel transcript. aggression = average bid strength (mean bid relative to
// the deck's top card, 0-100); efficiency = share of total prize value the seat
// captured (final score / total prize value, 0-100, carry-overs included).
func goofStyle(state gs.State, seat int) (aggression, efficiency int) {
	if len(state.History) == 0 || seat < 0 || seat > 1 {
		return 0, 0
	}
	maxCard, bidSum, prizeTotal := 0, 0, 0
	for _, r := range state.History {
		bidSum += r.Cards[seat]
		for _, c := range []int{r.Cards[0], r.Cards[1], r.Prize} {
			if c > maxCard {
				maxCard = c
			}
		}
		prizeTotal += r.Prize
	}
	rounds := len(state.History)
	if maxCard > 0 {
		aggression = bidSum * 100 / (rounds * maxCard)
	}
	if prizeTotal > 0 {
		efficiency = state.Scores[seat] * 100 / prizeTotal
	}
	return aggression, efficiency
}

// rateWithRetry applies the (idempotent) rating with a bounded retry so a transient
// error right after the match commits doesn't lose the leaderboard/Elo update. It
// honors context cancellation between attempts.
func rateWithRetry(ctx context.Context, rater Rater, rr RatingResult, attempts int, backoff time.Duration) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		if err = rater.Rate(ctx, rr); err == nil {
			return nil
		}
	}
	return err
}

// HandleTimeout forces a missing seat to play (deterministically) once its window
// has expired, then resolves. Driven by the sweeper.
func (s *Service) HandleTimeout(ctx context.Context, matchPublicID string) error {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return nil // another instance holds it; skip this round
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless so the timeout still fires
	// and escrow can't stay wedged; OCC on commit protects against a racing writer.)

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil || m.Status != StatusActive {
		return nil
	}
	if m.RoundDeadline == nil || s.clock.Now().Before(*m.RoundDeadline) {
		return nil // not actually expired (raced with a real action)
	}

	eng := s.engine(m)
	state := m.State
	var events []gs.Event
	for seat := 0; seat < 2; seat++ {
		if state.Sealed[seat] == nil {
			// In sandbox, the house seat is never force-timed-out: commit lets it
			// play its real policy, so even an abandoned practice round resolves
			// with a genuine house move rather than a deterministic forfeit.
			if m.Mode == ModeSandbox && seat == HouseSeat {
				continue
			}
			ns, evs, terr := eng.ForceTimeout(state, seat)
			if terr != nil {
				return terr
			}
			state = ns
			events = append(events, evs...)
		}
	}
	// A racing live move (Act) may have advanced the match first — the forced
	// timeout is then moot, surfaced as ErrConcurrentUpdate by the OCC commit.
	if _, err = s.commit(ctx, m, eng, state, events); err != nil && !errors.Is(err, ErrConcurrentUpdate) {
		return err
	}
	return nil
}

// SweepExpired processes all matches whose move window has lapsed.
func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
	// Post-outage grace: a missed turn forfeits the seat's stake, which is right when
	// an agent quits but wrong when WE were the ones unreachable. During the grace
	// window opened after a detected platform outage we skip the sweep entirely, so
	// deadlines that lapsed while nobody could play are not converted into losses.
	// The matches stay active and are decided on play once agents reconnect.
	//
	// Nil tracker (feature unwired) or no grace ⇒ unchanged behaviour.
	if s.liveness.InGrace() {
		return 0, nil
	}
	ids, err := s.repo.ListActiveExpired(ctx, s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	var swept int
	var errs error
	for _, id := range ids {
		if err := s.HandleTimeout(ctx, id); err != nil {
			// Best-effort: one match that can't be resolved (e.g. a wedged/corrupt
			// state) must not block the timeout of every other expired match.
			// Collect and continue; the sweeper logs the joined error.
			errs = errors.Join(errs, fmt.Errorf("timeout %s: %w", id, err))
			continue
		}
		swept++
	}
	return swept, errs
}

// pollSafetyTick bounds the long-poll wait if a push wake-up is ever missed
// (e.g. a Redis blip). On the happy path the notifier wakes the waiter the moment
// the state changes, so this timer almost never fires — it's a backstop, not the
// mechanism. Far fewer idle reads than the old fixed 400ms poll.
const pollSafetyTick = 2 * time.Second

// State returns the redacted agent view; with wait it long-polls until the state
// advances (opponent acts / round resolves) or the timeout elapses. The wait is
// driven by the Notifier (Redis pub/sub) so it returns on the actual state change
// rather than a fixed timer — low latency without hammering Postgres.
func (s *Service) State(ctx context.Context, matchPublicID, viewerAgentPublicID string, wait bool, timeout time.Duration) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if !wait || m.Status == StatusFinished {
		return s.view(m, viewerAgentPublicID), nil
	}
	startSeq := m.State.NextSeq

	// Subscribe BEFORE the recheck below so a change landing between them can't be
	// missed (it either bumps NextSeq on the recheck or delivers on the channel).
	wake, cancel := s.notify.Subscribe(matchPublicID)
	defer cancel()

	deadline := s.clock.Now().Add(timeout)
	for {
		remaining := deadline.Sub(s.clock.Now())
		if remaining <= 0 {
			break
		}
		tick := remaining
		if tick > pollSafetyTick {
			tick = pollSafetyTick
		}
		select {
		case <-ctx.Done():
			return s.view(m, viewerAgentPublicID), nil
		case <-wake: // a real state change was published
		case <-time.After(tick): // backstop in case a wake-up was missed
		}
		m, err = s.repo.Get(ctx, matchPublicID)
		if err != nil {
			return AgentView{}, ErrNotFound
		}
		if m.State.NextSeq != startSeq || m.Status == StatusFinished {
			break
		}
	}
	return s.view(m, viewerAgentPublicID), nil
}

// Replay returns the full event log; the seed is revealed only once finished.
func (s *Service) Replay(ctx context.Context, matchPublicID string) (ReplayDoc, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return ReplayDoc{}, ErrNotFound
	}
	events, err := s.repo.LoadEvents(ctx, matchPublicID)
	if err != nil {
		return ReplayDoc{}, err
	}
	doc := ReplayDoc{
		MatchID:       m.PublicID,
		EngineVersion: m.EngineVersion,
		Commit:        m.Commit,
		ReplayHash:    m.ReplayHash,
		Status:        m.Status,
		Events:        events,
		Roster:        RosterOf(m.Players),
	}
	// Pacing side-car: same order as Events, never mixed into them (Events is hashed).
	// Best-effort — a replay without timing is still correct, just unpaced.
	if timed, terr := s.repo.LoadEventsTimed(ctx, matchPublicID); terr == nil && len(timed) > 0 {
		start := timed[0].At
		doc.Timing = make([]EventTiming, 0, len(timed))
		for _, te := range timed {
			doc.Timing = append(doc.Timing, EventTiming{
				Seq: te.Event.Seq, At: te.At.UTC(), OffsetMs: te.At.Sub(start).Milliseconds(),
			})
		}
	}
	if m.Status == StatusFinished {
		doc.Seed = m.Seed // provable-fairness reveal
	}

	// Per-move authenticity: surface the signatures and re-verify them so the
	// reader gets a server-checked verdict AND the raw proofs to re-check offline.
	if proofs, perr := s.repo.LoadMoveSignatures(ctx, matchPublicID); perr == nil && len(proofs) > 0 {
		doc.MoveProofs = proofs
		allValid := true
		for _, mp := range proofs {
			if !movesig.Verify(mp.Pubkey, matchPublicID, mp.Round, mp.Seat, mp.Card, mp.Signature) {
				allValid = false
				break
			}
		}
		doc.MovesVerified = &allValid
	}
	return doc, nil
}

func mapEngineErr(err error) error {
	switch {
	case errors.Is(err, gs.ErrIllegalCard):
		return errIllegalCard("that card is not in your hand")
	case errors.Is(err, gs.ErrFinished):
		return ErrNotActive
	default:
		return errIllegalCard(err.Error())
	}
}

// finalizePlayers computes per-seat final score + (display) coin delta.
func finalizePlayers(players []Player, state gs.State, pool, bid int64, rakePct int) []Player {
	rake := pool * int64(rakePct) / 100
	out := make([]Player, len(players))
	copy(out, players)
	for i := range out {
		seat := out[i].Seat
		out[i].FinalScore = state.Scores[seat]
		switch {
		case state.Winner == gs.Tie:
			out[i].CoinsDelta = 0
		case state.Winner == seat:
			out[i].CoinsDelta = (pool - rake) - bid
		default:
			out[i].CoinsDelta = -bid
		}
	}
	return out
}

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
	return s.cfg.RakePct
}
