package mafia

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/agentwire"
	"github.com/agent-arena/arena/internal/benchmark"
	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/webhook"
)

// Mafia push-play: a no-stakes 12-seat table where the developer's seat is driven
// by their hosted agent endpoint (manifest push model) and the other 11 seats are
// filled and driven by deterministic rule-based bots — so a single developer can
// exercise a full Mafia match solo, watched live over the normal SSE/state
// endpoints. This is also what makes a solo Mafia practice table actually run
// (Mafia needs a full roster, unlike Goofspiel/Monopoly).

// BotAgent is one seedable opponent identity (a demo agent) used to fill seats.
type BotAgent struct {
	PublicID      string
	OwnerPublicID string
}

// RemoteResolver returns the push-play Target for an agent's verified manifest.
type RemoteResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// PushClient POSTs a game view to the developer's endpoint and decodes the move.
type PushClient interface {
	Play(ctx context.Context, t agentclient.Target, request, out any) (int, error)
	Initialize(ctx context.Context, t agentclient.Target, req agentclient.InitializeRequest) (agentclient.InitializeResponse, error)
	Event(ctx context.Context, t agentclient.Target, n agentclient.EventNotification) error
	GameEnd(ctx context.Context, t agentclient.Target, n agentclient.GameEndNotification) error
}

type pushPlayer struct {
	remote   RemoteResolver
	client   PushClient
	bots     []BotAgent
	enqueue  webhook.Enqueuer // durable async /event + /game-end; nil => inline fallback
	gw       *agentgw.Gateway // local-runtime socket; nil disables the socket path
	em       *telemetry.Client
	persist  benchmark.Persist
	meta     benchmark.AgentMetaResolver
	log      *slog.Logger
	maxMatch time.Duration
}

// EnablePushPlay wires POST /v1/mafia/pushplay. bots must have at least
// RosterSize-1 entries to fill a table; fewer disables push-play.
func (s *Service) EnablePushPlay(remote RemoteResolver, client PushClient, bots []BotAgent, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.pusher = &pushPlayer{remote: remote, client: client, bots: bots, log: log, maxMatch: 5 * time.Minute}
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

// SetBenchmark wires per-match benchmark telemetry (decision-quality summaries).
// em ships to Pyyol Lens; persist (optional) routes durably via the outbox. Call
// after EnablePushPlay; a no-op if push-play isn't enabled.
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
		return agentwire.SocketTransport{GW: p.gw, AgentID: agentID, Game: "mafia"}
	}
	return agentwire.HTTPTransport{
		Client: p.client, Target: target, Enqueue: p.enqueue,
		AgentID: agentID, Game: "mafia", Log: p.log,
	}
}

// MafiaPushView is the redacted per-seat JSON POSTed to the agent endpoint. It
// carries only what this seat legitimately knows.
type MafiaPushView struct {
	Game     string       `json:"game"` // "mafia"
	MatchID  string       `json:"match_id"`
	YourSeat int          `json:"your_seat"`
	YourRole string       `json:"your_role,omitempty"`
	Day      int          `json:"day"`
	Phase    string       `json:"phase"`
	Alive    map[int]bool `json:"alive"`
	Allies   []int        `json:"allies,omitempty"`
	Legal    []string     `json:"legal"`
	Public   []mf.Event   `json:"public,omitempty"`
	Private  []mf.Event   `json:"private,omitempty"`
	// Live voting state (voting phase only) + the shot clock, so the agent can
	// reason about the current tally and pace its thinking.
	Votes      map[int]int `json:"votes,omitempty"`       // voter seat -> target seat
	VoteTally  map[int]int `json:"vote_tally,omitempty"`  // target seat -> vote count
	DeadlineMs int64       `json:"deadline_ms,omitempty"` // ms left on the shot clock
}

// MafiaPushMove is the action the agent returns. Rationale is optional private
// reasoning (NOT the public in-game Text) captured only for observability.
type MafiaPushMove struct {
	Action    string                `json:"action"`
	Target    int                   `json:"target"`
	Tone      string                `json:"tone"`
	Text      string                `json:"text"`
	Rationale string                `json:"rationale,omitempty"`
	Usage     *benchmark.TokenUsage `json:"usage,omitempty"`
}

// StartPushPlay opens a no-stakes table (creator seat 1), fills the rest with
// bots to start the match, and drives seat 1 from the developer's endpoint.
func (s *Service) StartPushPlay(ctx context.Context, userAgent, userOwner string) (string, error) {
	if s.pusher == nil || len(s.pusher.bots) < s.cfg.RosterSize-1 {
		return "", httpx.NewError(501, "pushplay_unavailable", "Mafia push-play is not configured on this server.")
	}
	target, found, err := s.pusher.remote.PlayTarget(ctx, userAgent)
	if err != nil {
		return "", err
	}
	connected := s.pusher.gw != nil && s.pusher.gw.Connected(userAgent)
	if !connected && (!found || target.EndpointURL == "") {
		return "", httpx.NewError(400, "no_agent_transport",
			"Connect your agent (pyyol run) or register and verify a hosted endpoint before running push-play.")
	}

	matchID, err := s.CreateTable(ctx, userAgent, userOwner, 0)
	if err != nil {
		s.pusher.log.Error("mafia pushplay: create table failed", "err", err)
		return "", err
	}
	// Fill the remaining seats with bots; the final join starts the match.
	seatIDs := []string{userAgent}
	for i := 0; i < s.cfg.RosterSize-1; i++ {
		b := s.pusher.bots[i]
		if _, err := s.Join(ctx, b.PublicID, b.OwnerPublicID, matchID); err != nil {
			s.pusher.log.Error("mafia pushplay: bot join failed", "seat", i+2, "bot", b.PublicID, "err", err)
			return "", err
		}
		seatIDs = append(seatIDs, b.PublicID)
	}

	go s.pusher.drive(s, matchID, userAgent, target, seatIDs)
	return matchID, nil
}

// drive advances the match: each pass, every alive seat with a pending action
// acts — seat 1 (userAgent) via the endpoint, the rest via a rule-based bot. The
// engine validates each move, so a stale action (phase already advanced) is
// harmlessly rejected and retried next pass.
func (p *pushPlayer) drive(s *Service, matchID, userAgent string, target agentclient.Target, seatIDs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), p.maxMatch)
	defer cancel()

	// Per-match benchmark: record the developer-seat decisions (bots excluded),
	// emitted (durably when wired) at match end.
	rec := benchmark.NewRecorder("mafia", matchID)
	var agentMeta benchmark.AgentMeta
	if p.meta != nil {
		agentMeta = p.meta(ctx, userAgent)
	}
	defer func() {
		if err := benchmark.Flush(rec, p.persist, p.em, "practice"); err != nil {
			p.log.Warn("mafia pushplay: benchmark persist failed", "match", matchID, "err", err)
		}
	}()

	initialized := false
	deliveredSeq := 0 // highest public-event Seq already pushed to /event
	// Per-seat role-aware house bots (mafia avoid allies; detective votes/investigates
	// on its proven private results). Cached per seat so each keeps its deterministic
	// rng across the match. Pure engine — no AI/network.
	houseBots := map[int]*mf.Bot{}
	for {
		if ctx.Err() != nil {
			p.log.Warn("mafia pushplay: deadline exceeded", "match", matchID)
			return
		}
		base, err := s.State(ctx, matchID, userAgent, false, 0)
		if err != nil {
			p.log.Warn("mafia pushplay: state read failed", "match", matchID, "err", err)
			return
		}
		// Pick the transport fresh each pass (socket if connected, else endpoint).
		tr := p.transport(userAgent, target)
		// Lifecycle: initialize once, lazily on the first state read (best-effort).
		// Role is included so the agent knows its allegiance up front.
		if !initialized {
			initialized = true
			if err := tr.Initialize(ctx, agentclient.InitializeRequest{
				MatchID: matchID, Game: "mafia", Seat: base.YourSeat, Role: base.YourRole, Players: s.cfg.RosterSize,
			}); err != nil {
				p.log.Warn("mafia pushplay: initialize failed (continuing)", "match", matchID, "err", err)
			}
		}
		// Async event for each public transcript entry that appeared since the last
		// read. Public events carry a monotonic Seq, used directly for ordering.
		deliveredSeq = p.dispatchPublicEvents(ctx, tr, matchID, base.Public, deliveredSeq)
		if base.Status != StatusActive {
			// Record the developer seat's meta + outcome (role-agnostic: the reward
			// row flags whether this seat was on the winning team).
			rec.SetAgentMeta(base.YourSeat, userAgent, agentMeta)
			rec.SetResult(base.YourSeat, userAgent, mafiaResult(base.Result, base.YourSeat))
			p.log.Info("mafia pushplay: match finished", "match", matchID, "status", base.Status, "socket", tr.Socket())
			// Lifecycle: game-end with a FAT, replayable payload — the settled result
			// PLUS the full public transcript (chat + votes + eliminations), so the
			// agent has the complete match record in one payload.
			result, _ := json.Marshal(mafiaGameEnd{
				Result: base.Result, MatchID: matchID, Game: "mafia",
				Seat: base.YourSeat, Transcript: base.Public,
			})
			if err := tr.GameEnd(context.WithoutCancel(ctx), matchID, result); err != nil {
				p.log.Warn("mafia pushplay: game-end delivery failed", "match", matchID, "err", err)
			}
			return
		}

		acted := false
		for _, id := range seatIDs {
			v, err := s.State(ctx, matchID, id, false, 0)
			if err != nil || v.Status != StatusActive || len(v.Legal) == 0 {
				continue
			}
			var act mf.Action
			if id == userAgent {
				var outcome benchmark.Outcome
				var latencyMS int64
				var rationale string
				var usage *benchmark.TokenUsage
				act, outcome, latencyMS, rationale, usage = p.decideRemote(ctx, tr, matchID, v)
				rec.Record(benchmark.Decision{
					Seat: v.YourSeat, AgentID: id, Outcome: outcome, LatencyMS: latencyMS,
					Round: v.Day, Action: act.Kind, Rationale: rationale, Usage: usage,
				})
			} else {
				// Strong, role-aware engine bot. Falls back to the simple legal pick
				// if it ever returns a kind not currently legal (never stalls a seat).
				bot := houseBots[v.YourSeat]
				if bot == nil {
					bot = mf.NewBot("house", []byte(matchID+":"+id), v.YourSeat)
					houseBots[v.YourSeat] = bot
				}
				act = bot.Decide(toEngineView(v))
				if act.Kind == "" || !containsStr(v.Legal, act.Kind) {
					act = botDecide(v)
				}
			}
			if _, err := s.Act(ctx, id, matchID, act, 0, "", "", true); err == nil { // platform-driven: no stale-phase guard, no per-move signature
				acted = true
			}
		}
		if !acted {
			// No seat could act (e.g. brief lock contention) — yield, don't spin.
			time.Sleep(150 * time.Millisecond)
		}
	}
}

// mafiaGameEnd is the fat /game-end payload: the settled result plus the full
// public transcript, so the agent has the whole match record (chat, votes,
// eliminations) in one payload. Result keeps its historical shape (additive).
type mafiaGameEnd struct {
	Result     any        `json:"result"`
	MatchID    string     `json:"match_id"`
	Game       string     `json:"game"`
	Seat       int        `json:"seat"`
	Transcript []mf.Event `json:"transcript"`
}

// dispatchPublicEvents pushes an event over the transport for each public
// transcript entry whose Seq exceeds the last delivered one. Fire-and-forget so
// the drive loop and engine never block; the agent orders by Seq. Returns the new
// highest delivered Seq.
func (p *pushPlayer) dispatchPublicEvents(ctx context.Context, tr agentwire.Transport, matchID string, events []mf.Event, delivered int) int {
	highest := delivered
	for _, e := range events {
		if e.Seq <= delivered {
			continue
		}
		payload, _ := json.Marshal(e.Payload)
		if err := tr.Event(context.WithoutCancel(ctx), matchID, e.Seq, string(e.Type), payload); err != nil {
			p.log.Warn("mafia pushplay: event delivery failed", "match", matchID, "seq", e.Seq, "err", err)
		}
		if e.Seq > highest {
			highest = e.Seq
		}
	}
	return highest
}

// mafiaResult maps the settled economy result to the developer seat's outcome
// via its reward row's OnWinningTeam flag (works for any role). Nil/absent → "".
func mafiaResult(r *EconomyResult, seat int) benchmark.Result {
	if r == nil {
		return ""
	}
	for _, row := range r.Rewards {
		if row.Seat == seat {
			if row.OnWinningTeam {
				return benchmark.ResultWin
			}
			return benchmark.ResultLoss
		}
	}
	return ""
}

func (p *pushPlayer) decideRemote(ctx context.Context, tr agentwire.Transport, matchID string, v AgentView) (mf.Action, benchmark.Outcome, int64, string, *benchmark.TokenUsage) {
	req := MafiaPushView{
		Game: "mafia", MatchID: matchID, YourSeat: v.YourSeat, YourRole: v.YourRole,
		Day: v.Day, Phase: v.Phase, Alive: v.Alive, Allies: v.Allies, Legal: v.Legal,
		Public: v.Public, Private: v.Private,
		Votes: v.Votes, VoteTally: v.VoteTally, DeadlineMs: v.DeadlineMs,
	}
	var move MafiaPushMove
	start := time.Now()
	err := tr.Turn(ctx, req, &move)
	latencyMS := time.Since(start).Milliseconds()
	switch {
	case err != nil:
		return botDecide(v), benchmark.ClassifyError(err, false), latencyMS, move.Rationale, move.Usage
	case !containsStr(v.Legal, move.Action):
		return botDecide(v), benchmark.OutcomeIllegal, latencyMS, move.Rationale, move.Usage
	default:
		return mf.Action{Kind: move.Action, Target: move.Target, Tone: move.Tone, Text: move.Text}, benchmark.OutcomeOK, latencyMS, move.Rationale, move.Usage
	}
}

// botDecide is a deterministic rule-based move for the current phase — the same
// baseline the demo runner uses, kept in-package to avoid an import cycle.
// toEngineView adapts the service's redacted AgentView into the engine bot's view.
// Public/Private are already engine mf.Event values, so this is a field mapping;
// Team is unused by the bot (it reasons from Role + Allies + its private results).
func toEngineView(v AgentView) mf.AgentView {
	return mf.AgentView{
		Seat:    v.YourSeat,
		Role:    v.YourRole,
		Day:     v.Day,
		Phase:   v.Phase,
		Alive:   v.Alive,
		Allies:  v.Allies,
		Legal:   v.Legal,
		Public:  v.Public,
		Private: v.Private,
	}
}

func botDecide(v AgentView) mf.Action {
	kind := ""
	if len(v.Legal) > 0 {
		kind = v.Legal[0] // exactly one legal kind per seat per phase
	}
	target := firstOtherAlive(v.Alive, v.YourSeat)
	switch kind {
	case "message":
		return mf.Action{Kind: "message", Tone: "info", Text: "Observing the table.", Target: target}
	case "protect":
		return mf.Action{Kind: "protect", Target: v.YourSeat} // doctor may guard itself
	case "":
		return mf.Action{}
	default: // night_kill | investigate | profile | vote
		return mf.Action{Kind: kind, Target: target}
	}
}

func firstOtherAlive(alive map[int]bool, seat int) int {
	best := 0
	for s, ok := range alive {
		if ok && s != seat && (best == 0 || s < best) {
			best = s
		}
	}
	return best
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
