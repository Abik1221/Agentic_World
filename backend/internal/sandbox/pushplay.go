package sandbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/agentwire"
	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/remoteplay"
	"github.com/agent-arena/arena/internal/webhook"
)

// Driver is the slice of match.Service the push-play loop needs beyond opening a
// match: read the current per-seat state and submit a card for that seat. It is
// an interface so the push loop is unit-testable with a fake match.
type Driver interface {
	State(ctx context.Context, matchPublicID, viewerAgentPublicID string, wait bool, timeout time.Duration) (match.AgentView, error)
	Act(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string) (match.AgentView, error)
	// Timeout applies the engine's deterministic timeout for a seat that did not answer,
	// so the miss is recorded rather than disguised as a move the agent chose.
	DriveTimeout(ctx context.Context, agentPublicID, matchPublicID string, round int) (match.AgentView, error)
}

// RemoteResolver returns the push-play Target (endpoint URL + bearer token) for an
// agent's active, endpoint-verified manifest. Implemented by manifest.Service.
type RemoteResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// pushPlayer drives the developer's SEAT of a sandbox match by calling their
// hosted agent endpoint (the manifest "push" model) for each decision, while the
// house bot plays the other seat inline through the standard match machinery. It
// runs entirely server-side; the browser watches the result live over the normal
// spectator SSE stream. Nil until EnablePushPlay is called.
type pushPlayer struct {
	driver Driver
	remote RemoteResolver
	client *agentclient.Client
	// enqueue is the durable webhook queue for async /event + /game-end. When set
	// (production), notifications are persisted and delivered by the central
	// dispatcher (retry + health-gated). When nil, they fall back to best-effort
	// inline goroutines so tests/standalone use still work.
	enqueue webhook.Enqueuer
	// gw is the local-runtime WebSocket gateway. When the developer's agent is
	// connected over the socket, matches are driven over it; otherwise the loop
	// falls back to the hosted HTTP endpoint. Nil disables the socket path.
	gw *agentgw.Gateway
	em *telemetry.Client
	// persist routes the per-match benchmark summary durably through the outbox.
	persist benchmark.Persist
	meta    benchmark.AgentMetaResolver
	log     *slog.Logger
	// maxMatch caps a single driven match's wall-clock so a stalled/hostile
	// endpoint can never leak a goroutine forever.
	maxMatch time.Duration
}

// SetBenchmark wires per-match benchmark telemetry (decision-quality summaries).
// meta (optional) resolves the agent's manifest metadata (version + model).
// Call after EnablePushPlay; a no-op if push-play isn't enabled.
func (s *Service) SetBenchmark(em *telemetry.Client, persist benchmark.Persist, meta benchmark.AgentMetaResolver) {
	if s.pusher != nil {
		s.pusher.em = em
		s.pusher.persist = persist
		s.pusher.meta = meta
	}
}

// SetWebhookEnqueuer routes async /event + /game-end through the durable webhook
// queue. Call after EnablePushPlay; a no-op if push-play isn't enabled.
func (s *Service) SetWebhookEnqueuer(e webhook.Enqueuer) {
	if s.pusher != nil {
		s.pusher.enqueue = e
	}
}

// SetGateway wires the local-runtime WebSocket gateway so a connected agent plays
// over its socket. Call after EnablePushPlay; a no-op if push-play isn't enabled.
func (s *Service) SetGateway(gw *agentgw.Gateway) {
	if s.pusher != nil {
		s.pusher.gw = gw
	}
}

// transport picks the delivery path for this agent+match: the live socket if the
// agent is connected, else the hosted HTTP endpoint (durable webhook queue for
// notifications). Re-evaluated per use so a mid-match connect/disconnect is
// handled — a dropped socket simply reverts to the endpoint (or fallback moves).
func (p *pushPlayer) transport(agentID string, target agentclient.Target) agentwire.Transport {
	if p.gw != nil && p.gw.Connected(agentID) {
		return agentwire.SocketTransport{GW: p.gw, AgentID: agentID, Game: "goofspiel"}
	}
	return agentwire.HTTPTransport{
		Client: p.client, Target: target, Enqueue: p.enqueue,
		AgentID: agentID, Game: "goofspiel", Log: p.log,
	}
}

// EnablePushPlay wires the optional /v1/sandbox/pushplay capability. Without it,
// StartPushPlay returns 501 and the feature is simply absent.
func (s *Service) EnablePushPlay(driver Driver, remote RemoteResolver, client *agentclient.Client, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.pusher = &pushPlayer{driver: driver, remote: remote, client: client, log: log, maxMatch: 3 * time.Minute}
}

// StartPushPlay opens a no-stakes sandbox match and drives the developer's seat
// from their hosted agent endpoint. The developer must have an active,
// endpoint-verified manifest (that is where the URL + bearer token come from);
// otherwise it returns a 400 so the UI can point them at registration. The match
// then plays itself to completion in the background and is watchable at
// /v1/match/{id}/watch, exactly like any other match.
func (s *Service) StartPushPlay(ctx context.Context, humanAgent, humanOwner, difficulty string) (StartResult, error) {
	if !s.enabled {
		return StartResult{}, httpx.NewError(403, "sandbox_disabled", "Sandbox practice mode is disabled in this environment.")
	}
	if s.pusher == nil {
		return StartResult{}, httpx.NewError(501, "pushplay_unavailable", "Push-play is not configured on this server.")
	}
	target, found, err := s.pusher.remote.PlayTarget(ctx, humanAgent)
	if err != nil {
		return StartResult{}, err
	}
	// A local-runtime agent connected over the socket needs no hosted endpoint;
	// a legacy push agent needs a verified endpoint URL. Require at least one.
	connected := s.pusher.gw != nil && s.pusher.gw.Connected(humanAgent)
	if !connected && (!found || target.EndpointURL == "") {
		return StartResult{}, httpx.NewError(400, "no_agent_transport",
			"Connect your agent (pyyol run) or register and verify a hosted endpoint before running push-play.")
	}

	opp := opponentFor(difficulty)
	id, err := s.matches.CreateSandbox(ctx, humanAgent, humanOwner, opp.ID, HouseOwner, opp.Style)
	if err != nil {
		return StartResult{}, err
	}

	// Drive the developer's seat in the background: the request returns immediately
	// with a match id the client watches live. A fresh context (not the request's)
	// keeps the loop alive after the HTTP response, bounded by maxMatch.
	go s.pusher.drive(id, humanAgent, target)

	res := StartResult{MatchID: id, Mode: match.ModeSandbox, Opponent: opp}
	res.Driver = "remote"
	return res, nil
}

// drive polls the match for the developer's turn and, when it is their move, asks
// their endpoint for a card and submits it. A remote error/timeout/illegal move
// falls back to the lowest legal card so a bad endpoint can never wedge the match
// — the same guarantee remoteplay.PlayGoofspiel gives.
func (p *pushPlayer) drive(matchID, agentID string, target agentclient.Target) {
	ctx, cancel := context.WithTimeout(context.Background(), p.maxMatch)
	defer cancel()

	// Lifecycle: initialize once at match start (best-effort — the agent may also
	// initialise lazily on the first turn). Seat A is always the developer.
	if err := p.transport(agentID, target).Initialize(ctx, agentclient.InitializeRequest{
		MatchID: matchID, Game: "goofspiel", Seat: 0, Players: 2,
	}); err != nil {
		p.log.Warn("pushplay: initialize failed (continuing)", "match", matchID, "err", err)
	}

	// Per-match benchmark: record every driven decision's outcome + latency for
	// the developer's seat (seat 0), emitted (durably when wired) at match end.
	rec := benchmark.NewRecorder("goofspiel", matchID)
	if p.meta != nil {
		rec.SetAgentMeta(0, agentID, p.meta(ctx, agentID))
	}
	defer func() {
		if err := benchmark.Flush(rec, p.persist, p.em, "sandbox"); err != nil {
			p.log.Warn("pushplay: benchmark persist failed", "match", matchID, "err", err)
		}
	}()

	fallbacks := 0
	deliveredRounds := 0 // highest round already pushed to /event (async, ordered by seq)
	for {
		if ctx.Err() != nil {
			p.log.Warn("pushplay: driver deadline exceeded, stopping", "match", matchID)
			return
		}
		// Immediate read (wait=false): in a sandbox the house plays inline inside
		// the developer's Act, so it is almost always the developer's turn while the
		// match is active. A long-poll would stall here waiting for a change that
		// only this loop can cause.
		v, err := p.driver.State(ctx, matchID, agentID, false, 0)
		if err != nil {
			p.log.Warn("pushplay: state read failed, stopping driver", "match", matchID, "err", err)
			return
		}
		// Pick the transport fresh each pass so a mid-match connect/disconnect is
		// handled. Async event notifications for rounds resolved since the last read
		// (opponent card + winner) — fire-and-forget so the loop/engine never block;
		// the agent orders by Seq.
		tr := p.transport(agentID, target)
		deliveredRounds = p.dispatchRoundEvents(ctx, tr, matchID, v, deliveredRounds)
		if v.Status != "active" {
			// Record the developer seat's outcome (seat 0) for win-rate.
			if v.Result != nil {
				rec.SetResult(0, agentID, benchmark.ResultFromLabel(v.Result.Winner))
			}
			p.log.Info("pushplay: match finished", "match", matchID, "status", v.Status, "fallbacks", fallbacks, "socket", tr.Socket())
			// Lifecycle: game-end with a FAT, replayable result — the outcome plus
			// the full round-by-round history, so the agent has the complete match
			// record without stitching event notifications together.
			result, _ := json.Marshal(gameEndResult{
				Result:  v.Result,
				MatchID: matchID,
				Game:    "goofspiel",
				Seat:    0,
				History: historyFromView(v),
			})
			if err := tr.GameEnd(context.WithoutCancel(ctx), matchID, result); err != nil {
				p.log.Warn("pushplay: game-end delivery failed", "match", matchID, "err", err)
			}
			return
		}
		if !v.YourTurn {
			// House is acting (rare — it plays inline in commit); yield briefly.
			time.Sleep(150 * time.Millisecond)
			continue
		}

		legal := v.LegalActions.PlayCardFrom
		if len(legal) == 0 {
			legal = v.You.Hand
		}
		if len(legal) == 0 {
			p.log.Warn("pushplay: no legal action available, stopping", "match", matchID)
			return
		}

		view := p.turnView(matchID, v, legal)
		card, outcome, latencyMS, rationale, usage := p.decide(ctx, tr, target, view)
		rec.Record(benchmark.Decision{
			Seat: 0, AgentID: agentID, Outcome: outcome, LatencyMS: latencyMS,
			Round: v.Round, Action: strconv.Itoa(card), Rationale: rationale, Usage: usage,
			// The INPUT half of the record: the exact view that was POSTed to the agent,
			// not a reconstruction of it. Serialized and size-capped by the Recorder.
			View: view,
		})
		if outcome.Fallback() {
			fallbacks++
		}
		// Same rule as the ranked drive: an unanswered turn is the engine's timeout, not a
		// move made on the agent's behalf. Otherwise a seat can be dark for a whole match
		// and still finish with an absence tally of zero.
		var serr error
		if outcome.Fallback() {
			_, serr = p.driver.DriveTimeout(ctx, agentID, matchID, v.Round)
		} else {
			_, serr = p.driver.Act(ctx, agentID, matchID, v.Round, card, "")
		}
		if serr != nil {
			p.log.Warn("pushplay: submit failed, stopping driver", "match", matchID, "round", v.Round, "err", serr)
			return
		}
	}
}

// dispatchRoundEvents pushes an event for each round that resolved since the last
// read, over the chosen transport. Delivery is async/best-effort so the turn loop
// and the engine never block; each event carries Seq for ordering. Returns the new
// highest delivered round.
func (p *pushPlayer) dispatchRoundEvents(ctx context.Context, tr agentwire.Transport, matchID string, v match.AgentView, delivered int) int {
	highest := delivered
	for _, h := range v.History {
		if h.Round <= delivered {
			continue
		}
		payload, _ := json.Marshal(h)
		if err := tr.Event(context.WithoutCancel(ctx), matchID, h.Round, "round_revealed", payload); err != nil {
			p.log.Warn("pushplay: event delivery failed", "match", matchID, "round", h.Round, "err", err)
		}
		if h.Round > highest {
			highest = h.Round
		}
	}
	return highest
}

// turnView builds the exact payload this seat is handed for the round.
//
// Lifted out of decide so the caller can BOTH push it and record it as the input half
// of the decision trace. Previously the view existed only inside decide, so
// agent_match_decisions.input_json was left NULL on every sandbox match — the decision
// inspector could show what the agent played but never the state it was looking at,
// which is the half that makes a move judgeable. The three sibling push paths (ranked
// drive, Mafia, Monopoly) all recorded it; this one silently did not.
func (p *pushPlayer) turnView(matchID string, v match.AgentView, legal []int) remoteplay.GoofspielView {
	return remoteplay.GoofspielView{
		Game:         "goofspiel",
		MatchID:      matchID,
		Seat:         0, // developer is always seat A in a sandbox match
		Round:        v.Round,
		CurrentPrize: v.CurrentPrize,
		PrizePool:    v.PrizePool,
		YourHand:     v.You.Hand,
		Scores:       [2]int{v.You.Score, v.Opponent.Score},
		LegalActions: legal,
		History:      historyFromView(v), // self-contained: every resolved round so far
		// The shot clock, so the agent can size its own thinking. Absence is the agent's
		// own risk under the forfeit rule, which is only fair if it was told the budget.
		MoveWindowMs: v.MoveWindowMs,
		DeadlineMs:   v.DeadlineMs,
		Chat:         chatFromView(v),
	}
}

// decide asks the agent for a card over the transport and validates it against
// the legal set, falling back to the lowest legal card on any error or illegal
// response — so an absent/slow agent can never wedge the match.
func (p *pushPlayer) decide(ctx context.Context, tr agentwire.Transport, target agentclient.Target, view remoteplay.GoofspielView) (int, benchmark.Outcome, int64, string, *benchmark.TokenUsage) {
	legal := view.LegalActions
	var move remoteplay.GoofspielMove
	// Same as the ranked drive: the turn carries the seat's remaining clock, so the play
	// client's configured Timeout applies only when nobody supplied one.
	if view.DeadlineMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(view.DeadlineMs)*time.Millisecond)
		defer cancel()
	}
	start := time.Now()
	err := tr.Turn(ctx, view, &move)
	latencyMS := time.Since(start).Milliseconds()
	switch {
	case err != nil:
		// The reachability probe now lives in agentwire.HTTPTransport.Turn, so EVERY
		// hosted-endpoint path gets it — including the staked ones this driver is not.
		return lowestInt(legal), benchmark.ClassifyError(err, false), latencyMS, move.Rationale, move.Usage
	case !containsInt(legal, move.Card):
		return lowestInt(legal), benchmark.OutcomeIllegal, latencyMS, move.Rationale, move.Usage
	default:
		return move.Card, benchmark.OutcomeOK, latencyMS, move.Rationale, move.Usage
	}
}

// historyFromView maps the match service's per-round history into the seat's
// replayable RoundView list (developer is seat 0). Running scores are computed
// cumulatively so the payload is a complete, self-consistent record.
func historyFromView(v match.AgentView) []remoteplay.RoundView {
	out := make([]remoteplay.RoundView, 0, len(v.History))
	cum := [2]int{}
	for _, h := range v.History {
		w := winnerSeat(h.Winner)
		if w == 0 {
			cum[0] += h.PrizePool
		} else if w == 1 {
			cum[1] += h.PrizePool
		}
		out = append(out, remoteplay.RoundView{
			Round:     h.Round,
			Prize:     h.Prize,
			PrizePool: h.PrizePool,
			YourCard:  h.YourCard,
			OppCard:   h.OppCard,
			Winner:    w,
			Scores:    cum,
		})
	}
	return out
}

// gameEndResult is the fat /game-end payload: the settled result plus the full
// replayable round history. The embedded Result keeps the historical shape
// (winner + coins) so existing consumers keep working; History is additive.
type gameEndResult struct {
	Result  any                    `json:"result"`
	MatchID string                 `json:"match_id"`
	Game    string                 `json:"game"`
	Seat    int                    `json:"seat"`
	History []remoteplay.RoundView `json:"history"`
}

// winnerSeat maps the match view's "you"/"opponent"/"tie" to a seat index from
// the developer's perspective (seat 0), matching remoteplay.RoundView semantics.
func winnerSeat(winner string) int {
	switch winner {
	case "you":
		return 0
	case "opponent":
		return 1
	default:
		return -1 // tie
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
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}

// chatFromView carries the table talk into the pushed payload.
//
// The ranked drive already did this; the sandbox path did not, so an agent practising
// over its hosted endpoint could speak and never be spoken to. Same data, same shape,
// so an agent written against sandbox behaves identically in ranked play.
func chatFromView(v match.AgentView) []remoteplay.ChatLine {
	if len(v.Chat) == 0 {
		return nil
	}
	out := make([]remoteplay.ChatLine, 0, len(v.Chat))
	for _, c := range v.Chat {
		out = append(out, remoteplay.ChatLine{
			Round: c.Round, Seat: c.Seat, Text: c.Text, Kind: c.Kind, You: c.You,
		})
	}
	return out
}
