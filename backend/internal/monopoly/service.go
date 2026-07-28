package monopoly

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/rating"
)

// monopolyCanonAction is the deterministic string an agent signs for one move:
// the verb + its params (property, amount, and the full trade for a proposal).
// Both signer and verifier compute it identically from the same Action.
func monopolyCanonAction(act mono.Action) string {
	trade := ""
	if act.Trade != nil {
		if b, err := json.Marshal(act.Trade); err == nil {
			trade = string(b)
		}
	}
	return fmt.Sprintf("%s|%d|%d|%s", act.Kind, act.Property, act.Amount, trade)
}

// Config tunes move windows and table economics.
type Config struct {
	EntryFee       int64
	PlatformFeePct int
	MoveWindow     time.Duration
	LockTTL        time.Duration
	// WaitingTTL is how long a staked waiting table (below TargetPlayers) may sit
	// before the sweeper aborts it, so an agent isn't stuck in a lobby that never fills.
	WaitingTTL time.Duration
}

// Service drives the Monopoly match lifecycle around the pure engine.
type Service struct {
	repo   Repo
	lock   Locker
	wallet Wallet
	bcast  Broadcaster
	finish FinishHook
	clock  platform.Clock
	cfg    Config
	// pusher is set by EnablePushPlay to enable POST /v1/monopoly/pushplay
	// (manifest push model). Nil ⇒ push-play returns 501.
	pusher *pushPlayer
	// notify wakes long-polling State callers on a state change. Nil ⇒ no
	// long-poll (State returns immediately), so tests and notifier-less builds work.
	notify Notifier
	// rater applies per-arena skill ratings when a paid table finalizes. Nil ⇒
	// ratings skipped. rating.Service satisfies it.
	rater Rater
	// limits/ver gate a STAKED join (spending budget + certification/suspension).
	// Nil ⇒ the check is skipped; practice (zero-fee) tables never stake so never
	// consult them. Set once at wiring via SetLimits/SetVerifier.
	limits Limits
	ver    Verifier
}

// SetLimits installs the per-agent spending-limit check for staked joins.
func (s *Service) SetLimits(l Limits) { s.limits = l }

// SetVerifier installs the eligibility check for staked joins.
func (s *Service) SetVerifier(v Verifier) { s.ver = v }

// Rater applies a finished ranked table's TrueSkill change to the Monopoly arena.
// Satisfied directly by *rating.Service.
type Rater interface {
	Rate(ctx context.Context, res rating.MatchResult) error
}

// SetRater installs the rating hook (called once at wiring time).
func (s *Service) SetRater(r Rater) { s.rater = r }

// Notifier is the low-latency wake-up channel for long-polling State callers.
// Satisfied by *store.Notifier (structural).
type Notifier interface {
	Notify(matchPublicID string)
	Subscribe(matchPublicID string) (events <-chan struct{}, cancel func())
}

// SetNotifier installs the wake-up channel (called once at wiring time).
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// publish pushes game events AND the resulting "thinking…" set.
//
// The pending set travels with the events that changed it, in the same tick, so a
// seat stops being shown as deciding exactly as its action lands. Taking state here
// means no future call site can emit events and leave a stale indicator behind.
func (s *Service) publish(matchID string, state mono.State, events []mono.Event) {
	s.bcast.Broadcast(matchID, events)
	if state.Finished {
		s.bcast.BroadcastPending(matchID, nil)
	} else {
		eng := mono.New(matchConfig(len(state.Players)))
		s.bcast.BroadcastPending(matchID, pendingSeats(state, eng.PendingSeat(state)))
	}
	if s.notify != nil {
		s.notify.Notify(matchID)
	}
}

func NewService(repo Repo, lock Locker, wallet Wallet, bcast Broadcaster, finish FinishHook, clock platform.Clock, cfg Config) *Service {
	if cfg.MoveWindow <= 0 {
		cfg.MoveWindow = 45 * time.Second
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = 15 * time.Second
	}
	if cfg.WaitingTTL <= 0 {
		cfg.WaitingTTL = 10 * time.Minute
	}
	if cfg.PlatformFeePct <= 0 {
		cfg.PlatformFeePct = DefaultPlatformFeePct
	}
	if finish == nil {
		finish = NoopFinishHook{}
	}
	return &Service{repo: repo, lock: lock, wallet: wallet, bcast: bcast, finish: finish, clock: clock, cfg: cfg}
}

func lockKey(id string) string { return "monopoly:lock:" + id }

// matchConfig reconstructs the engine config for a match from its player count.
// Every other parameter is a fixed default, so an engine rebuilt from a stored
// match reproduces the same rules — the basis for deterministic replay.
func matchConfig(players int) mono.Config {
	return mono.Config{Players: players, StartingCash: 1500, MaxTurns: mono.DefaultMaxTurns, GoSalary: 200}
}

// CreateTable opens a new Monopoly table with the creator at seat 0 and server
// bots in the remaining seats, and starts it immediately (Monopoly is turn-based;
// the creator rolls first). Returns the match id.
func (s *Service) CreateTable(ctx context.Context, agentPublicID, ownerPublicID string, entryFee int64, players int) (string, error) {
	if players <= 0 {
		players = DefaultPlayers
	}
	if players < MinPlayers {
		players = MinPlayers
	}
	if players > MaxPlayers {
		players = MaxPlayers
	}
	if entryFee < 0 {
		entryFee = 0
	}
	// Refuse a staked table when no wallet is wired: otherwise the match would store
	// an EntryFee and report a pool/rake/rewards/coins_delta while StakeTable and
	// SettleTable no-op, advertising a stake and payouts that never move real coins. (G2)
	if s.wallet == nil && entryFee > 0 {
		return "", ErrStakesUnavailable
	}

	// Staked tables are agent-vs-agent: open a WAITING lobby (no bots) that other
	// agents Join; stakes are escrowed and the engine starts once it fills. Practice
	// tables (entryFee == 0) start immediately against server bots, below.
	if entryFee > 0 {
		return s.createWaiting(ctx, agentPublicID, ownerPublicID, entryFee, players)
	}

	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	eng := mono.New(matchConfig(players))
	state, events := eng.Init(seed)

	id := platform.NewID(platform.PrefixMonopoly)
	deadline := s.clock.Now().Add(s.cfg.MoveWindow)
	_, err := s.repo.Create(ctx, CreateMatchInput{
		PublicID: id, Title: "Monopoly AI Arena", EntryFee: entryFee, RakePct: s.cfg.PlatformFeePct,
		Players: players, Seed: seed, Commit: mono.Commit(seed),
		State: state, Deadline: deadline, Events: events,
		Creator: Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: 0},
	})
	if err != nil {
		return "", err
	}
	s.publish(id, state, events)
	return id, nil
}

func agentJoinLockKey(agent string) string { return "agent:join:lock:" + agent }

// createWaiting opens a staked, agent-vs-agent table with only the creator seated,
// waiting for (players-1) more agents to Join. Nothing is staked until it starts.
func (s *Service) createWaiting(ctx context.Context, agentPublicID, ownerPublicID string, entryFee int64, players int) (string, error) {
	if s.limits != nil {
		if err := s.limits.CheckJoin(ctx, agentPublicID, entryFee); err != nil {
			return "", err
		}
	}
	if s.ver != nil {
		if err := s.ver.CheckEligible(ctx, agentPublicID); err != nil {
			return "", err
		}
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	id := platform.NewID(platform.PrefixMonopoly)
	_, err := s.repo.CreateWaiting(ctx, CreateMatchInput{
		PublicID: id, Title: "Monopoly AI Arena", EntryFee: entryFee, RakePct: s.cfg.PlatformFeePct,
		Players: players, TargetPlayers: players, Seed: seed, Commit: mono.Commit(seed),
		Creator: Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: 0},
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Lobby lists open waiting staked tables the caller can join (excluding its own).
func (s *Service) Lobby(ctx context.Context, entryFee int64, ownerPublicID string) ([]LobbyItem, error) {
	return s.repo.ListWaiting(ctx, entryFee, ownerPublicID, 50)
}

// Join seats the agent at a waiting table and, once the table fills, escrows every
// seat's stake and starts the match. Serializes the agent's concurrent joins first
// (shared "agent:join:lock:" namespace with Mafia/Goofspiel) so it can't race joins
// across tables/games and bypass per-agent spending limits.
func (s *Service) Join(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string) (AgentView, error) {
	if relAgent, okA, lerr := s.lock.Lock(ctx, agentJoinLockKey(agentPublicID), s.cfg.LockTTL); lerr == nil {
		if !okA {
			return AgentView{}, ErrBusy
		}
		defer relAgent()
	}
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusWaiting {
		return AgentView{}, ErrNotWaiting
	}
	if m.agentByAgentID(agentPublicID) != nil {
		return AgentView{}, ErrAlreadyJoined
	}
	if len(m.Agents) >= m.TargetPlayers {
		return AgentView{}, ErrTableFull
	}
	// One owner may hold at most one seat — otherwise a single owner could seat a
	// coordinated majority and funnel honest agents' entry fees to itself.
	for i := range m.Agents {
		if m.Agents[i].OwnerPublicID == ownerPublicID {
			return AgentView{}, ErrSameOwner
		}
	}
	if s.limits != nil {
		if err := s.limits.CheckJoin(ctx, agentPublicID, m.EntryFee); err != nil {
			return AgentView{}, err
		}
	}
	if s.ver != nil {
		if err := s.ver.CheckEligible(ctx, agentPublicID); err != nil {
			return AgentView{}, err
		}
	}

	seat := len(m.Agents) // creator holds seat 0; joiners take 1,2,…
	if err := s.repo.JoinSeat(ctx, matchPublicID, Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: seat}); err != nil {
		return AgentView{}, err
	}

	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	if len(m.Agents) < m.TargetPlayers {
		return s.view(m, agentPublicID), nil
	}
	if err := s.startTable(ctx, m); err != nil {
		return AgentView{}, err
	}
	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(m, agentPublicID), nil
}

// startTable initializes the engine for a filled waiting table, escrows every
// seated agent's stake, and flips it to active. All seats are real agents (no bots).
func (s *Service) startTable(ctx context.Context, m Match) error {
	eng := mono.New(matchConfig(m.TargetPlayers))
	state, events := eng.Init(m.Seed)

	agents := make([]string, len(m.Agents))
	for i, p := range m.Agents {
		agents[i] = p.AgentPublicID
	}
	if s.wallet != nil && m.EntryFee > 0 {
		if err := s.wallet.StakeTable(ctx, m.PublicID, agents, m.EntryFee); err != nil {
			return err
		}
	}
	deadline := s.clock.Now().Add(s.cfg.MoveWindow)
	if err := s.repo.Start(ctx, m.PublicID, state, deadline, events); err != nil {
		// Compensate a stake-then-start dual-write failure so no coins are trapped.
		if s.wallet != nil && m.EntryFee > 0 {
			_ = s.wallet.RefundTable(ctx, m.PublicID, agents, m.EntryFee)
		}
		return err
	}
	s.publish(m.PublicID, state, events)
	return nil
}

// Cancel aborts a creator's waiting table before it starts. No stakes have been
// taken yet (staking happens at start), so there is nothing to refund.
func (s *Service) Cancel(ctx context.Context, agentPublicID, matchPublicID string) error {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return ErrBusy
		}
		defer release()
	}
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return ErrNotFound
	}
	if m.Status != StatusWaiting {
		return ErrNotWaiting
	}
	if len(m.Agents) == 0 || m.Agents[0].AgentPublicID != agentPublicID {
		return ErrNotCreator
	}
	return s.repo.CancelWaiting(ctx, matchPublicID, agentPublicID)
}

// Act applies one action for the calling agent, then auto-advances every bot
// seat until it is an agent's turn again (or the match ends).
//
// Concurrency model (mirrors Goofspiel): the Redis lock is a FAST PATH that
// avoids wasted retries when held. Correctness comes from optimistic concurrency —
// the UNIQUE(match_id, seq) event-log constraint rejects a racing writer
// (ErrConcurrentUpdate) and we re-read + retry. So a Redis outage degrades to a
// few extra retries, never a stuck/lost/double-applied move.
// Act applies an agent's move. signature is the agent's Ed25519 signature over
// the canonical (match, next_seq, seat, action) message; REQUIRED when the agent
// has a registered signing key and the move is not platformDriven (push-play/bot
// over the authenticated socket/endpoint), mirroring Goofspiel.
func (s *Service) Act(ctx context.Context, agentPublicID, matchPublicID string, act mono.Action, signature string, platformDriven bool) (AgentView, error) {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless, relying on the OCC retry.)

	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.tryAct(ctx, agentPublicID, matchPublicID, act, signature, platformDriven)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue // another writer advanced first; re-read and retry
		}
		return view, err
	}
	return AgentView{}, ErrBusy // retries exhausted under heavy contention
}

// tryAct is one optimistic-concurrency attempt: read the snapshot, validate, step,
// persist. A racing writer surfaces as ErrConcurrentUpdate for Act's retry loop.
// pendingSeats is who the table is waiting on, derived from state.
//
// Normally that is just the seat on turn. During an AUCTION it is every seat still
// in the bidding — they decide concurrently, so a single "turn" seat would under-
// report the table and the UI would show one bidder thinking while three others
// silently were too.
func pendingSeats(st mono.State, turn int) []int {
	if st.Finished {
		return nil
	}
	if a := st.Auction; a != nil {
		var out []int
		for seat, in := range a.InAuction {
			if in && seat < len(st.Players) && !st.Players[seat].Bankrupt {
				out = append(out, seat)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if turn < 0 {
		return nil
	}
	return []int{turn}
}

// Roster returns the public identity of every seat on the board, bots included.
// Public data only — no cash, no holdings, no hidden state.
func (s *Service) Roster(ctx context.Context, matchPublicID string) ([]RosterSeat, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return nil, ErrNotFound
	}
	return RosterOf(m.Agents, m.Players), nil
}

// Say posts one line of public table talk from a seated agent.
//
// Unlike Act this is NOT turn-gated: Monopoly's deal-making lives between turns,
// so an agent may talk while another seat is rolling, mid-auction, or while a
// trade sits pending. A line is never a move — it cannot roll, buy, bid or pass.
// The line joins the authoritative event log (so it replays with the match), is
// broadcast to spectators, and lands in State.Chat, which every agent view
// carries — that is what lets the other seats answer.
func (s *Service) Say(ctx context.Context, agentPublicID, matchPublicID, text, kind string) (AgentView, error) {
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
	p := m.agentByAgentID(agentPublicID)
	if p == nil {
		return AgentView{}, ErrNotPlayer
	}
	eng := mono.New(matchConfig(m.Players))
	state, events, err := eng.Say(m.State, p.Seat, text, kind)
	if err != nil {
		if errors.Is(err, mono.ErrFinished) {
			return AgentView{}, ErrNotActive
		}
		return AgentView{}, ErrIllegalAction // empty text, bad seat, or bankrupt
	}
	if err := s.persist(ctx, m, state, events); err != nil {
		return AgentView{}, err
	}
	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(m, agentPublicID), nil
}

func (s *Service) tryAct(ctx context.Context, agentPublicID, matchPublicID string, act mono.Action, signature string, platformDriven bool) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusActive {
		return AgentView{}, ErrNotActive
	}
	p := m.agentByAgentID(agentPublicID)
	if p == nil {
		return AgentView{}, ErrNotPlayer
	}
	eng := mono.New(matchConfig(m.Players))
	if eng.PendingSeat(m.State) != p.Seat {
		return AgentView{}, ErrNotYourTurn
	}

	// Move authenticity: if the agent registered a signing key, a request-path move
	// must carry a valid Ed25519 signature over the canonical (match, next_seq,
	// seat, action) message — verified BEFORE the move is applied. next_seq is the
	// gap-free per-move counter (visible to the agent in view.State), so a captured
	// signature can never be replayed onto a later move. Platform-driven moves
	// (push-play/bot) skip it, exactly as Goofspiel does.
	canonAction := monopolyCanonAction(act)
	signSeq, signSeat := m.State.NextSeq, p.Seat
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
			if !movesig.VerifyAction(movesig.DomainMonopoly, pubkey, matchPublicID, signSeq, signSeat, canonAction, signature) {
				return AgentView{}, ErrBadSignature
			}
			signPubkey = pubkey
		}
	}

	state, events, err := eng.Step(m.State, p.Seat, act, m.Seed)
	if err != nil {
		return AgentView{}, ErrIllegalAction
	}
	state, botEvents := s.drive(eng, state, m.Seed, m.botSeats())
	events = append(events, botEvents...)

	if err := s.persist(ctx, m, state, events); err != nil {
		return AgentView{}, err // ErrConcurrentUpdate bubbles to Act's retry loop
	}
	// Persist the authorship proof (best-effort; the move is already in the log).
	if signPubkey != "" {
		_ = s.repo.RecordMoveSignature(ctx, matchPublicID, signSeq, signSeat, canonAction, signature, signPubkey)
	}
	m, err = s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(m, agentPublicID), nil
}

// drive plays every pending BOT seat with the engine's deterministic bots,
// stopping when the pending seat is a human agent or the match is over. A bad
// bot move can never wedge the match: it falls back to the engine timeout.
func (s *Service) drive(eng *mono.Engine, state mono.State, seed []byte, bots map[int]bool) (mono.State, []mono.Event) {
	var evs []mono.Event
	for guard := 0; guard < 200000 && !state.Finished; guard++ {
		seat := eng.PendingSeat(state)
		if !bots[seat] {
			break
		}
		bot := mono.NewBot("bot", mono.DefaultStyles[seat%len(mono.DefaultStyles)], seed, seat)
		ns, ev, err := eng.Step(state, seat, bot.Decide(eng, state, seat), seed)
		if err != nil {
			ns, ev, err = eng.ForceTimeout(state, seed)
			if err != nil {
				break
			}
		}
		state = ns
		evs = append(evs, ev...)
	}
	return state, evs
}

func (s *Service) persist(ctx context.Context, m Match, state mono.State, events []mono.Event) error {
	if state.Finished {
		return s.finalize(ctx, m, state, events)
	}
	deadline := s.clock.Now().Add(s.cfg.MoveWindow)
	if err := s.repo.Advance(ctx, m.PublicID, state, &deadline, events); err != nil {
		return err
	}
	s.publish(m.PublicID, state, events)
	return nil
}

func (s *Service) finalize(ctx context.Context, m Match, state mono.State, events []mono.Event) error {
	econ := ComputeEconomy(len(m.Agents), m.EntryFee, m.RakePct)
	rewards := ComputeRewards(state.Winner, m.Agents, econ)

	payouts := map[string]int64{}
	for _, r := range rewards {
		if r.Eligible && r.Payout > 0 {
			payouts[r.AgentPublicID] = r.Payout
		}
	}

	// A DRAW must refund the table, not confiscate it.
	//
	// The engine can legitimately finish with no winner (equal net worth at the
	// MaxTurns cap, or no solvent seat left). ComputeRewards then matches no seat, so
	// `payouts` came out EMPTY — and settleMonopoly treats whatever is unpaid as
	// "remainder" and posts it to platform revenue. The result was that a tied table
	// moved every player's stake to the house: 4 agents × 500 coins tied at turn 200
	// meant 2000 coins confiscated and nothing returned.
	//
	// Goofspiel already refunds stakes on a tie with zero rake; Monopoly now matches
	// it. No rake is taken on a draw — the platform fee is for settling a result, and
	// a draw has none.
	platformFee := econ.PlatformFee
	if len(payouts) == 0 && m.EntryFee > 0 {
		platformFee = 0
		for _, a := range m.Agents {
			payouts[a.AgentPublicID] += m.EntryFee
		}
	}

	if s.wallet != nil && m.EntryFee > 0 {
		if err := s.wallet.SettleTable(ctx, m.PublicID, econ.GrossPool, platformFee, payouts); err != nil {
			return err
		}
	}

	agents := finalizeAgents(m.Agents, rewards, m.EntryFee)
	hash := mono.ReplayHash(events)
	if err := s.repo.Finish(ctx, m.PublicID, state, state.Winner, hash, agents, events); err != nil {
		return err
	}
	s.publish(m.PublicID, state, events)

	// Paid tables update the per-arena skill rating (TrueSkill, N-player). Placement
	// is a server-authoritative net-worth ordering across the AGENT seats: bankrupt
	// seats rank last, the rest by net worth (equal net worth = a tie). Idempotent
	// per match, retried since the match is already durably finished + settled.
	if s.rater != nil && m.EntryFee > 0 {
		bankrupt := make(map[int]bool, len(state.Players))
		for _, p := range state.Players {
			bankrupt[p.Seat] = p.Bankrupt
		}
		key := func(seat int) int {
			if bankrupt[seat] {
				return -1 // bankrupt seats rank below any solvent net worth (≥ 0)
			}
			return state.NetWorth(seat)
		}
		res := rating.MatchResult{MatchPublicID: m.PublicID, Game: rating.GameMonopoly}
		for _, a := range agents {
			placement, ak := 1, key(a.Seat)
			for _, b := range agents {
				if key(b.Seat) > ak {
					placement++
				}
			}
			res.Players = append(res.Players, rating.PlayerResult{
				AgentPublicID: a.AgentPublicID, Seat: a.Seat, Placement: placement, CoinsDelta: a.CoinsDelta,
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
	// (lerr != nil — Redis unreachable: proceed lockless so a timeout still fires;
	// OCC on persist protects against a racing writer.)

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil || m.Status != StatusActive {
		return nil
	}
	if m.RoundDeadline == nil || s.clock.Now().Before(*m.RoundDeadline) {
		return nil
	}
	eng := mono.New(matchConfig(m.Players))
	state, events, err := eng.ForceTimeout(m.State, m.Seed)
	if err != nil {
		return err
	}
	state, botEvents := s.drive(eng, state, m.Seed, m.botSeats())
	events = append(events, botEvents...)
	if len(events) == 0 {
		return nil
	}
	// A racing live move advanced the match first — the forced timeout is moot.
	if err := s.persist(ctx, m, state, events); err != nil && !errors.Is(err, ErrConcurrentUpdate) {
		return err
	}
	return nil
}

func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
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

// SweepStaleWaiting aborts staked waiting tables that have sat past WaitingTTL
// without reaching TargetPlayers, freeing agents from a lobby that can never fill.
// No stakes are escrowed before a table starts, so nothing is refunded.
func (s *Service) SweepStaleWaiting(ctx context.Context, limit int) (int, error) {
	cutoff := s.clock.Now().Add(-s.cfg.WaitingTTL)
	return s.repo.ExpireStaleWaiting(ctx, cutoff, limit)
}

func (s *Service) State(ctx context.Context, matchPublicID, viewerAgent string, wait bool, timeout time.Duration) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if !wait || s.notify == nil || m.Status == StatusFinished {
		return s.view(m, viewerAgent), nil
	}

	// Long-poll: block until the match state changes (a turn/phase advance or the
	// match finishing) or the timeout elapses. Subscribe BEFORE the first compare
	// so a change landing in between is never missed.
	startVer := stateVersion(m)
	wake, cancel := s.notify.Subscribe(matchPublicID)
	defer cancel()
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
			return s.view(m, viewerAgent), nil
		case <-wake:
		case <-time.After(tick):
		}
		m, err = s.repo.Get(ctx, matchPublicID)
		if err != nil {
			return AgentView{}, ErrNotFound
		}
		if stateVersion(m) != startVer || m.Status == StatusFinished {
			break
		}
	}
	return s.view(m, viewerAgent), nil
}

// stateVersion is a cheap change key: any turn advance, phase change, or finish
// flips it, which is what a long-poll caller wants to wake on.
func stateVersion(m Match) string {
	return m.Status + "|" + m.State.Phase + "|" + strconv.Itoa(m.State.TurnCount) + "|" + strconv.FormatBool(m.State.Finished)
}

func (s *Service) Economy(ctx context.Context, matchPublicID string) (EconomySnapshot, []RewardRow, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return EconomySnapshot{}, nil, ErrNotFound
	}
	econ := ComputeEconomy(len(m.Agents), m.EntryFee, m.RakePct)
	if m.Status != StatusFinished {
		return econ, nil, nil
	}
	return econ, ComputeRewards(m.State.Winner, m.Agents, econ), nil
}

// Replay returns the full public event log. Every Monopoly event is a record of
// something that already happened, so the log is public at all times.
func (s *Service) Replay(ctx context.Context, matchPublicID string) ([]mono.Event, error) {
	return s.repo.LoadEvents(ctx, matchPublicID, -1)
}

// ReplayTimed returns the log with each event's timestamp plus the roster —
// everything needed to replay a past table exactly as it happened, at its original
// pace and with every seat named (bots included).
func (s *Service) ReplayTimed(ctx context.Context, matchPublicID string) ([]TimedEvent, []RosterSeat, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	timed, err := s.repo.LoadEventsTimed(ctx, matchPublicID)
	if err != nil {
		return nil, nil, err
	}
	return timed, RosterOf(m.Agents, m.Players), nil
}

func (s *Service) Live(ctx context.Context) ([]LiveMatch, error) {
	return s.repo.LiveMatches(ctx)
}

func (s *Service) view(m Match, viewerAgent string) AgentView {
	// A waiting (or aborted) table has no engine state yet — return a light view
	// without touching the engine, so PendingSeat/PublicState never run on a zero board.
	if m.Status != StatusActive && m.Status != StatusFinished {
		v := AgentView{
			MatchID: m.PublicID, Status: m.Status, EntryFee: m.EntryFee,
			Economy: ComputeEconomy(len(m.Agents), m.EntryFee, m.RakePct),
		}
		if p := m.agentByAgentID(viewerAgent); p != nil {
			v.YourSeat = p.Seat
		}
		return v
	}
	eng := mono.New(matchConfig(m.Players))
	pending := -1
	if !m.State.Finished {
		pending = eng.PendingSeat(m.State)
	}
	redacted := m.State.PublicState()
	v := AgentView{
		MatchID: m.PublicID, Status: m.Status,
		Phase: m.State.Phase, Turn: pending, State: &redacted,
		EntryFee: m.EntryFee,
		Economy:  ComputeEconomy(len(m.Agents), m.EntryFee, m.RakePct),
		// Every seat, bots included — see RosterOf.
		Roster:  RosterOf(m.Agents, m.Players),
		Pending: pendingSeats(m.State, pending),
	}
	if m.Status == StatusActive {
		v.Deadline = m.RoundDeadline
	}
	if p := m.agentByAgentID(viewerAgent); p != nil {
		v.YourSeat = p.Seat
		if m.Status == StatusActive && pending == p.Seat {
			v.YourTurn = true
			v.Legal = eng.LegalActions(m.State, p.Seat)
		}
	}
	if m.Status == StatusFinished {
		v.Result = &Result{WinnerSeat: m.State.Winner, Rewards: ComputeRewards(m.State.Winner, m.Agents, v.Economy)}
	}
	return v
}

func finalizeAgents(agents []Player, rewards []RewardRow, entryFee int64) []Player {
	bySeat := map[int]RewardRow{}
	for _, r := range rewards {
		bySeat[r.Seat] = r
	}
	out := make([]Player, len(agents))
	copy(out, agents)
	for i := range out {
		if r, ok := bySeat[out[i].Seat]; ok && r.Eligible {
			out[i].CoinsDelta = r.Payout - entryFee
		} else {
			out[i].CoinsDelta = -entryFee
		}
	}
	return out
}
