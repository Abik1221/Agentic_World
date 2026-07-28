package match

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/benchmark"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/platform/telemetry"
)

// mover is the socket surface the driver needs — satisfied by *agentgw.Gateway.
// Kept as a narrow interface so the drive loop is decoupled and unit-testable.
type mover interface {
	Connected(agentID string) bool
	Turn(ctx context.Context, agentID string, view, out any) error
	GameEnd(ctx context.Context, agentID, game, matchID string, result json.RawMessage) error
}

// RemoteResolver resolves a verified hosted endpoint for an agent (satisfied by
// manifest.Service). PushClient POSTs a turn view to it and delivers game-end
// (satisfied by *agentclient.Client). Together they let the ranked driver drive
// an agent that registered an endpoint but is NOT connected over the socket.
type RemoteResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}
type PushClient interface {
	Play(ctx context.Context, t agentclient.Target, request, out any) (int, error)
	GameEnd(ctx context.Context, t agentclient.Target, n agentclient.GameEndNotification) error
}

// seatDriver is one seat's turn/game-end surface — either the live socket or a
// hosted HTTP endpoint — so the drive loop treats both identically.
type seatDriver interface {
	Turn(ctx context.Context, view, out any) error
	GameEnd(ctx context.Context, result json.RawMessage) error
}

// socketSeat drives a seat over its live WebSocket (via the mover interface).
type socketSeat struct {
	gw      mover
	id      string
	matchID string
}

func (s socketSeat) Turn(ctx context.Context, view, out any) error {
	return s.gw.Turn(ctx, s.id, view, out)
}
func (s socketSeat) GameEnd(ctx context.Context, result json.RawMessage) error {
	return s.gw.GameEnd(ctx, s.id, "goofspiel", s.matchID, result)
}

// httpSeat drives a seat by calling its verified hosted endpoint (push model).
type httpSeat struct {
	client  PushClient
	target  agentclient.Target
	matchID string
}

func (h httpSeat) Turn(ctx context.Context, view, out any) error {
	_, err := h.client.Play(ctx, h.target, view, out)
	return err
}
func (h httpSeat) GameEnd(ctx context.Context, result json.RawMessage) error {
	return h.client.GameEnd(ctx, h.target, agentclient.GameEndNotification{
		MatchID: h.matchID, Game: "goofspiel", Result: result,
	})
}

// driver auto-plays a paired, staked match by driving each agent's seat over its
// authenticated socket: it asks the connected agent for a card (over the local-
// runtime WebSocket) and submits it via DriveAct. A seat whose agent is NOT
// connected is left to self-drive (HTTP) + the sweeper's deterministic timeout, so
// an offline / hosted-endpoint agent never blocks the match.
//
// This is the "agents-vs-agents" live loop — both seats staked equally at pairing,
// so no house money is involved; settlement is the existing finalize/Settle path.
// Enabled by cfg.RankedAutoDrive (off by default until integration-tested with two
// live agents); when off, paired agents self-drive over HTTP exactly as before.
type driver struct {
	gw       mover
	resolver RemoteResolver // optional: resolve hosted endpoints for non-socket agents
	client   PushClient     // optional: POST turns to hosted endpoints
	em       *telemetry.Client
	persist  benchmark.Persist
	meta     benchmark.AgentMetaResolver
	log      *slog.Logger
	maxMatch time.Duration
}

// seatFor returns how to drive a seat: its live socket if connected, else its
// verified hosted endpoint, else (nil,false) so the seat self-drives over HTTP +
// the sweeper timeout as before. This is what lets an endpoint-only agent play
// ranked without holding a socket open.
func (d *driver) seatFor(ctx context.Context, id, matchID string) (seatDriver, bool) {
	if d.gw != nil && d.gw.Connected(id) {
		return socketSeat{gw: d.gw, id: id, matchID: matchID}, true
	}
	if d.resolver != nil && d.client != nil {
		if t, ok, err := d.resolver.PlayTarget(ctx, id); ok && err == nil {
			return httpSeat{client: d.client, target: t, matchID: matchID}, true
		}
	}
	return nil, false
}

// EnableRankedDrive turns on socket auto-driving of paired matches. em may be
// nil/disabled (benchmark telemetry is then a no-op). persist, when set, routes
// the per-match benchmark summary through the durable outbox instead of the
// best-effort emitter — so a staked match's benchmark fact is never dropped.
// meta (optional) resolves each agent's manifest metadata (version + model) for
// version-diff and provider benchmarks.
func (s *Service) EnableRankedDrive(gw mover, resolver RemoteResolver, client PushClient, em *telemetry.Client, persist benchmark.Persist, meta benchmark.AgentMetaResolver, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.driver = &driver{gw: gw, resolver: resolver, client: client, em: em, persist: persist, meta: meta, log: log, maxMatch: 5 * time.Minute}
}

// goofspielTurnView is the self-contained per-seat JSON asked over the socket; it
// mirrors what the SDK's goofspiel handler expects. It carries the FULL match
// context an agent needs to play intelligently — the running round history (each
// prize and both revealed bids), the opponent's remaining hand, rounds left, the
// shot clock, and the fairness commit — not just the current prize. Older SDKs
// that only read the original fields keep working (added fields are ignored).
type goofspielTurnView struct {
	Game             string           `json:"game"`
	MatchID          string           `json:"match_id"`
	Seat             int              `json:"seat"`
	Round            int              `json:"round"`
	TotalRounds      int              `json:"total_rounds"`
	CurrentPrize     int              `json:"current_prize"`
	PrizePool        int              `json:"prize_pool"`
	YourHand         []int            `json:"your_hand"`
	OpponentHand     []int            `json:"opponent_hand"`
	LegalActions     []int            `json:"legal_actions"`
	YourScore        int              `json:"your_score"`
	OppScore         int              `json:"opponent_score"`
	History          []goofspielRound `json:"history"`
	PrizeOrderCommit string           `json:"prize_order_commit"`
	MoveWindowMs     int64            `json:"move_window_ms"`
	DeadlineMs       int64            `json:"deadline_ms,omitempty"`
	// Chat is what has been said at the table so far, oldest first. Without it an
	// agent's `rationale` would be a monologue — it could talk but never answer.
	Chat []goofspielChatLine `json:"chat,omitempty"`
}

// goofspielChatLine is one line of table talk as an agent sees it.
type goofspielChatLine struct {
	Round int    `json:"round"`
	Seat  int    `json:"seat"`
	You   bool   `json:"you"`
	Text  string `json:"text"`
	Kind  string `json:"kind"`
}

// goofspielRound is one resolved round in the turn view's history: the prize, both
// players' revealed bids, and who took it — enough to model opponent tendencies.
type goofspielRound struct {
	Round    int    `json:"round"`
	Prize    int    `json:"prize"`
	YourCard int    `json:"your_card"`
	OppCard  int    `json:"opp_card"`
	Winner   string `json:"winner"` // "you" | "opponent" | "tie"
}

type goofspielTurnMove struct {
	Round     int                   `json:"round"`
	Card      int                   `json:"card"`
	Rationale string                `json:"rationale,omitempty"` // optional agent reasoning, captured for observability
	Usage     *benchmark.TokenUsage `json:"usage,omitempty"`
}

// maybeDrive spawns the auto-driver for a freshly-paired match when auto-driving is
// enabled and at least one seat's agent is connected over the socket.
func (s *Service) maybeDrive(matchID, aAgent, bAgent string) {
	if s.driver == nil {
		return
	}
	// Spawn only if at least one seat is drivable (socket or hosted endpoint);
	// otherwise both self-drive and there's nothing for the loop to do.
	ctx := context.Background()
	_, okA := s.driver.seatFor(ctx, aAgent, matchID)
	_, okB := s.driver.seatFor(ctx, bAgent, matchID)
	if !okA && !okB {
		return
	}
	go s.driver.run(s, matchID, aAgent, bAgent)
}

func (d *driver) run(s *Service, matchID, aAgent, bAgent string) {
	ctx, cancel := context.WithTimeout(context.Background(), d.maxMatch)
	defer cancel()
	agents := []string{aAgent, bAgent}
	// Per-match benchmark accumulator: every decision's outcome (legal / illegal /
	// timeout / transport / disconnect) + latency is folded in, then emitted once
	// as a benchmark_recorded fact per seat when the match ends.
	rec := benchmark.NewRecorder("goofspiel", matchID)
	if d.meta != nil {
		for seat, id := range agents {
			rec.SetAgentMeta(seat, id, d.meta(ctx, id))
		}
	}
	defer d.flushBenchmark(rec)
	for {
		if ctx.Err() != nil {
			d.log.Warn("ranked drive: deadline exceeded", "match", matchID)
			return
		}
		base, err := s.State(ctx, matchID, aAgent, false, 0)
		if err != nil {
			d.log.Warn("ranked drive: state read failed", "match", matchID, "err", err)
			return
		}
		if base.Status != StatusActive {
			// Record each seat's outcome (from its own perspective) for win-rate, and
			// best-effort game-end to each connected agent.
			for seat, id := range agents {
				v, _ := s.State(ctx, matchID, id, false, 0)
				rec.SetResult(seat, id, goofspielResult(v.Result))
				if sd, ok := d.seatFor(ctx, id, matchID); ok {
					result, _ := json.Marshal(v.Result)
					_ = sd.GameEnd(context.WithoutCancel(ctx), result)
				}
			}
			return
		}
		acted := false
		for seat, id := range agents {
			sd, ok := d.seatFor(ctx, id, matchID)
			if !ok {
				continue // neither socket nor endpoint — self-drive + sweeper timeout cover this seat
			}
			v, err := s.State(ctx, matchID, id, false, 0)
			if err != nil || !v.YourTurn || len(v.You.Hand) == 0 {
				continue
			}
			card, outcome, latencyMS, rationale, usage := d.decide(ctx, sd, seat, matchID, v)
			rec.Record(benchmark.Decision{
				Seat: seat, AgentID: id, Outcome: outcome, LatencyMS: latencyMS,
				Round: v.Round, Action: strconv.Itoa(card), Rationale: rationale, Usage: usage,
			})
			// Emit the decision NOW, not at match end. The Recorder above is an
			// in-process buffer flushed once when the match finishes and capped at 256
			// moves, so an arena crash lost every decision in the match and a long game
			// silently stopped recording. This event is durable on its own.
			if s.decisionTracer != nil {
				de := telemetry.DecisionEvent{
					Game: "goofspiel", MatchID: matchID, AgentID: id, Seat: seat,
					Round: v.Round, Action: strconv.Itoa(card), Outcome: string(outcome),
					LatencyMS: latencyMS, Rationale: rationale,
					MeterSource: telemetry.MeterSourceSDK,
				}
				if usage != nil {
					de.Provider, de.Model = usage.Provider, usage.Model
					de.PromptTokens = int64(usage.PromptTokens)
					de.CompletionTokens = int64(usage.CompletionTokens)
					de.TotalTokens = int64(usage.TotalTokens)
				}
				s.decisionTracer.EmitAgentDecision(de)
			}
			// Publish the agent's reasoning as table talk BEFORE its card lands, so
			// spectators (and the opponent, who receives the transcript on its next
			// view) see it argue its move rather than a silent number appearing.
			// Best-effort: a rejected line must never block the move.
			if strings.TrimSpace(rationale) != "" {
				if _, serr := s.Say(ctx, id, matchID, rationale, gs.ChatKindRationale); serr != nil {
					d.log.Debug("ranked drive: table talk not posted", "err", serr)
				}
			}
			if _, err := s.DriveAct(ctx, id, matchID, v.Round, card); err == nil {
				acted = true
			}
		}
		if !acted {
			// Round resolving, or the only actionable seat is a self-drive one — yield.
			time.Sleep(150 * time.Millisecond)
		}
	}
}

// goofspielResult maps a seat's result view (from its own perspective) to a
// benchmark Result for win-rate. Nil (match ended without a result) → unknown.
func goofspielResult(r *resultView) benchmark.Result {
	if r == nil {
		return ""
	}
	return benchmark.ResultFromLabel(r.Winner)
}

// flushBenchmark emits the per-match benchmark summary at match end (durable via
// the outbox when wired, else best-effort emitter). Shared logic lives in
// benchmark.Flush; this wrapper just logs a persist failure.
func (d *driver) flushBenchmark(rec *benchmark.Recorder) {
	if err := benchmark.Flush(rec, d.persist, d.em, "ranked"); err != nil {
		d.log.Warn("ranked drive: benchmark persist failed", "err", err)
	}
}

// decide asks the connected agent for its card; on transport failure or an illegal
// card it falls back to the lowest card in hand (deterministic, engine-legal) so a
// flaky agent loses the round rather than wedging the match.
func (d *driver) decide(ctx context.Context, sd seatDriver, seat int, matchID string, v AgentView) (int, benchmark.Outcome, int64, string, *benchmark.TokenUsage) {
	hist := make([]goofspielRound, 0, len(v.History))
	for _, r := range v.History {
		hist = append(hist, goofspielRound{
			Round: r.Round, Prize: r.Prize,
			YourCard: r.YourCard, OppCard: r.OppCard, Winner: r.Winner,
		})
	}
	chat := make([]goofspielChatLine, 0, len(v.Chat))
	for _, c := range v.Chat {
		chat = append(chat, goofspielChatLine{
			Round: c.Round, Seat: c.Seat, You: c.You, Text: c.Text, Kind: c.Kind,
		})
	}
	req := goofspielTurnView{
		Chat: chat,
		Game: "goofspiel", MatchID: matchID, Seat: seat, Round: v.Round,
		TotalRounds:  v.TotalRounds,
		CurrentPrize: v.CurrentPrize, PrizePool: v.PrizePool,
		YourHand:     v.You.Hand,
		OpponentHand: v.Opponent.Hand,
		LegalActions: v.LegalActions.PlayCardFrom,
		YourScore:    v.You.Score, OppScore: v.Opponent.Score,
		History:          hist,
		PrizeOrderCommit: v.PrizeOrderCommit,
		MoveWindowMs:     v.MoveWindowMs,
		DeadlineMs:       v.DeadlineMs,
	}
	var move goofspielTurnMove
	start := time.Now()
	err := sd.Turn(ctx, req, &move)
	latencyMS := time.Since(start).Milliseconds()
	switch {
	case err != nil:
		// Transport/timeout/disconnect: the engine falls back deterministically.
		return lowestInt(v.You.Hand), benchmark.ClassifyError(err, errors.Is(err, agentgw.ErrNotConnected)), latencyMS, move.Rationale, move.Usage
	case !containsInt(v.You.Hand, move.Card):
		// The agent answered, but with an illegal card → fallback.
		return lowestInt(v.You.Hand), benchmark.OutcomeIllegal, latencyMS, move.Rationale, move.Usage
	default:
		return move.Card, benchmark.OutcomeOK, latencyMS, move.Rationale, move.Usage
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func lowestInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	best := xs[0]
	for _, x := range xs[1:] {
		if x < best {
			best = x
		}
	}
	return best
}
