package match

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/replay"
)

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
	clock  platform.Clock
	cfg    Config
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

// publish fans new events to spectators (SSE) and wakes any agent long-polling
// this match. Both are best-effort and off the correctness path.
func (s *Service) publish(matchPublicID string, events []gs.Event) {
	s.bcast.Broadcast(matchPublicID, events)
	s.notify.Notify(matchPublicID)
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
		RakePct:       s.cfg.RakePct,
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
		PublicID: publicID, Game: "goofspiel", Bid: bid, RakePct: s.cfg.RakePct,
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
	s.publish(publicID, events)
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
	s.publish(publicID, events)
	return publicID, nil
}

// Join seats the caller at seat B, deals the match, and starts round 1.
func (s *Service) Join(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string) (AgentView, error) {
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
	s.publish(matchPublicID, events)

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
func (s *Service) Act(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string) (AgentView, error) {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless, relying on the OCC retry.)

	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.tryAct(ctx, agentPublicID, matchPublicID, round, card, signature)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue // another writer advanced first; re-read and retry
		}
		return view, err
	}
	return AgentView{}, ErrBusy // retries exhausted under heavy contention
}

// tryAct is one optimistic-concurrency attempt: read the current snapshot, validate,
// seal, and commit. A lost race returns ErrConcurrentUpdate for Act to retry.
func (s *Service) tryAct(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string) (AgentView, error) {
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
	pubkey, err := s.repo.AgentSigningKey(ctx, agentPublicID)
	if err != nil {
		return AgentView{}, err
	}
	if pubkey != "" {
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
	if pubkey != "" {
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
		s.publish(m.PublicID, events)
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
		players, err := s.finalize(ctx, m, resolved, all)
		if err != nil {
			return Match{}, err
		}
		s.publish(m.PublicID, all)
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
	s.publish(m.PublicID, all)
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
	if m.Mode != ModeSandbox {
		if err := s.wallet.Settle(ctx, m.PublicID, winnerAgent, pool, m.RakePct); err != nil {
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
	if err := s.repo.Finish(ctx, m.PublicID, state, winnerAgent, hash, players, newEvents); err != nil {
		return nil, err
	}

	// Sandbox is unrated and off the growth path: skip ratings, clips, and
	// notifications. Competitive matches update skill ratings (idempotent on the
	// match id, so a finalize retry after a crash re-applies safely) and fire the
	// engagement hooks off the hot path.
	if m.Mode != ModeSandbox {
		rr := RatingResult{MatchPublicID: m.PublicID, WinnerSeat: state.Winner}
		for _, p := range players {
			rr.Players = append(rr.Players, RatingPlayer{AgentPublicID: p.AgentPublicID, Seat: p.Seat, CoinsDelta: p.CoinsDelta})
		}
		if err := s.rater.Rate(ctx, rr); err != nil {
			return nil, err
		}
		s.finish.MatchFinished(ctx, m.PublicID)
	}
	return players, nil
}

// HandleTimeout forces a missing seat to play (deterministically) once its window
// has expired, then resolves. Driven by the sweeper.
func (s *Service) HandleTimeout(ctx context.Context, matchPublicID string) error {
	release, ok, err := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL)
	if err != nil || !ok {
		return err // another instance holds it; skip this round
	}
	defer release()

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
	_, err = s.commit(ctx, m, eng, state, events)
	return err
}

// SweepExpired processes all matches whose move window has lapsed.
func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
	ids, err := s.repo.ListActiveExpired(ctx, s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.HandleTimeout(ctx, id); err != nil {
			// Best-effort: log-and-continue is the caller's job (sweeper).
			return 0, err
		}
	}
	return len(ids), nil
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
