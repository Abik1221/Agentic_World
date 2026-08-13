package match

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/readycheck"
)

// The service half of the ready check: acknowledge a seat, and drive one table's decision.
//
// The rule everything here protects: NOTHING IS ESCROWED UNTIL EVERY SEAT IS READY. Escrow
// happens in exactly one place below — the Start branch of ReadyTick — and every other path
// out of a ready-check table leaves the coins where they are.
//
// CreatePaired is deliberately NOT switched to this path yet. It is switched last, once this
// exists to move tables out of ready_check; until then a paired table would land there with
// nothing to release it and matchmaking would silently stop producing games.

// ReadyRepo is the persistence this needs. Declared here, next to its only user, rather than
// widening the main Repo interface — the ready check is a self-contained state machine and a
// deployment without it should not have to implement five methods to compile.
type ReadyRepo interface {
	// CreatePairedReadyCheck persists a dealt-but-unstarted table. On ReadyRepo rather than
	// the main Repo interface on purpose: it is only ever reachable when a ready check is
	// configured, so widening Repo would force every implementation — and every test fake —
	// to carry a method most of them can never call.
	CreatePairedReadyCheck(ctx context.Context, in CreatePairedInput) error
	MarkReady(ctx context.Context, matchPublicID, agentPublicID string, at time.Time) (bool, error)
	ReadySeats(ctx context.Context, matchPublicID string) ([]ReadySeat, error)
	RecordAsk(ctx context.Context, matchPublicID, agentPublicID string, at time.Time) error
	ActivateAfterReady(ctx context.Context, matchPublicID string, startsAt, deadline time.Time) (bool, error)
	AbandonReadyCheck(ctx context.Context, matchPublicID string) error
}

// ReadyAsker delivers "are you there?" to a seat. Satisfied by the push client.
//
// Best-effort by design: a failed ask is indistinguishable from an agent that did not answer,
// and both are handled the same way — the seat gets another ask, then it is dropped. Returning
// an error here must never abort the sweep, because one unreachable seat cannot be allowed to
// freeze a table for everyone else.
type ReadyAsker interface {
	AskReady(ctx context.Context, ask ReadyAsk) error
}

// ReadyAsk is one seat's ask.
//
// A struct rather than positional arguments because the ask now carries THREE strings
// (agent, match, game) and two of them would sit adjacent in a parameter list — a silent
// swap that compiles. That is exactly the failure this type exists to prevent: the ask used
// to hardcode Game:"goofspiel" and Players:2, so an agent seated at a 12-player Mafia table
// was told it was joining a 2-player Goofspiel match. An SDK that branches on `game` to pick
// a handler would have loaded the wrong one, and the developer's first evidence would be
// their agent playing badly rather than an error.
type ReadyAsk struct {
	AgentPublicID string
	MatchPublicID string
	// Game as the match itself records it — never a default. The whole point of the type.
	Game string
	// Players is the number of seats actually at this table right now, not the policy's
	// maximum: a Mafia table that filled 7 of 12 is a 7-player game to the agent being asked.
	Players int
	// Deadline is when the ask expires, so an agent can decide whether it can be ready in
	// time rather than guessing.
	Deadline time.Time
}

// ReadyStarter tells a seat its table is about to begin, and when.
//
// One-way, delivered through the SAME outbox that carries every other game event, so it is
// signed, retried and health-gated like the rest rather than being a bespoke push. The
// existing "event" lifecycle call is reused rather than a new frame type invented: both SDKs
// already handle /event, so a match_start needs no protocol version bump and no SDK release —
// the same reasoning that let the ready ask reuse /initialize.
//
// Optional. Without it an agent still plays: it simply learns the match began by receiving its
// first turn, which is what happens today. What it loses is the ability to show a countdown,
// which is the whole point for a developer watching a terminal.
type ReadyStarter interface {
	EnqueueEvent(ctx context.Context, agentPublicID, game, matchID string, seq int, eventType string, payload []byte) error
}

// QueueEventRecorder records what happened to a seat during a ready check, for reporting.
//
// OPTIONAL — every call is nil-checked. A ready check must behave identically with no
// observability attached: this decides whether real coins are escrowed, and a reporting hook
// that could fail it would be a fraud-adjacent outage caused by a chart.
//
// Declared here rather than importing the store so the dependency points the usual way. The
// adapter lives with the repository.
type QueueEventRecorder interface {
	// ReadyAsked: this seat was asked to confirm it is there.
	ReadyAsked(ctx context.Context, agentPublicID, matchPublicID string)
	// ReadyOK: it confirmed.
	ReadyOK(ctx context.Context, agentPublicID, matchPublicID string)
	// Dropped: it never answered and lost its place (never its stake — nothing was escrowed).
	// This is the "unreachable after the developer started it" number.
	Dropped(ctx context.Context, agentPublicID, matchPublicID, reason string)
}

// SetQueueEvents attaches the reporting recorder. Nil keeps the ready check silent.
func (s *Service) SetQueueEvents(r QueueEventRecorder) { s.queueEvents = r }

// ReadyRequeuer puts a dropped seat's agent back into matchmaking.
//
// A seat that missed its window loses its place, not its money. Requeueing is what makes that
// true in practice rather than only in principle: without it, being briefly unreachable would
// mean silently falling out of the arena.
type ReadyRequeuer interface {
	Requeue(ctx context.Context, agentPublicID, ownerPublicID string, bid int64) error
}

// SetReadyCheck installs the ready-check collaborators. Absent any of them the service behaves
// exactly as before, which is the safe default: no ready gate rather than a half-driven one.
func (s *Service) SetReadyCheck(r ReadyRepo, asker ReadyAsker, requeue ReadyRequeuer) {
	s.readyRepo, s.readyAsker, s.readyRequeue = r, asker, requeue
}

// SetReadyStarter installs the match_start notifier. Nil ⇒ no countdown is announced and
// agents learn the match began from their first turn, exactly as they do today.
func (s *Service) SetReadyStarter(n ReadyStarter) { s.readyStarter = n }

// announceStart tells both seats when play begins.
//
// Best-effort and AFTER activation: the match is already live and already escrowed, so a failed
// announcement costs a countdown, never a game. Blocking activation on a notification would let
// an unreachable agent hold up a table that has already taken everyone's stake.
func (s *Service) announceStart(ctx context.Context, m Match, startsAt time.Time, seats []ReadySeat) {
	if s.readyStarter == nil {
		return
	}
	// Absolute instant plus the server's clock, the same pair the view ships — so a terminal
	// and a browser count to the same moment instead of each counting down from ten and
	// drifting apart. server_now is what lets a client with a skewed clock still be right.
	payload, err := json.Marshal(map[string]any{
		"match_id":   m.PublicID,
		"game":       m.Game,
		"starts_at":  startsAt.UTC(),
		"server_now": s.clock.Now().UTC(),
	})
	if err != nil {
		return
	}
	for _, seat := range seats {
		if err := s.readyStarter.EnqueueEvent(ctx, seat.AgentPublicID, m.Game, m.PublicID, 0, "match_start", payload); err != nil {
			slog.Debug("ready check: could not announce the start", "match", m.PublicID, "agent", seat.AgentPublicID, "error", err)
		}
	}
}

// Ready records that a seat has acknowledged it is present and willing to play.
//
// Idempotent at the storage layer, so a retried ack is the same agent answering once. The
// caller is told nothing about whether it was the first — an agent that retried should see
// success, not a conflict it cannot act on.
func (s *Service) Ready(ctx context.Context, agentPublicID, matchPublicID string) error {
	if s.readyRepo == nil {
		return httpx.NewError(409, "ready_check_disabled", "This deployment does not run a ready check.")
	}
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return ErrNotFound
	}
	if m.Status != StatusReadyCheck {
		// Covers both "already started" and "already abandoned". Neither is an error the
		// agent can do anything about, and both mean the same thing to it: stop waiting.
		return httpx.NewError(409, "not_ready_check", "This match is not waiting for readiness.")
	}
	if m.playerByAgent(agentPublicID) == nil {
		return ErrNotPlayer
	}
	// Recorded on the way in, not after MarkReady: the seat HAS answered by this point, and
	// the funnel is about who answered, not about whether the write succeeded.
	if s.queueEvents != nil {
		s.queueEvents.ReadyOK(ctx, agentPublicID, matchPublicID)
	}
	if _, err := s.readyRepo.MarkReady(ctx, matchPublicID, agentPublicID, s.clock.Now()); err != nil {
		return err
	}
	return nil
}

// ReadyTick advances one ready-check table by a single decision.
//
// Driven by the sweeper. Returns whether the table left ready_check, so a caller can stop
// polling it.
//
// Every branch that ends the table without starting it leaves the stakes untouched — that is
// not an omission, it is the guarantee. Escrow appears exactly once, in Start.
func (s *Service) ReadyTick(ctx context.Context, matchPublicID string) (done bool, err error) {
	if s.readyRepo == nil {
		return true, nil
	}
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return true, nil // gone; nothing to drive
	}
	if m.Status != StatusReadyCheck {
		return true, nil
	}

	seats, err := s.readyRepo.ReadySeats(ctx, matchPublicID)
	if err != nil {
		// Fails CLOSED on a read error: the table stays in ready_check and is retried. The
		// alternative — guessing — either starts a match nobody agreed to or abandons one
		// everybody did, and both are worse than waiting for the database to come back.
		return false, err
	}

	pol := readycheck.DefaultPolicy(m.Game)
	now := s.clock.Now()
	d := readycheck.Evaluate(pol, toReadySeats(seats, pol), now)

	switch d.Action {
	case readycheck.Ask:
		for _, agent := range d.Seats {
			// Recorded BEFORE the ask goes out. If the process dies between the two, the
			// seat has used an ask it never received — which costs it one of two chances.
			// The other order costs it nothing but lets an ask that was delivered go
			// uncounted, so a silent seat could be asked forever and hold the table open.
			if err := s.readyRepo.RecordAsk(ctx, matchPublicID, agent, now); err != nil {
				return false, err
			}
			// Recorded alongside the durable ask, for the same reason and in the same order:
			// the funnel should agree with what the ready check itself believes happened.
			if s.queueEvents != nil {
				s.queueEvents.ReadyAsked(ctx, agent, matchPublicID)
			}
			if s.readyAsker != nil {
				err := s.readyAsker.AskReady(ctx, ReadyAsk{
					AgentPublicID: agent,
					MatchPublicID: matchPublicID,
					Game:          m.Game,
					Players:       len(seats),
					Deadline:      now.Add(pol.Window),
				})
				if err == nil {
					// THE ASK IS SYNCHRONOUS AND ITS ANSWER IS THE ACKNOWLEDGEMENT.
					//
					// /initialize returns {"ready": true} in the same round trip, so a
					// successful ask means the seat has already answered — there is no
					// second message coming. Recording it here is what turns the answer
					// into readiness.
					//
					// Without this the ack was detected and DISCARDED: the asker read
					// resp.Ready, returned success, and nothing ever called MarkReady. Every
					// table was asked twice and dropped while its agents were answering
					// correctly the whole time. Found by running it, not by reading it — the
					// unit tests pass an asker that only reports delivery, so they could not
					// see the gap.
					//
					// MarkReady is idempotent, so an agent that ALSO calls /ready explicitly
					// (the async path) is the same seat answering once.
					if _, mErr := s.readyRepo.MarkReady(ctx, matchPublicID, agent, now); mErr != nil {
						slog.Debug("ready check: could not record an acknowledgement", "match", matchPublicID, "agent", agent, "error", mErr)
					}
					continue
				}
				// Best-effort: an undeliverable ask is the same as an unanswered one, and the
				// next tick handles it. Aborting here would let one unreachable seat freeze
				// the table for everyone.
				slog.Debug("ready check: could not deliver the ask", "match", matchPublicID, "agent", agent, "error", err)
			}
		}
		return false, nil

	case readycheck.Drop:
		// The seat loses its place, not its stake — nothing was ever escrowed. Requeueing is
		// what makes that concrete: without it, a brief outage means falling out of the arena.
		for _, agent := range d.Seats {
			owner := ownerOf(seats, agent)
			slog.Info("ready check: dropping a seat that never answered — no stake was taken, requeueing it",
				"match", matchPublicID, "agent", agent, "asks", pol.MaxAsks)
			// The funnel's "unreachable after start" count. Recorded before the requeue so a
			// requeue failure cannot also lose the record of why the seat went.
			if s.queueEvents != nil {
				s.queueEvents.Dropped(ctx, agent, matchPublicID, "no_answer")
			}
			if s.readyRequeue != nil && owner != "" {
				if err := s.readyRequeue.Requeue(ctx, agent, owner, m.Bid); err != nil {
					slog.Debug("ready check: requeue failed", "agent", agent, "error", err)
				}
			}
		}
		// A dropped seat leaves the table below its roster, so the table itself cannot
		// continue. Abandoning releases the seats that DID answer to requeue immediately
		// rather than sit behind a table that can no longer fill.
		if err := s.abandonAndRequeueRest(ctx, m, seats, d.Seats); err != nil {
			return false, err
		}
		return true, nil

	case readycheck.Abandon:
		return true, s.abandonAndRequeueRest(ctx, m, seats, nil)

	case readycheck.Start:
		return s.startAfterReady(ctx, m, seats, d.StartsIn)

	default: // Wait
		return false, nil
	}
}

// startAfterReady escrows and activates, in that order.
//
// The ONLY place a ready-check table takes money. Escrow first, activate second, refund if the
// activation fails — the same shape CreatePaired already uses, for the same reason: a match
// that is active with nothing escrowed pays out coins that were never staked.
func (s *Service) startAfterReady(ctx context.Context, m Match, seats []ReadySeat, in time.Duration) (bool, error) {
	if len(seats) < 2 {
		return true, s.readyRepo.AbandonReadyCheck(ctx, m.PublicID)
	}
	a, b := seats[0].AgentPublicID, seats[1].AgentPublicID

	if err := s.wallet.StakeMatch(ctx, m.PublicID, a, b, m.Bid); err != nil {
		// Could not take the stakes, so the table cannot start. Nothing to unwind.
		slog.Info("ready check: could not escrow after readiness; abandoning without taking anything",
			"match", m.PublicID, "error", err)
		return true, s.readyRepo.AbandonReadyCheck(ctx, m.PublicID)
	}

	now := s.clock.Now()
	startsAt := readycheck.StartsAt(now, in)
	// The first turn's window opens when play does, not when the countdown does — otherwise
	// the countdown eats the first seat's thinking time.
	deadline := startsAt.Add(s.moveWindow(ctx, a, b))

	won, err := s.readyRepo.ActivateAfterReady(ctx, m.PublicID, startsAt, deadline)
	if err != nil || !won {
		// Lost the race to another sweeper, or the write failed after the money moved.
		// Refund either way: the stakes must never outlive the attempt that took them.
		_ = s.wallet.RefundStakes(ctx, m.PublicID, a, b, m.Bid)
		return err == nil, err
	}
	// Publishing and driving move HERE from CreatePaired, and both had to move together.
	// Announcing the match at pairing would tell every consumer a game began before its
	// seats had agreed to play, and driving it would ask for a move on a table that is not
	// active yet — tryAct would refuse, and the driver would burn its first turn on a
	// rejection. TestEveryActivationPathStartsTheDriver is the guard: an activation path
	// that forgets to drive leaves a live staked table nobody is playing.
	s.publish(m.PublicID, m.State, nil)
	s.announceStart(ctx, m, startsAt, seats)
	s.maybeDrive(m.PublicID, a, b)
	slog.Info("ready check: every seat is ready — escrowed and starting",
		"match", m.PublicID, "starts_at", startsAt, "countdown", in)
	return true, nil
}

// abandonAndRequeueRest releases a table that cannot start and returns the seats that DID
// answer to the queue. They did nothing wrong; holding them behind a dead table would punish
// the agents that were on time.
func (s *Service) abandonAndRequeueRest(ctx context.Context, m Match, seats []ReadySeat, dropped []string) error {
	isDropped := make(map[string]bool, len(dropped))
	for _, a := range dropped {
		isDropped[a] = true
	}
	for _, st := range seats {
		if isDropped[st.AgentPublicID] || st.ReadyAt == nil {
			continue
		}
		if s.readyRequeue != nil {
			if err := s.readyRequeue.Requeue(ctx, st.AgentPublicID, st.OwnerPublicID, m.Bid); err != nil {
				slog.Debug("ready check: requeue failed", "agent", st.AgentPublicID, "error", err)
			}
		}
	}
	return s.readyRepo.AbandonReadyCheck(ctx, m.PublicID)
}

// toReadySeats maps storage rows onto the pure state machine's input.
//
// The pointer-to-value conversion is where a NULL would otherwise become a zero time.Time —
// January year 1, which reads as a very expired ask. A seat that was never asked has Asks == 0
// and the state machine treats that as "nobody has spoken to them", never as silence.
func toReadySeats(rows []ReadySeat, _ readycheck.Policy) []readycheck.Seat {
	out := make([]readycheck.Seat, 0, len(rows))
	for _, r := range rows {
		s := readycheck.Seat{AgentPublicID: r.AgentPublicID, Ready: r.ReadyAt != nil, Asks: r.Asks}
		if r.AskedAt != nil {
			s.AskedAt = *r.AskedAt
		}
		out = append(out, s)
	}
	return out
}

func ownerOf(seats []ReadySeat, agent string) string {
	for _, s := range seats {
		if s.AgentPublicID == agent {
			return s.OwnerPublicID
		}
	}
	return ""
}
