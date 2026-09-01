package mafia

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/agentwire"
	"github.com/agent-arena/arena/internal/benchmark"
	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/turnproof"
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
	// Health is the liveness probe the transport runs when a turn fails, so a missed
	// turn on a STAKED table can be classified before it counts toward an absence
	// forfeit. Required by agentwire.HTTPClient — declaring it here is what makes the
	// compiler refuse a client this path could not have asked.
	Health(ctx context.Context, t agentclient.Target) (agentclient.HealthResult, error)
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
	// turns mints the per-turn proof that binds a gateway LLM call to ONE decision.
	// Nil ⇒ views ship without a proof and no decision here can be counted as
	// LLM-backed, which is what left paid Mafia tables unprotected by the integrity
	// check even though the check itself was installed.
	turns TurnMinter
}

// TurnMinter issues the token that binds a gateway LLM call to one decision.
// Satisfied by *turnproof.Signer — the same signer Goofspiel uses, so one secret covers
// every game and the gateway verifies them all identically.
type TurnMinter interface {
	Mint(agentID, matchID string, round int) string
}

// EnablePushPlay wires POST /v1/mafia/pushplay. bots must have at least
// RosterSize-1 entries to fill a table; fewer disables push-play.
func (s *Service) EnablePushPlay(remote RemoteResolver, client PushClient, bots []BotAgent, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.pusher = &pushPlayer{remote: remote, client: client, bots: bots, log: log, maxMatch: 5 * time.Minute, turns: s.turns}
	// The same list is the allowlist for JoinHouseSeat, so every path that fills a seat
	// with a bot — push-play here, group-matchmaking backfill — draws from one declared
	// set and no other agent can ever be seated through the gate-bypassing path.
	s.houseAgents = make(map[string]bool, len(bots))
	for _, b := range bots {
		s.houseAgents[b.PublicID] = true
	}
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
	// Digest states in counts what happened before the window. Nil when the whole
	// transcript fits. Never prose — a summary of what a player said is not what they said.
	Digest  *mf.PublicDigest `json:"digest,omitempty"`
	Private []mf.Event       `json:"private,omitempty"`
	// Live voting state (voting phase only) + the shot clock, so the agent can
	// reason about the current tally and pace its thinking.
	Votes      map[int]int `json:"votes,omitempty"`       // voter seat -> target seat
	VoteTally  map[int]int `json:"vote_tally,omitempty"`  // target seat -> vote count
	DeadlineMs int64       `json:"deadline_ms,omitempty"` // ms left on the shot clock
	// StartsAt / ServerNow are the start countdown, carried here for the reason this struct's
	// CannotProtect comment already states: a field added to the service's AgentView and not
	// copied into this hand-built push view does not exist as far as a push-play agent is
	// concerned. A hosted agent watching its own table deserves the same countdown a browser
	// gets, and both count to the same absolute instant.
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	ServerNow time.Time  `json:"server_now"`
	// Round is the decision's turn number, and it exists so BOTH SIDES AGREE on it.
	//
	// The SDK derives what it reports as X-Pyyol-Turn from `round` first and `day` only as
	// a fallback. Several Mafia decisions happen inside one day, so `day` alone cannot
	// identify a decision — and if the proof is minted for a phase-folded turn while the
	// agent reports the bare day, the gateway compares two different numbers and every
	// verification fails silently. Publishing the number explicitly removes the guess.
	Round int `json:"round"`
	// TurnProof binds a gateway LLM call to THIS decision. The SDK attaches it as
	// X-Pyyol-Proof on every model call it routes, which is what makes "this agent
	// really used an LLM for this turn" provable rather than self-reported.
	//
	// Mafia shipped no proof at all, so no seat could ever be shown to be LLM-backed and
	// the integrity check on paid tables could never arm.
	TurnProof string `json:"turn_proof,omitempty"`
	// CannotProtect is the seat this DOCTOR shielded last night and may not shield again
	// tonight (-1 when nothing is barred). AllyKills is what each fellow MAFIA has selected
	// so far tonight.
	//
	// Both are published by the engine's own view and were NOT forwarded here, which made
	// them inert: the engine knew, and no agent was ever told. That is the same defect as the
	// Monopoly wire mismatch — a hand-built push struct silently dropping a field the engine
	// added. Anything added to mf.AgentView has to be carried here too, or it does not exist.
	CannotProtect int         `json:"cannot_protect"`
	AllyKills     map[int]int `json:"ally_kills,omitempty"`
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
	//
	// JoinHouseSeat, not Join: every bot shares the `usr_system` owner, and Join's
	// anti-collusion rule rejects a second seat from an owner who already holds one — so
	// this loop used to fail on the SECOND bot with a 409, making Mafia push-play
	// unavailable wherever the house bots are the fillers (i.e. production).
	seatIDs := []string{userAgent}
	for i := 0; i < s.cfg.RosterSize-1; i++ {
		b := s.pusher.bots[i]
		if _, err := s.JoinHouseSeat(ctx, b.PublicID, b.OwnerPublicID, matchID); err != nil {
			s.pusher.log.Error("mafia pushplay: bot join failed", "seat", i+2, "bot", b.PublicID, "err", err)
			return "", err
		}
		seatIDs = append(seatIDs, b.PublicID)
	}

	// A single-entry map: free practice is one developer and eleven bots. Same driver as a
	// group-matched table, so the two paths cannot drift.
	go s.pusher.drive(s, matchID, map[string]agentclient.Target{userAgent: target}, seatIDs)
	return matchID, nil
}

// drive advances the match: each pass, every alive seat with a pending action
// acts — each REAL seat via its own endpoint/socket, the rest via a rule-based bot. The
// engine validates each move, so a stale action (phase already advanced) is
// harmlessly rejected and retried next pass.
// drive advances the match: each pass, every alive seat with a pending action acts — a REAL
// agent through its own transport, the rest via a rule-based bot.
//
// remotes maps every real agent's public id to its transport target. It used to be a single
// (userAgent, target) pair, which was right when the only way to reach this was free practice:
// one developer, eleven bots. A GROUP-MATCHED table has several real agents, and calling this
// once per agent would have each call bot-playing the others' seats.
//
// That gap is why a ranked Mafia seat never received a turn at all: mafiaTableCreator started
// the table via CreateTable+Join and nothing here was ever spawned. Verified by watching four
// funded, matched agents get zero pushes.
func (p *pushPlayer) drive(s *Service, matchID string, remotes map[string]agentclient.Target, seatIDs []string) {
	if len(remotes) == 0 {
		// Nothing to drive. Returning rather than bot-playing every seat: a table with no real
		// agent is not this loop's job, and silently playing it out would manufacture a
		// finished match nobody participated in.
		return
	}
	// The seat whose view is used for match STATUS and for the benchmark's agent meta. Sorted
	// so two runs of the same match read it the same way.
	realIDs := make([]string, 0, len(remotes))
	for id := range remotes {
		realIDs = append(realIDs, id)
	}
	sort.Strings(realIDs)
	primary := realIDs[0]

	ctx, cancel := context.WithTimeout(context.Background(), p.maxMatch)
	defer cancel()

	// Per-match benchmark: record the developer-seat decisions (bots excluded),
	// emitted (durably when wired) at match end.
	rec := benchmark.NewRecorder("mafia", matchID)
	var agentMeta benchmark.AgentMeta
	if p.meta != nil {
		agentMeta = p.meta(ctx, primary)
	}
	defer func() {
		if err := benchmark.Flush(rec, p.persist, p.em, "practice"); err != nil {
			p.log.Warn("mafia pushplay: benchmark persist failed", "match", matchID, "err", err)
		}
	}()

	// PER AGENT, not per match. Each real seat has its own initialize handshake and its own
	// delivered-seq cursor: sharing one cursor across agents would mark an event delivered for
	// everybody the moment it reached the first of them, so the rest would silently never
	// receive it — the exact guarantee the push path exists to provide.
	initialized := make(map[string]bool, len(remotes))
	deliveredSeq := make(map[string]int, len(remotes))
	// Per-seat role-aware house bots (mafia avoid allies; detective votes/investigates
	// on its proven private results). Cached per seat so each keeps its deterministic
	// rng across the match. Pure engine — no AI/network.
	houseBots := map[int]*mf.Bot{}
	for {
		if ctx.Err() != nil {
			p.log.Warn("mafia pushplay: deadline exceeded", "match", matchID)
			return
		}
		// Status is read through ONE real seat, chosen deterministically so a replay of this
		// loop reads the same way twice. Any seat's view carries the same match status.
		base, err := s.State(ctx, matchID, primary, false, 0)
		if err != nil {
			p.log.Warn("mafia pushplay: state read failed", "match", matchID, "err", err)
			return
		}
		// Transport is chosen PER REAL AGENT inside the loop below, since each has its own
		// socket-or-endpoint answer. Kept here only for the primary seat's lifecycle calls.
		tr := p.transport(primary, remotes[primary])
		// Lifecycle: initialize once, lazily on the first state read (best-effort).
		// Role is included so the agent knows its allegiance up front.
		// Initialize + event delivery for EVERY real agent, each with its own transport and
		// cursor. Doing this only for one seat is what left the other three silent.
		for _, id := range realIDs {
			itr := p.transport(id, remotes[id])
			iv, ierr := s.State(ctx, matchID, id, false, 0)
			if ierr != nil {
				continue
			}
			if !initialized[id] {
				initialized[id] = true
				if err := itr.Initialize(ctx, agentclient.InitializeRequest{
					MatchID: matchID, Game: "mafia", Seat: iv.YourSeat, Role: iv.YourRole,
					Players: s.cfg.RosterSize,
				}); err != nil {
					p.log.Warn("mafia pushplay: initialize failed (continuing)",
						"match", matchID, "agent", id, "err", err)
				}
			}
			deliveredSeq[id] = p.dispatchPublicEvents(ctx, itr, matchID, iv.Public, deliveredSeq[id])
		}
		if base.Status != StatusActive {
			// Record the developer seat's meta + outcome (role-agnostic: the reward
			// row flags whether this seat was on the winning team).
			rec.SetAgentMeta(base.YourSeat, primary, agentMeta)
			rec.SetResult(base.YourSeat, primary, mafiaResult(base.Result, base.YourSeat))
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
			// A REAL seat decides through its own transport; everyone else is bot-played.
			// This used to be `id == userAgent`, which is why only one seat per table was ever
			// asked — the whole reason a ranked Mafia seat sat idle. Per-seat transport, not
			// the primary's: two agents can be on different transports (one socket, one
			// hosted endpoint), and pushing to the wrong one silently reaches nobody.
			if seatTarget, isReal := remotes[id]; isReal {
				str := p.transport(id, seatTarget)
				var outcome benchmark.Outcome
				var latencyMS int64
				var rationale string
				var usage *benchmark.TokenUsage
				act, outcome, latencyMS, rationale, usage = p.decideRemote(ctx, str, matchID, id, v)
				rec.Record(benchmark.Decision{
					Seat: v.YourSeat, AgentID: id, Outcome: outcome, LatencyMS: latencyMS,
					Round: v.Day, Action: describeAction(act), Rationale: rationale, Usage: usage,
					// The INPUT half of the record: the view this seat was handed,
					// including its own role and what it had heard. Owner-scoped on read.
					View: v,
				})
				// Durable per-decision event — see the goofspiel drive loop for why the
				// match-end Recorder alone is not enough.
				if s.decisionTracer != nil {
					de := telemetry.DecisionEvent{
						Game: GameName, MatchID: matchID, AgentID: id, Seat: v.YourSeat,
						Round: v.Day, Action: act.Kind, Outcome: string(outcome),
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

// mintProof returns this turn's proof token, or "" when no minter is wired.
func (p *pushPlayer) mintProof(agentID, matchID string, turn int) string {
	if p.turns == nil {
		return ""
	}
	return p.turns.Mint(agentID, matchID, turn)
}

func (p *pushPlayer) decideRemote(ctx context.Context, tr agentwire.Transport, matchID, agentID string, v AgentView) (mf.Action, benchmark.Outcome, int64, string, *benchmark.TokenUsage) {
	// The transcript is WINDOWED for the payload only. Every one of these events was already
	// pushed to this agent in real time by dispatchPublicEvents, so re-sending the whole
	// archive each turn buys nothing and, on a real 550-event match, costs tens of thousands
	// of tokens against free tiers that allow 6,000 per minute. The push stream and the
	// game-end record still carry everything — see the note in engine/mafia/view.go.
	windowed, digest := mf.WindowPublic(v.Public)
	req := MafiaPushView{
		Game: "mafia", MatchID: matchID, YourSeat: v.YourSeat, YourRole: v.YourRole,
		Day: v.Day, Phase: v.Phase, Alive: v.Alive, Allies: v.Allies, Legal: v.Legal,
		Public: windowed, Digest: digest, Private: v.Private,
		Votes: v.Votes, VoteTally: v.VoteTally, DeadlineMs: v.DeadlineMs,
		StartsAt: v.StartsAt, ServerNow: v.ServerNow,
		// One proof per DECISION, not per day: several decisions happen inside one Mafia
		// day, so the turn number folds the phase in (see turnproof.MafiaTurn). The SAME
		// number is published as Round above, which is what the agent reports back.
		Round:     turnproof.MafiaTurn(v.Day, v.Phase),
		TurnProof: p.mintProof(agentID, matchID, turnproof.MafiaTurn(v.Day, v.Phase)),
		// Role-scoped by the engine already: only a doctor's view carries CannotProtect and
		// only a mafia's carries AllyKills, so copying them unconditionally cannot leak.
		CannotProtect: v.CannotProtect,
		AllyKills:     v.AllyKills,
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
		// Carried so the built-in bot plays by the same rules a developer's agent does.
		CannotProtect: v.CannotProtect,
		AllyKills:     v.AllyKills,
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
		// NO target. This line names nobody, so it must not be recorded as accusing
		// anybody.
		//
		// It used to carry `target`, which is firstOtherAlive — the lowest living seat.
		// The engine bots read the transcript to decide who to vote for, so three seats
		// saying "Observing the table." registered as three accusations of that seat,
		// and every other bot dutifully piled on. A whole table lynched seat 1 on day
		// one, unanimously, having said nothing about it. That looked like the bots
		// voting by seat order; they were faithfully following an accusation that was
		// never made.
		return mf.Action{Kind: "message", Tone: "info", Text: "Observing the table."}
	case "protect":
		// The doctor may guard itself, but NOT the same seat two nights running — so a bot
		// that always returned its own seat was legal on night one and refused every night
		// after, leaving the house doctor doing nothing for the rest of the match.
		if v.CannotProtect != v.YourSeat {
			return mf.Action{Kind: "protect", Target: v.YourSeat}
		}
		if t := firstOtherAliveExcluding(v.Alive, v.YourSeat, v.CannotProtect); t > 0 {
			return mf.Action{Kind: "protect", Target: t}
		}
		return mf.Action{Kind: "abstain"}
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

// firstOtherAliveExcluding is firstOtherAlive with one more seat ruled out — the seat a
// doctor shielded last night and may not shield again tonight. Lowest seat first, so the
// choice stays deterministic and a replay reproduces it.
func firstOtherAliveExcluding(alive map[int]bool, seat, barred int) int {
	best := 0
	for s, ok := range alive {
		if ok && s != seat && s != barred && (best == 0 || s < best) {
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

// describeAction renders a Mafia action as "kind:target", or bare "kind" when the action
// has no target.
//
// The kind alone was being recorded, and that is not enough to say anything about the
// decision. "vote" tells you a seat voted; it does not tell you WHO it voted for, and who
// it voted for is the entire question — the engine knows every role, so "did this town
// agent vote for an actual mafia" is an objective fact the platform could score. Without
// the target that fact is unrecoverable, and unlike a scorer, DATA CAPTURE CANNOT BE
// RETROFITTED: every match played without it is permanently unscoreable.
//
// Also strictly better in the trace UI, where "vote:5" beats "vote".
func describeAction(a mf.Action) string {
	switch a.Kind {
	case mf.ActVote, mf.ActNightKill, mf.ActInvestigate, mf.ActProtect, mf.ActProfile:
		return a.Kind + ":" + strconv.Itoa(a.Target)
	default:
		// message, abstain and anything else carry no target worth recording.
		return a.Kind
	}
}

// DriveMatchedSeats pushes turns to the REAL agents on a table the group matcher started.
//
// # Why a ranked Mafia seat needed this
//
// mafiaTableCreator.CreateStartedTable builds a group-matched table with CreateTable + Join and
// then calls DriveHouseSeats, which drives ONLY the bot fillers. Its comment says real agents
// "act for themselves over the API" — true of a polling client, and not true of either
// transport the SDK actually offers:
//
//	pyyol run        waits for turn frames over its socket
//	hosted endpoint  waits to be POSTed to
//
// Both are PUSH. Nothing pushed. So a developer who queued for ranked Mafia was seated and then
// sat idle until every phase timed out — measured: four funded, matched agents, zero pushes,
// and all 27 historical LLM-backed Mafia matches came from the free-practice path instead.
//
// Goofspiel already has the equivalent (match.maybeDrive spawns its driver on a freshly paired
// match). This is that, for Mafia.
//
// Best-effort and non-blocking: it resolves each agent's transport and hands the whole set to
// the same drive loop free practice uses, so the two paths cannot drift. An agent with no
// reachable transport is skipped rather than failing the table — it forfeits by silence exactly
// as it does today, which is the pre-existing behaviour and not a new penalty.
func (s *Service) DriveMatchedSeats(ctx context.Context, matchPublicID string, realAgentIDs, allSeatIDs []string) {
	if s.pusher == nil || len(realAgentIDs) == 0 {
		return
	}
	remotes := make(map[string]agentclient.Target, len(realAgentIDs))
	for _, id := range realAgentIDs {
		target, found, err := s.pusher.remote.PlayTarget(ctx, id)
		connected := s.pusher.gw != nil && s.pusher.gw.Connected(id)
		switch {
		case err != nil && !connected:
			s.pusher.log.Debug("mafia matched-drive: no transport for seat",
				"match", matchPublicID, "agent", id, "err", err)
			continue
		case !connected && (!found || target.EndpointURL == ""):
			// No socket and no verified endpoint: nothing to push to. Skipped, not fatal.
			continue
		}
		remotes[id] = target
	}
	if len(remotes) == 0 {
		return
	}
	// context.WithoutCancel: the caller's context ends with the HTTP request that matched the
	// table, and the match outlives it by minutes. drive() applies its own maxMatch timeout.
	go s.pusher.drive(s, matchPublicID, remotes, allSeatIDs)
}
