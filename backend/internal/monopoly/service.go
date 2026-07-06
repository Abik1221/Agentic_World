package monopoly

import (
	"context"
	"crypto/rand"
	"strconv"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/platform"
)

// Config tunes move windows and table economics.
type Config struct {
	EntryFee       int64
	PlatformFeePct int
	MoveWindow     time.Duration
	LockTTL        time.Duration
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
}

// Notifier is the low-latency wake-up channel for long-polling State callers.
// Satisfied by *store.Notifier (structural).
type Notifier interface {
	Notify(matchPublicID string)
	Subscribe(matchPublicID string) (events <-chan struct{}, cancel func())
}

// SetNotifier installs the wake-up channel (called once at wiring time).
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// publish broadcasts events to spectators AND wakes any long-polling State callers.
func (s *Service) publish(matchID string, events []mono.Event) {
	s.bcast.Broadcast(matchID, events)
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
	if s.wallet != nil && entryFee > 0 {
		if err := s.wallet.StakeTable(ctx, id, []string{agentPublicID}, entryFee); err != nil {
			return "", err
		}
	}
	s.publish(id, events)
	return id, nil
}

// Act applies one action for the calling agent, then auto-advances every bot
// seat until it is an agent's turn again (or the match ends).
func (s *Service) Act(ctx context.Context, agentPublicID, matchPublicID string, act mono.Action) (AgentView, error) {
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

	state, events, err := eng.Step(m.State, p.Seat, act, m.Seed)
	if err != nil {
		return AgentView{}, ErrIllegalAction
	}
	state, botEvents := s.drive(eng, state, m.Seed, m.botSeats())
	events = append(events, botEvents...)

	if err := s.persist(ctx, m, state, events); err != nil {
		return AgentView{}, err
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
	s.publish(m.PublicID, events)
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
	if s.wallet != nil && m.EntryFee > 0 {
		if err := s.wallet.SettleTable(ctx, m.PublicID, econ.PlatformFee, payouts); err != nil {
			return err
		}
	}

	agents := finalizeAgents(m.Agents, rewards, m.EntryFee)
	hash := mono.ReplayHash(events)
	if err := s.repo.Finish(ctx, m.PublicID, state, state.Winner, hash, agents, events); err != nil {
		return err
	}
	s.publish(m.PublicID, events)
	s.finish.MatchFinished(ctx, m.PublicID)
	return nil
}

func (s *Service) HandleTimeout(ctx context.Context, matchPublicID string) error {
	release, ok, err := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL)
	if err != nil || !ok {
		return err
	}
	defer release()

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
	return s.persist(ctx, m, state, events)
}

func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
	ids, err := s.repo.ListActiveExpired(ctx, GameName, s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.HandleTimeout(ctx, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
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

func (s *Service) Live(ctx context.Context) ([]LiveMatch, error) {
	return s.repo.LiveMatches(ctx)
}

func (s *Service) view(m Match, viewerAgent string) AgentView {
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
