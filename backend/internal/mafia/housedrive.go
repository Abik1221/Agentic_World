package mafia

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// houseDriveMaxMatch bounds a house-seat driver's lifetime. A Mafia game is already
// bounded by MaxDays and by each phase's deadline, so this is only a backstop against a
// goroutine outliving a table that got wedged — not the mechanism that ends a match.
const houseDriveMaxMatch = 2 * time.Hour

// DefaultHouseDriveInterval paces the driver. Bots decide in microseconds, so without
// this they would act the instant a phase opened and, in discussion, take every message
// slot before a real agent's first LLM call returned. A bot that answers faster than any
// LLM-backed agent possibly could is not a fair table. Override with
// Config.HouseDriveInterval.
const DefaultHouseDriveInterval = time.Second

// DriveHouseSeats advances the house-bot seats at a live table until the match ends.
//
// It exists because the only Mafia drive loop was push-play's, which drives exactly ONE
// developer seat plus engine bots for everything else, and is started only by
// StartPushPlay. A table that group matchmaking filled with house bots has no such loop:
// its real agents act for themselves over the API, but nothing would ever act for the
// fillers. Those seats would sit silent until each phase's deadline expired and
// ForceTimeout resolved it — so a bot-filled table would technically progress while
// running at the pace of its timeouts and never voting, speaking, or killing. Working
// enough to look finished, and useless to play.
//
// Non-blocking: it spawns its own goroutine and returns. Safe to call with an empty seat
// list (no-op), which is what a table that filled with real agents passes.
func (s *Service) DriveHouseSeats(matchPublicID string, houseAgentIDs []string) {
	if len(houseAgentIDs) == 0 {
		return
	}
	ids := append([]string(nil), houseAgentIDs...) // caller's slice may be reused
	go s.driveHouseSeats(matchPublicID, ids)
}

func (s *Service) driveHouseSeats(matchPublicID string, houseAgentIDs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), houseDriveMaxMatch)
	defer cancel()

	log := slog.Default()
	// One bot per seat, cached for the whole match so each keeps its deterministic rng
	// and its accumulated private knowledge (a detective remembers what it proved).
	bots := map[int]*mf.Bot{}

	// When each seat is willing to act, keyed by the phase it is acting in.
	//
	// Every house seat used to answer in the same pass of this loop, so twelve players
	// spoke and voted inside one tick — instantly, together, in seat order. Nothing else
	// about a table gives the game away as completely: a human reads simultaneity as
	// machinery long before they notice anything about the moves themselves.
	//
	// So each seat now takes its own moment to answer. The delay is DETERMINISTIC — drawn
	// from the match id, the seat and the phase — so a replay of the same match paces
	// identically and the pause cannot become a source of flakiness in tests.
	//
	// It is a floor on when a seat MAY act, never a sleep: the loop keeps serving every
	// other seat while one is thinking. Blocking here would make the whole table wait on
	// the slowest thinker, which is the opposite of the intent.
	readyAt := map[phaseKey]time.Time{}

	for {
		select {
		case <-ctx.Done():
			log.Warn("mafia house driver: deadline exceeded", "match", matchPublicID)
			return
		case <-time.After(s.cfg.HouseDriveInterval):
		}

		for _, id := range houseAgentIDs {
			v, err := s.State(ctx, matchPublicID, id, false, 0)
			if err != nil {
				// A read failure is transient (lock contention, a blip); the next pass
				// retries. Only a finished match ends this loop.
				continue
			}
			if v.Status != StatusActive {
				return // settled, aborted, or gone — nothing left to drive
			}
			if len(v.Legal) == 0 {
				continue // this seat has nothing pending in the current phase
			}
			// Take a moment before answering, per seat and per phase.
			k := phaseKey{seat: v.YourSeat, day: v.Day, phase: string(v.Phase)}
			when, seen := readyAt[k]
			if !seen {
				when = time.Now().Add(thinkFor(matchPublicID, k))
				readyAt[k] = when
			}
			if time.Now().Before(when) {
				continue // still thinking — the loop serves the other seats meanwhile
			}
			bot := bots[v.YourSeat]
			if bot == nil {
				bot = mf.NewBot("house", []byte(matchPublicID+":"+id), v.YourSeat)
				bots[v.YourSeat] = bot
			}
			act := bot.Decide(toEngineView(v))
			if act.Kind == "" || !containsStr(v.Legal, act.Kind) {
				// Same guard the push-play loop uses: fall back to a simple legal pick so a
				// bot can never stall a seat by proposing something illegal.
				act = botDecide(v)
			}
			// platformDriven: no stale-phase guard and no per-move signature, exactly as
			// push-play drives its bot seats. The engine still validates every move, so a
			// move for an already-resolved phase is rejected and retried next pass.
			if _, err := s.Act(ctx, id, matchPublicID, act, 0, "", "", true); err != nil {
				log.Debug("mafia house driver: act rejected", "match", matchPublicID, "agent", id, "err", err)
			}
		}
	}
}

// phaseKey identifies one seat's turn to act: the same seat in a later phase is a new
// decision and gets its own pause.
type phaseKey struct {
	seat  int
	day   int
	phase string
}

// thinkFor is how long a house seat appears to consider its move.
//
// # Why it is derived rather than random
//
// A replay has to pace like the match it replays, and a test that drives a table has to
// be able to predict it. Drawing from the match id and the seat gives both: the same
// table always thinks for the same intervals, and two different tables do not share a
// rhythm. math/rand here would make the driver a source of flakiness for anything timing
// a phase.
//
// # Why the ranges differ by phase
//
// Discussion is where a player is composing a sentence and voting is where they are
// committing to one, so discussion runs longer. Night is quick: the acting roles are
// picking a name off a short list, and a long night is just dead air for everyone with
// no night role.
//
// Every range sits well inside the phase's own window, so a paced seat cannot miss its
// turn — the pause makes a bot look like it is thinking, and must never make it look
// like it timed out.
func thinkFor(matchID string, k phaseKey) time.Duration {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|think|%d|%d|%s", matchID, k.seat, k.day, k.phase))
	// 16 bits is plenty of spread for a sub-10s range and keeps the arithmetic obvious.
	n := int(sum[0])<<8 | int(sum[1])

	var lo, hi int // milliseconds
	switch k.phase {
	case mf.PhaseDiscussion:
		lo, hi = 900, 6500
	case mf.PhaseVoting:
		lo, hi = 700, 3800
	default: // night, morning, anything new
		lo, hi = 400, 1800
	}
	return time.Duration(lo+n%(hi-lo)) * time.Millisecond
}
