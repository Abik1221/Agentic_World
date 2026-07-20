package monopoly

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/agentwire"
	"github.com/agent-arena/arena/internal/benchmark"
	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/webhook"
)

// This file adds push-play to Monopoly: the platform drives the developer's seat
// (seat 0) of a no-stakes table by calling their hosted agent endpoint (manifest
// push model) for each decision, while the engine's deterministic bots fill and
// play the remaining seats. It reuses the exact same match machinery — the
// browser watches live over the normal SSE/state endpoints.

// RemoteResolver returns the push-play Target for an agent's active, verified
// manifest. Implemented by manifest.Service.
type RemoteResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// PushClient POSTs a game view to the developer's endpoint and decodes the move.
// Implemented by *agentclient.Client.
type PushClient interface {
	Play(ctx context.Context, t agentclient.Target, request, out any) (int, error)
	Initialize(ctx context.Context, t agentclient.Target, req agentclient.InitializeRequest) (agentclient.InitializeResponse, error)
	Event(ctx context.Context, t agentclient.Target, n agentclient.EventNotification) error
	GameEnd(ctx context.Context, t agentclient.Target, n agentclient.GameEndNotification) error
}

type pushPlayer struct {
	remote   RemoteResolver
	client   PushClient
	enqueue  webhook.Enqueuer // durable async /event + /game-end; nil => inline fallback
	gw       *agentgw.Gateway // local-runtime socket; nil disables the socket path
	em       *telemetry.Client
	persist  benchmark.Persist
	meta     benchmark.AgentMetaResolver
	log      *slog.Logger
	maxMatch time.Duration
}

// EnablePushPlay wires the optional POST /v1/monopoly/pushplay capability.
func (s *Service) EnablePushPlay(remote RemoteResolver, client PushClient, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.pusher = &pushPlayer{remote: remote, client: client, log: log, maxMatch: 5 * time.Minute}
}

// SetWebhookEnqueuer routes async /event + /game-end through the durable webhook
// queue. Call after EnablePushPlay; a no-op if push-play isn't enabled.
func (s *Service) SetWebhookEnqueuer(e webhook.Enqueuer) {
	if s.pusher != nil {
		s.pusher.enqueue = e
	}
}

// SetGateway wires the local-runtime WebSocket gateway. Call after EnablePushPlay.
func (s *Service) SetGateway(gw *agentgw.Gateway) {
	if s.pusher != nil {
		s.pusher.gw = gw
	}
}

// SetBenchmark wires per-match benchmark telemetry. em ships decision-quality
// summaries to Pyyol Lens; persist (optional) routes them durably through the
// outbox. Call after EnablePushPlay; a no-op if push-play isn't enabled.
func (s *Service) SetBenchmark(em *telemetry.Client, persist benchmark.Persist, meta benchmark.AgentMetaResolver) {
	if s.pusher != nil {
		s.pusher.em = em
		s.pusher.persist = persist
		s.pusher.meta = meta
	}
}

// transport picks the socket if the agent is connected, else the hosted endpoint.
func (p *pushPlayer) transport(agentID string, target agentclient.Target) agentwire.Transport {
	if p.gw != nil && p.gw.Connected(agentID) {
		return agentwire.SocketTransport{GW: p.gw, AgentID: agentID, Game: "monopoly"}
	}
	return agentwire.HTTPTransport{
		Client: p.client, Target: target, Enqueue: p.enqueue,
		AgentID: agentID, Game: "monopoly", Log: p.log,
	}
}

// monopolyGameEnd is the fat /game-end payload: the settled result, the final
// board, AND the full itemized engine event log (every roll/rent/purchase/trade),
// so the agent has a complete, replayable record in one payload.
type monopolyGameEnd struct {
	Result     any          `json:"result"`
	MatchID    string       `json:"match_id"`
	Game       string       `json:"game"`
	Seat       int          `json:"seat"`
	FinalState *mono.State  `json:"final_state,omitempty"`
	Log        []mono.Event `json:"log,omitempty"`
}

// MonopolyPushView is the JSON the platform POSTs to the agent endpoint each turn.
// It carries the redacted board (future cards stripped), the legal action kinds,
// and which seat the agent holds.
type MonopolyPushView struct {
	Game         string      `json:"game"` // "monopoly"
	MatchID      string      `json:"match_id"`
	Seat         int         `json:"seat"`
	Phase        string      `json:"phase"`
	LegalActions []string    `json:"legal_actions"`
	State        *mono.State `json:"state"`
}

// MonopolyPushMove is the action the agent returns.
type MonopolyPushMove struct {
	Action    string                `json:"action"`
	Property  int                   `json:"property"`
	Amount    int                   `json:"amount"`
	Trade     *mono.Trade           `json:"trade,omitempty"`     // required to originate a propose_trade
	Rationale string                `json:"rationale,omitempty"` // optional agent reasoning, captured for observability
	Usage     *benchmark.TokenUsage `json:"usage,omitempty"`
}

// StartPushPlay opens a no-stakes Monopoly table (creator seat 0 + engine bots)
// and drives seat 0 from the developer's hosted endpoint. Requires an active,
// endpoint-verified manifest.
func (s *Service) StartPushPlay(ctx context.Context, agentPublicID, ownerPublicID string, players int) (string, error) {
	if s.pusher == nil {
		return "", httpx.NewError(501, "pushplay_unavailable", "Push-play is not configured on this server.")
	}
	target, found, err := s.pusher.remote.PlayTarget(ctx, agentPublicID)
	if err != nil {
		return "", err
	}
	connected := s.pusher.gw != nil && s.pusher.gw.Connected(agentPublicID)
	if !connected && (!found || target.EndpointURL == "") {
		return "", httpx.NewError(400, "no_agent_transport",
			"Connect your agent (pyyol run) or register and verify a hosted endpoint before running push-play.")
	}
	id, err := s.CreateTable(ctx, agentPublicID, ownerPublicID, 0, players)
	if err != nil {
		return "", err
	}
	go s.pusher.drive(s, id, agentPublicID, target)
	return id, nil
}

// drive polls for seat 0's turn and asks the endpoint for each action until the
// match ends. An error/timeout/illegal move falls back to a safe legal action so
// a bad endpoint can never wedge the match.
func (p *pushPlayer) drive(s *Service, matchID, agentID string, target agentclient.Target) {
	ctx, cancel := context.WithTimeout(context.Background(), p.maxMatch)
	defer cancel()

	// Per-match benchmark: record every driven decision's outcome + latency for
	// the developer's seat, emitted (durably when wired) at match end.
	rec := benchmark.NewRecorder("monopoly", matchID)
	var agentMeta benchmark.AgentMeta
	if p.meta != nil {
		agentMeta = p.meta(ctx, agentID)
	}
	defer func() {
		if err := benchmark.Flush(rec, p.persist, p.em, "practice"); err != nil {
			p.log.Warn("monopoly pushplay: benchmark persist failed", "match", matchID, "err", err)
		}
	}()

	fallbacks := 0
	initialized := false
	deliveredTurn := 0 // highest State.TurnCount already pushed to /event
	for {
		if ctx.Err() != nil {
			p.log.Warn("monopoly pushplay: deadline exceeded", "match", matchID)
			return
		}
		v, err := s.State(ctx, matchID, agentID, false, 0)
		if err != nil {
			p.log.Warn("monopoly pushplay: state read failed", "match", matchID, "err", err)
			return
		}
		// Pick the transport fresh each pass (socket if connected, else endpoint).
		tr := p.transport(agentID, target)
		// Lifecycle: initialize once, lazily on the first state read (best-effort —
		// the agent may also initialise on the first turn). Player count comes from
		// the live board so seat/roster match what the engine actually created.
		if !initialized {
			initialized = true
			players := 0
			if v.State != nil {
				players = len(v.State.Players)
			}
			if err := tr.Initialize(ctx, agentclient.InitializeRequest{
				MatchID: matchID, Game: "monopoly", Seat: v.YourSeat, Players: players,
			}); err != nil {
				p.log.Warn("monopoly pushplay: initialize failed (continuing)", "match", matchID, "err", err)
			}
		}
		// Async events: the FULL itemized engine log since the last delivery (every
		// roll/rent/purchase/card/trade), each with its gap-free Seq — so an agent
		// sees exactly what happened between its turns, not just a state snapshot.
		deliveredTurn = p.dispatchEngineEvents(ctx, tr, s, matchID, deliveredTurn)
		if v.Status != StatusActive {
			// Record the developer seat's meta + outcome for win-rate/version-diff/provider.
			rec.SetAgentMeta(v.YourSeat, agentID, agentMeta)
			if v.Result != nil {
				rec.SetResult(v.YourSeat, agentID, monopolyResult(v.Result.WinnerSeat, v.YourSeat))
			}
			p.log.Info("monopoly pushplay: match finished", "match", matchID, "status", v.Status, "fallbacks", fallbacks, "socket", tr.Socket())
			// Lifecycle: game-end with a FAT, replayable payload — result + final board
			// + the full itemized event log.
			log, _ := s.repo.LoadEvents(context.WithoutCancel(ctx), matchID, 0)
			result, _ := json.Marshal(monopolyGameEnd{
				Result: v.Result, MatchID: matchID, Game: "monopoly",
				Seat: v.YourSeat, FinalState: v.State, Log: log,
			})
			if err := tr.GameEnd(context.WithoutCancel(ctx), matchID, result); err != nil {
				p.log.Warn("monopoly pushplay: game-end delivery failed", "match", matchID, "err", err)
			}
			return
		}
		if !v.YourTurn || len(v.Legal) == 0 {
			time.Sleep(150 * time.Millisecond)
			continue
		}

		act, outcome, latencyMS, rationale, usage := p.decide(ctx, tr, matchID, v)
		round := 0
		if v.State != nil {
			round = v.State.TurnCount
		}
		rec.Record(benchmark.Decision{
			Seat: v.YourSeat, AgentID: agentID, Outcome: outcome, LatencyMS: latencyMS,
			Round: round, Action: act.Kind, Rationale: rationale, Usage: usage,
		})
		if outcome.Fallback() {
			fallbacks++
		}
		if _, err := s.Act(ctx, agentID, matchID, act); err != nil {
			// The chosen action was rejected; try a guaranteed-safe fallback once.
			if _, err2 := s.Act(ctx, agentID, matchID, safeFallback(v.Legal)); err2 != nil {
				p.log.Warn("monopoly pushplay: submit failed", "match", matchID, "err", err2)
				return
			}
			fallbacks++
		}
	}
}

// dispatchEngineEvents delivers the full itemized engine log emitted since the
// last delivery — one event per roll/rent/purchase/card/trade/etc., each with its
// gap-free Seq for ordering. Best-effort so the loop and engine never block.
// Returns the new highest delivered Seq.
func (p *pushPlayer) dispatchEngineEvents(ctx context.Context, tr agentwire.Transport, s *Service, matchID string, delivered int) int {
	evs, err := s.repo.LoadEvents(ctx, matchID, delivered)
	if err != nil {
		p.log.Warn("monopoly pushplay: load events failed", "match", matchID, "err", err)
		return delivered
	}
	highest := delivered
	for _, e := range evs {
		payload, _ := json.Marshal(e.Payload)
		if err := tr.Event(context.WithoutCancel(ctx), matchID, e.Seq, string(e.Type), payload); err != nil {
			p.log.Warn("monopoly pushplay: event delivery failed", "match", matchID, "seq", e.Seq, "err", err)
		}
		if e.Seq > highest {
			highest = e.Seq
		}
	}
	return highest
}

// monopolyResult maps the winning seat to the developer seat's outcome. A
// negative winner seat (no winner) is a draw.
func monopolyResult(winnerSeat, yourSeat int) benchmark.Result {
	switch {
	case winnerSeat < 0:
		return benchmark.ResultDraw
	case winnerSeat == yourSeat:
		return benchmark.ResultWin
	default:
		return benchmark.ResultLoss
	}
}

func (p *pushPlayer) decide(ctx context.Context, tr agentwire.Transport, matchID string, v AgentView) (mono.Action, benchmark.Outcome, int64, string, *benchmark.TokenUsage) {
	req := MonopolyPushView{
		Game: "monopoly", MatchID: matchID, Seat: v.YourSeat,
		Phase: v.Phase, LegalActions: v.Legal, State: v.State,
	}
	var move MonopolyPushMove
	start := time.Now()
	err := tr.Turn(ctx, req, &move)
	latencyMS := time.Since(start).Milliseconds()
	switch {
	case err != nil:
		return safeFallback(v.Legal), benchmark.ClassifyError(err, false), latencyMS, move.Rationale, move.Usage
	case !containsStr(v.Legal, move.Action):
		return safeFallback(v.Legal), benchmark.OutcomeIllegal, latencyMS, move.Rationale, move.Usage
	default:
		return mono.Action{Kind: move.Action, Property: move.Property, Amount: move.Amount, Trade: move.Trade}, benchmark.OutcomeOK, latencyMS, move.Rationale, move.Usage
	}
}

// safeFallback returns a legal action that needs no extra parameters where
// possible (roll/end_turn/decline/pass/…), else the first legal kind. Mirrors the
// engine's own missed-window default so the match always advances.
func safeFallback(legal []string) mono.Action {
	targetless := map[string]bool{
		"roll": true, "roll_jail": true, "pay_jail": true, "use_jail_card": true,
		"end_turn": true, "decline": true, "pass": true, "bankrupt": true,
		"accept_trade": true, "reject_trade": true, "skip_trade": true,
	}
	// Prefer the least-committal safe actions first. skip_trade is the safe no-op
	// in the open trade window (propose_trade needs a payload and must never be a
	// blind fallback).
	for _, pref := range []string{"end_turn", "decline", "pass", "skip_trade", "roll", "roll_jail", "reject_trade"} {
		if containsStr(legal, pref) {
			return mono.Action{Kind: pref}
		}
	}
	for _, k := range legal {
		if targetless[k] {
			return mono.Action{Kind: k}
		}
	}
	if len(legal) > 0 {
		return mono.Action{Kind: legal[0]}
	}
	return mono.Action{Kind: "end_turn"}
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
