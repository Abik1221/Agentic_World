package mafia

import (
	"context"
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
