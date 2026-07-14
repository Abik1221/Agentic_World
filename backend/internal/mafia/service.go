package mafia

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/platform"
)

// Config tunes phase windows and table economics.
type Config struct {
	EntryFee       int64
	PlatformFeePct int
	PhaseWindow    time.Duration
	LockTTL        time.Duration
	RosterSize     int
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
	eng    *mf.Engine
	// pusher is set by EnablePushPlay to enable POST /v1/mafia/pushplay
	// (manifest push model with bot-filled seats). Nil ⇒ push-play returns 501.
	pusher *pushPlayer
	// notify wakes long-polling State callers on a state change. Nil ⇒ no long-poll.
	notify Notifier
}

func NewService(repo Repo, lock Locker, limits Limits, wallet Wallet, bcast Broadcaster, ver Verifier, finish FinishHook, clock platform.Clock, cfg Config) *Service {
	if cfg.PhaseWindow <= 0 {
		cfg.PhaseWindow = 45 * time.Second
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = 15 * time.Second
	}
	if cfg.EntryFee <= 0 {
		cfg.EntryFee = DefaultEntryFee
	}
	if cfg.PlatformFeePct <= 0 {
		cfg.PlatformFeePct = DefaultPlatformFeePct
	}
	if cfg.RosterSize <= 0 {
		cfg.RosterSize = mf.RosterSize
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
	return &Service{repo: repo, lock: lock, limits: limits, wallet: wallet, bcast: bcast, ver: ver, finish: finish, clock: clock, cfg: cfg, eng: mf.New()}
}

func lockKey(id string) string { return "mafia:lock:" + id }

func (s *Service) Lobby(ctx context.Context, entryFee int64, ownerPublicID string) ([]LobbyItem, error) {
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
		RakePct:  s.cfg.PlatformFeePct,
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

	deadline := s.clock.Now().Add(s.cfg.PhaseWindow)
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
	s.publish(m.PublicID, events)
	return nil
}

// Act applies a seat's action. expectedDay/expectedPhase are the (day, phase) the
// CLIENT computed the action for (from the state it read); when provided (day != 0)
// they are rejected if the game has since advanced — so a late action for an
// already-resolved phase is refused rather than absorbed into the current same-kind
// phase. Pass 0/"" to skip the check (internal/bot callers). (G1)
func (s *Service) Act(ctx context.Context, agentPublicID, matchPublicID string, act mf.Action, expectedDay int, expectedPhase string) (AgentView, error) {
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

func (s *Service) persist(ctx context.Context, m Match, state mf.State, events []mf.Event) error {
	if state.Finished {
		return s.finalize(ctx, m, state, events)
	}
	deadline := s.clock.Now().Add(s.cfg.PhaseWindow)
	if err := s.repo.Advance(ctx, m.PublicID, state, &deadline, state.Alive, events); err != nil {
		return err
	}
	s.publish(m.PublicID, events)
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
	// A zero-fee practice table staked nothing, so there is nothing to settle.
	if m.EntryFee > 0 {
		if err := s.wallet.SettleTable(ctx, m.PublicID, econ.PlatformFee, payouts); err != nil {
			return err
		}
	}

	players := finalizePlayers(m.Players, state, rewards, m.EntryFee)
	hash := replayHash(events)
	if err := s.repo.Finish(ctx, m.PublicID, state, state.Winner, hash, players, events); err != nil {
		return err
	}
	s.publish(m.PublicID, events)
	s.finish.MatchFinished(ctx, m.PublicID)
	return nil
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

// Notifier wakes long-polling State callers when the match changes. Satisfied by
// *store.Notifier (structural). Nil ⇒ State returns immediately (no long-poll).
type Notifier interface {
	Notify(matchPublicID string)
	Subscribe(matchPublicID string) (events <-chan struct{}, cancel func())
}

// SetNotifier installs the wake-up channel (called once at wiring time).
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// publish broadcasts events to spectators AND wakes any long-polling State callers.
func (s *Service) publish(matchID string, events []mf.Event) {
	s.bcast.Broadcast(matchID, events)
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
	}
	if m.Status == StatusActive {
		v.Deadline = m.RoundDeadline
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
