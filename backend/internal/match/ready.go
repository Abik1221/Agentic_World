package match

import (
	"context"
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
	AskReady(ctx context.Context, agentPublicID, matchPublicID string, deadline time.Time) error
}

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
			if s.readyAsker != nil {
				if err := s.readyAsker.AskReady(ctx, agent, matchPublicID, now.Add(pol.Window)); err != nil {
					// Best-effort: an undeliverable ask is the same as an unanswered one,
					// and the next tick handles it. Aborting here would let one unreachable
					// seat freeze the table for everyone.
					slog.Debug("ready check: could not deliver the ask", "match", matchPublicID, "agent", agent, "error", err)
				}
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
