package match

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-arena/arena/internal/integrity"
	"log/slog"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/deadline"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/liveness"
	"github.com/agent-arena/arena/internal/movebind"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/platform/telemetry"
	"github.com/agent-arena/arena/internal/readycheck"
	"github.com/agent-arena/arena/internal/redact"
	"github.com/agent-arena/arena/internal/replay"
)

// matchFinishedSeat is one seat's settlement line on match.finished (Eye money path).
type matchFinishedSeat struct {
	AgentID    string `json:"agent_id"`
	Seat       int    `json:"seat"`
	Score      int    `json:"score"`
	CoinsDelta int64  `json:"coins_delta"`
}

// matchFinishedPayload is the match.finished event body (competitive matches).
// WinnerAgent is empty on a tie. Bid/pool/seats let Pyyol Eye audit the money path
// without a second store; extra fields are ignored by older consumers.
type matchFinishedPayload struct {
	MatchID     string              `json:"match_id"`
	Game        string              `json:"game"`
	WinnerAgent string              `json:"winner_agent"`
	Bid         int64               `json:"bid"`
	RakePct     int                 `json:"rake_pct"`
	Pool        int64               `json:"pool"`
	Seats       []matchFinishedSeat `json:"seats,omitempty"`
}

// Config tunes the match loop.
type Config struct {
	MoveWindow time.Duration
	RakePct    int
	Rounds     int
	LockTTL    time.Duration
}

// Service drives the match lifecycle. It is stateless; all state lives in the
// repo (snapshot + event log) and mutations are serialized by a per-match lock.
type Service struct {
	queue  QueueClearer // clears ranked-queue entries when a match ends; see QueueClearer
	stakes StakeFloor   // rejects a stake the game does not offer; see StakeFloor
	repo   Repo
	lock   Locker
	limits Limits
	wallet Wallet
	bcast  Broadcaster
	notify Notifier
	ver    Verifier
	rater  Rater
	finish FinishHook
	bot    Bot
	style  StyleRecorder // nil ⇒ style aggregates not recorded (optional, best-effort)
	// windows derives each seat's decision budget from its demonstrated latency.
	// Nil ⇒ every match uses the configured constant, exactly as before.
	windows WindowProvider
	// livecheck gates deadline extensions. Nil ⇒ a deadline expires as it always did.
	// Distinct from the `liveness` tracker above, which records agent online state for
	// presence; this one answers "is it answering RIGHT NOW" for one specific decision.
	livecheck LivenessProber
	driver    *driver // nil ⇒ paired agents self-drive (auto-drive disabled)
	clock     platform.Clock
	cfg       Config
	// rake, when set, supplies the LIVE platform commission for a new match, so the
	// admin's fee control actually moves money instead of being decorative. Nil ⇒ the
	// static config value. Read at creation only; the result is persisted on the match
	// and settlement reads it back, so a mid-match change never re-prices a live table.
	rake func() int
	// liveness suppresses forfeits during the grace window after a detected platform
	// outage. Nil is valid and means "no grace" — see SweepExpired.
	liveness *liveness.Tracker
	// turns mints the per-turn proof token that binds a gateway LLM call to one
	// decision. Nil ⇒ no token is issued, so nothing can be proven LLM-backed.
	turns TurnMinter
	// integrity counts an agent's proven-LLM decisions. Nil ⇒ no ranked integrity
	// check at all.
	integrity IntegrityChecker
	// integrityMinPct is the share of a ranked match's decisions that must be proven
	// LLM-backed. INCLUSIVE ("at least this share"), which is what makes 100 a usable
	// setting — a strictly-greater rule could never be satisfied by 13 of 13. A
	// MAJORITY is therefore configured as 51, not 50.
	//
	// 0 DISABLES the share rule, which is the correct default until someone has looked at
	// what honest agents actually score: an agent that does not route through the gateway
	// produces no proofs and would look deterministic, so enforcing a share before that is
	// understood would void real matches wholesale. The zero-proof gate (rule 1) is
	// independent of this and stays on.
	integrityMinPct int
	// chatTracer records table talk to Lens. Nil ⇒ telemetry off.
	chatTracer ChatTracer
	// decisionTracer records each resolved agent turn. Nil ⇒ telemetry off.
	decisionTracer DecisionTracer
	// boundMoves reports the move the MODEL produced for a turn, as the gateway observed
	// it. Nil ⇒ completion binding is not enforced, which is the pre-existing behaviour.
	boundMoves movebind.Reader
	// rejections records that a seat's move was refused this round, so tryExtend can tell a
	// slow agent from one that answered and was turned away. Nil ⇒ extensions unchanged.
	rejections RejectionLog
	// Ready-check collaborators. All nil ⇒ no ready gate, which is the pre-existing
	// behaviour rather than a half-driven state machine.
	readyRepo    ReadyRepo
	readyAsker   ReadyAsker
	readyRequeue ReadyRequeuer
	// rateLimits reports 429s the gateway watched, so a throttled turn earns the same bounded
	// grace as a slow one instead of forfeiting a stake. Optional; nil-checked.
	rateLimits RateLimitObserver
	// queueEvents records queue/ready-check facts for reporting. Optional; nil-checked.
	queueEvents  QueueEventRecorder
	readyStarter ReadyStarter
}

// SetBoundMoveReader installs completion-binding enforcement (called once at wiring time).
//
// Without it the service behaves exactly as it did before: a move is authenticated by its
// signature and applied, with nothing checking it against the model's own answer.
func (s *Service) SetBoundMoveReader(r movebind.Reader) { s.boundMoves = r }

// RejectionLog records and reports that a seat's move was REFUSED for a round.
//
// A narrow port for one fact, declared next to the two places that need it: tryAct writes it,
// tryExtend reads it. Satisfied by *store.MatchRepo.
type RejectionLog interface {
	// RecordMoveRejection notes that this seat submitted a move for this round and it was
	// refused. Idempotent on (match, agent, round) — one refusal answers the question.
	RecordMoveRejection(ctx context.Context, matchID, agentPublicID string, round int, reason string) error
	// MoveRejected reports whether this seat has had a move refused for this round.
	MoveRejected(ctx context.Context, matchID, agentPublicID string, round int) (bool, error)
}

// SetRejectionLog installs the refused-move record that stops a rejected seat earning deadline
// extensions. Nil ⇒ extensions behave exactly as they did before.
func (s *Service) SetRejectionLog(r RejectionLog) { s.rejections = r }

// ChatTracer records agent table talk to the observability pipeline. Satisfied by
// *telemetry.Client; nil means telemetry is off and every call is a no-op.
type ChatTracer interface {
	EmitAgentSaid(ev telemetry.ChatEvent)
	EmitAgentSayRejected(ev telemetry.ChatEvent)
}

// SetChatTracer installs the chat tracer (called once at wiring time).
func (s *Service) SetChatTracer(t ChatTracer) { s.chatTracer = t }

// DecisionTracer records one resolved agent turn. Satisfied by *telemetry.Client.
type DecisionTracer interface {
	EmitAgentDecision(ev telemetry.DecisionEvent)
}

// SetDecisionTracer installs the per-decision tracer (called once at wiring time).
func (s *Service) SetDecisionTracer(t DecisionTracer) { s.decisionTracer = t }

// SetLiveness installs the post-outage grace tracker (called once at wiring time).
// Without it the service forfeits exactly as before, which is the safe default.
func (s *Service) SetLiveness(t *liveness.Tracker) { s.liveness = t }

// SetTurnMinter installs the per-turn proof minter. Must be called BEFORE
// EnableRankedDrive, which copies it onto the driver — otherwise views ship without
// a token and no decision can be proven LLM-backed.
func (s *Service) SetTurnMinter(m TurnMinter) { s.turns = m }

// SetIntegrityCheck installs the ranked LLM-backing check.
//
// minPct is the SHARE rule (see rankedIntegrityFailed rule 2) and 0 leaves it off.
// Passing 0 no longer means "no enforcement at all": the zero-proof gate (rule 1) is
// always active once a checker is installed, because it is self-calibrating and
// therefore safe to run before anyone has tuned a threshold.
//
// Turn minPct on only after looking at what honest agents actually score. Batching,
// caching and retry patterns are all legitimate and produce fewer proofs than
// decisions, so the right threshold is an observation, not a guess.
func (s *Service) SetIntegrityCheck(c IntegrityChecker, minPct int) {
	s.integrity, s.integrityMinPct = c, minPct
}

// seatWasAbsent reports whether a seat's failure to prove anything is explained by it
// having gone dark rather than by it having played unproven.
//
// The two are indistinguishable in the proof tables — both score zero — and they call
// for opposite outcomes:
//
//   - PLAYED BUT UNPROVEN is the case the void exists for. Detection is imperfect
//     (batching, caching, a direct provider call instead of pyyol.route()), so the
//     conservative answer is to cancel the match and give both stakes back rather than
//     confiscate a real developer's coins on a false positive.
//   - WENT DARK is not ambiguous at all. The platform asked, waited out the seat's full
//     window, got nothing, and played the worst legal card on its behalf. Voiding there
//     punishes the OPPONENT — who showed up, paid for inference and won — by cancelling
//     the win, and it hands the absent agent its stake back. Absence is the absent
//     agent's own risk: it stays at the table, loses on the board, and forfeits.
//
// The rule is a simple majority of the seat's own turns: a seat the platform had to play
// for more often than not was not meaningfully present. One slow round does not strip a
// match of integrity protection.
func seatWasAbsent(timeouts, decisions int) bool {
	if decisions <= 0 {
		return false
	}
	return timeouts*2 > decisions
}

// rankedIntegrityFailed reports whether a finished ranked match should be VOIDED
// because a seat cannot show it was played by an LLM, and names the seat if so.
//
// Pyyol is an arena for AI agents and ranked carries real money, so a hand-written
// script taking stakes from developers who are genuinely paying for inference is the
// thing this exists to stop. decisions is how many moves each seat actually made.
//
// A seat that was merely ABSENT is never grounds to void — see seatWasAbsent. It loses
// the match on the board and forfeits its stake, and the opponent is paid.
//
// FAILS OPEN. If the count cannot be read the match settles normally, because the
// alternative — voiding on a database hiccup — would cancel legitimate matches in
// bulk during an outage. A cheat that slips through is still recorded and reviewable;
// a wrongly voided match is a broken product for everyone playing at that moment.
func (s *Service) rankedIntegrityFailed(ctx context.Context, m Match, decisions int, timeouts [2]int) (bool, string) {
	if s.integrity == nil || decisions <= 0 {
		return false, ""
	}

	// Read every seat's proof count once. Both rules below are decided from the same
	// snapshot, so a seat cannot be judged against a different set of facts than its
	// opponent — and an unreadable count still fails open for the whole match.
	bound := make(map[string]int, len(m.Players))
	total := 0
	for _, p := range m.Players {
		n, err := s.integrity.BoundDecisions(ctx, m.PublicID, p.AgentPublicID)
		if err != nil {
			slog.Warn("match: integrity check unavailable; settling normally",
				"match", m.PublicID, "agent", p.AgentPublicID, "error", err)
			return false, ""
		}
		bound[p.AgentPublicID] = n
		total += n
	}

	// A seat that went dark is exempt from BOTH rules below. Its zero proofs are fully
	// explained by never having answered, so they are not evidence of anything, and the
	// remedy for absence is losing the game — which it already did — not cancelling the
	// opponent's win. Logged because a silently-skipped integrity check is exactly the
	// kind of thing that should never be invisible.
	absent := func(p Player) bool {
		if p.Seat < 0 || p.Seat >= len(timeouts) {
			return false
		}
		if !seatWasAbsent(timeouts[p.Seat], decisions) {
			return false
		}
		slog.Info("match: integrity check skipped for an ABSENT seat — it forfeits on the board rather than voiding the match",
			"match", m.PublicID, "agent", p.AgentPublicID, "seat", p.Seat,
			"timeouts", timeouts[p.Seat], "decisions", decisions)
		return true
	}

	// RULE 1 — the zero-proof gate. Always on, and safe to have always on.
	//
	// "Stakes must not flow to a seat that proved nothing" is the cheap version of
	// ranked integrity: it needs no threshold to tune, so it does not depend on
	// knowing what an honest agent scores. But applied literally it would void a match
	// whenever the proof pipeline was not running — the gateway is off by default, and an
	// agent that calls its provider directly rather than through pyyol.route() is
	// unverified without being dishonest. Every honest seat then measures zero, and the
	// gate would cancel real matches for reasons the developer did not choose.
	//
	// So the rule is RELATIVE: a zero-proof seat is only voided when SOME OTHER seat
	// in the SAME MATCH did prove its decisions. One proof anywhere on the table is
	// evidence the pipeline was live and reachable for that match; against that, a
	// seat with none is an outlier rather than a victim of an unshipped feature.
	//
	// The property this buys: the gate is inert until proofs actually exist, then
	// starts protecting automatically with no deploy and no threshold to pick. What it
	// deliberately does NOT catch is a table where nobody proves anything (two
	// deterministic scripts playing each other, or the gateway being off) — and it
	// cannot, without voiding honest play. That gap closes when the gateway is
	// enabled (PYYOL_LLM_GATEWAY_ENABLED) and routing through the gateway is the norm for
	// staked play; only then is "zero proofs" unambiguous.
	if total > 0 {
		for _, p := range m.Players {
			if bound[p.AgentPublicID] == 0 && !absent(p) {
				return true, p.AgentPublicID
			}
		}
	}

	// RULE 2 — the share threshold. Off by default (0) and still an observation
	// rather than a guess: batching, caching and retries all legitimately produce
	// fewer proofs than decisions, so the right number comes from looking at what
	// honest agents score once proofs are flowing.
	if s.integrityMinPct > 0 {
		for _, p := range m.Players {
			if absent(p) {
				continue
			}
			if bound[p.AgentPublicID]*100 < decisions*s.integrityMinPct {
				return true, p.AgentPublicID
			}
		}
	}
	return false, ""
}

// StyleRecorder accumulates per-agent behavioral style aggregates at match finish
// (read-only descriptive metrics; never affects play or money). Optional.
type StyleRecorder interface {
	RecordStyle(ctx context.Context, agentPublicID, game string, aggression, efficiency int) error
}

// WindowProvider decides how long to wait for one agent's decision.
//
// A seam rather than a constant because a single number cannot serve both a 0.9s cloud
// model and a 95s local one: generous enough for the second and one dead agent stalls
// every table, tight enough for the first and honest slow agents lose rounds they were
// winning. See internal/deadline — the window is derived from what the agent has actually
// demonstrated, floored and ceilinged.
//
// Optional. Unset, every match uses cfg.MoveWindow exactly as before, so adopting this
// changes nothing until a provider is installed.
type WindowProvider interface {
	// Window returns the decision budget for this agent in this game. Implementations
	// must be fast and must never block a turn — a cached or best-effort answer is
	// correct here, a slow one is not.
	Window(ctx context.Context, agentPublicID, game string) time.Duration
}

// SetWindowProvider installs adaptive decision windows. Nil keeps the static config.
func (s *Service) SetWindowProvider(w WindowProvider) {
	if w != nil {
		s.windows = w
	}
}

// moveWindow is the budget for a seat's next decision: the provider's answer when one is
// installed, else the configured constant.
//
// Falls back on ANY doubt — no provider, no agent, a non-positive answer. A deadline is on
// the path of every turn on the platform, so this must degrade to the old behaviour rather
// than risk a zero window, which would forfeit every decision the instant it was asked.
func (s *Service) moveWindow(ctx context.Context, agents ...string) time.Duration {
	base := s.cfg.MoveWindow
	if s.windows == nil {
		return base
	}
	// A round deadline is SHARED by both seats, so the table runs on the slower agent's
	// window. Taking the faster one would cut the slower agent off mid-decision through
	// no fault of its own — it would be forfeiting rounds because of who it was matched
	// against, which is the one thing a deadline must never depend on.
	longest := base
	for _, a := range agents {
		if a == "" {
			continue
		}
		if w := s.windows.Window(ctx, a, "goofspiel"); w > longest {
			longest = w
		}
	}
	return longest
}

// agentIDs is every seated agent's public id, for a decision that concerns the whole table.
//
// A round deadline is SHARED, so it is computed from all seats and runs on the slowest —
// taking one seat's window would cut the other off through no fault of its own.
func (m Match) agentIDs() []string {
	ids := make([]string, 0, len(m.Players))
	for _, p := range m.Players {
		if p.AgentPublicID != "" {
			ids = append(ids, p.AgentPublicID)
		}
	}
	return ids
}

// roundStart is when the current round began, and whether that is known.
//
// Prefers the RECORDED start. Falls back to the old reconstruction only for a round that
// was already in flight when the column shipped, because a row written before then has no
// start to read and the previous behaviour is better than none.
//
// Returns false rather than a zero Time when there is nothing to anchor to: a zero Time
// would be silently converted into a think-time of decades and poured into the sample set
// that decides whether an agent is a human.
func (s *Service) roundStart(m Match) (time.Time, bool) {
	if m.RoundStartedAt != nil {
		return *m.RoundStartedAt, true
	}
	if m.RoundDeadline == nil {
		return time.Time{}, false
	}
	return m.RoundDeadline.Add(-s.cfg.MoveWindow), true
}

// RateLimitObserver reports whether the platform WATCHED this seat get rate-limited on this
// turn.
//
// Stronger evidence than any health probe. A liveness probe says "something is listening";
// a 429 recorded by the gateway says "this agent made a real model call for THIS decision and
// its provider refused it". The platform saw the attempt happen.
//
// This exists because the alternative is indefensible: a developer on a free tier loses a
// STAKED match — real coins — because their provider throttled them mid-turn. They did not
// play badly and they did not go dark. Free tiers are tight enough for this to be routine:
// OpenRouter allows 50 requests a day, Groq 6,000 tokens a minute.
//
// OPTIONAL. Nil means deadlines behave exactly as they did.
type RateLimitObserver interface {
	// RateLimited answers "did a 429 land for this (agent, match, round) inside `within`".
	// Scoped to the round on purpose: a 429 from an earlier turn says nothing about whether
	// this one is being attempted, and treating it as evidence would hand an idle agent an
	// extension it did not earn.
	RateLimited(ctx context.Context, agentPublicID, matchPublicID string, round int, within time.Duration) bool
}

// SetRateLimitObserver enables 429-aware deadline extensions. Nil leaves them off.
func (s *Service) SetRateLimitObserver(o RateLimitObserver) { s.rateLimits = o }

// LivenessProber reports whether an agent's endpoint is answering right now.
//
// Satisfied by a small adapter over agentwire.ConfirmReachability. Optional: unset, a
// deadline expires exactly as it always did.
type LivenessProber interface {
	// Alive answers "is anything listening" for this agent. Must be fast and must never
	// block a sweep — it runs while a round is being decided. Any doubt should answer
	// false, because a false "alive" stalls a table while a false "gone" only forfeits a
	// turn the agent was already failing to answer.
	Alive(ctx context.Context, agentPublicID string) bool
}

// SetLivenessProber enables liveness-gated deadline extensions. Nil keeps them off.
func (s *Service) SetLivenessProber(p LivenessProber) {
	if p != nil {
		s.livecheck = p
	}
}

// tryExtend gives a still-alive agent more time instead of forfeiting its turn.
//
// This is the half of the adaptive-deadline design that makes a single window unnecessary.
// A constant has to be simultaneously generous enough for a 95s local model and tight
// enough that a dead agent does not stall a table — impossible, because it cannot tell
// the two apart. A probe can: /health is free, involves no inference, and answers exactly
// the question the deadline was guessing at.
//
// Alive means it is genuinely still thinking, so extend. Gone means stop waiting now
// rather than burning the remainder of the window on a process that will never answer.
//
// Bounded by the policy ceiling, so a hung-but-responsive endpoint cannot extend forever.
//
// # The bound, and why it needs a fixed origin
//
// Extensions granted so far are DERIVED from elapsed time rather than tracked in a counter.
// That is still the right call — a counter is more state to keep consistent across a crash —
// but it only works if elapsed is measured from something an extension does NOT move.
//
// It was measured from RoundDeadline, which the extension itself pushes forward, so after each
// grant elapsed snapped back to roughly one window, the derived count never climbed, and the
// ceiling was never reached. On a live staked table Goofspiel granted 17 extensions against a
// MaxExtensions of 3, holding one round open for twelve minutes.
//
// RoundDeadlineBase is the deadline as first set for this round and no extension touches it, so
// elapsed measured from it is the real time this round has been open. See migration 0085.
//
// This became reachable in practice with completion binding: an agent whose every move is
// refused answers /health perfectly well, so it reads as "still thinking" indefinitely — which
// would have let a rejected cheat stall a table other people have staked on.
//
// # DECIDED, NOT YET IMPLEMENTED: a REJECTED move should earn no extension
//
// The extension exists to give a slow-but-working agent time to answer. A seat whose move was
// refused is not waiting on a model — it answered, and the answer contradicted what its own
// model produced. Extending there rewards the behaviour and holds up an opponent who staked
// real coins: at the policy ceiling of 3, a rejected cheat costs the table roughly two minutes
// per round instead of one window.
//
// The counter-argument does not survive contact: an agent that submitted a bad move and wants
// to correct it can simply resubmit inside the window it already has. The extension is not the
// retry mechanism.
//
// Not implemented here because it needs state this function does not have. "Was a move rejected
// for this seat this round" is not derivable from the match row, the decision log (a refused
// move never becomes a decision) or the liveness probe, and it has to survive across instances,
// so it means a durable per-(match, round, seat) rejection marker written by tryAct. That is a
// small schema change and a settlement-affecting behaviour change, which together deserve their
// own commit rather than a rushed rider on the deadline fix.
//
// Returns true when the deadline was pushed out and the caller should NOT force a timeout.
func (s *Service) tryExtend(ctx context.Context, m Match, unsealed []string) bool {
	if s.livecheck == nil || len(unsealed) == 0 || m.RoundDeadline == nil {
		return false
	}
	pol := deadline.DefaultPolicy(m.Game)
	window := s.moveWindow(ctx, unsealed...)
	// The FIXED origin, falling back to the current deadline when a row carries no base — which
	// is the pre-existing behaviour, not a silent loss of the bound.
	origin := m.RoundDeadline
	if m.RoundDeadlineBase != nil {
		origin = m.RoundDeadlineBase
	}
	elapsed := s.clock.Now().Sub(origin.Add(-window))
	granted := 0
	if elapsed > window && pol.Extension > 0 {
		granted = int((elapsed - window) / pol.Extension)
	}
	ext, ok := deadline.Extend(pol, elapsed, granted)
	if !ok {
		return false // ceiling or extension cap reached: the turn is genuinely over
	}
	// Only extend for a seat that is actually THERE. One dead seat must not buy the
	// table more time, or a crashed agent stalls every round to the ceiling.
	for _, agent := range unsealed {
		// Alive OR provably throttled. A 429 the gateway recorded for THIS round is proof the
		// agent is present and trying — it made the call and the provider refused it — so it
		// earns the same grace a slow-but-answering agent gets.
		//
		// The bound is untouched: this only decides WHETHER a seat qualifies. How much time it
		// gets still comes from deadline.Extend above, with MaxExtensions and Ceiling
		// unchanged, so a throttled agent cannot hold a table open any longer than a slow one.
		// The opponent staked coins too.
		//
		// Window is the round's own window: evidence from an earlier turn does not show this
		// turn is being attempted.
		if !s.livecheck.Alive(ctx, agent) &&
			!(s.rateLimits != nil && s.rateLimits.RateLimited(ctx, agent, m.PublicID, m.State.Round, window)) {
			return false
		}
	}
	// A seat that ANSWERED and was REFUSED is not thinking. /health says nothing useful about
	// it — an agent whose every move contradicts its own model's output stays perfectly
	// responsive while being turned away each time, which is how a rejected seat could hold a
	// round open to the policy ceiling on a table someone else has staked on.
	//
	// Reads as "no rejection" on an error, so a lookup failure costs the table nothing it had
	// before. This can only ever REMOVE extra time, never grant it, so failing open here means
	// behaving exactly as the code did before this check existed.
	for _, agent := range unsealed {
		rejected, err := s.rejectedThisRound(ctx, m, agent)
		if err != nil {
			slog.Debug("match: could not read the move-rejection log; extending as before",
				"match", m.PublicID, "agent", agent, "error", err)
			continue
		}
		if rejected {
			slog.Info("match: NOT extending — this seat submitted a move and it was refused, so it is not still thinking",
				"match", m.PublicID, "agent", agent, "round", m.State.Round)
			return false
		}
	}
	next := s.clock.Now().Add(ext)
	if err := s.repo.ExtendDeadline(ctx, m.PublicID, next); err != nil {
		slog.Debug("match: could not extend a deadline; forfeiting the turn as before",
			"match", m.PublicID, "error", err)
		return false
	}
	slog.Info("match: deadline extended — the agent is still answering /health, so it is thinking rather than gone",
		// The ROUND, because without it an extension cannot be matched against the
		// refused-move record for the same round, and "did this grant more time to a seat we
		// had already turned away" becomes unanswerable from the log.
		"match", m.PublicID, "round", m.State.Round, "unsealed", unsealed,
		"extension", ext, "elapsed", elapsed)
	return true
}

// rejectedThisRound reports whether a seat has had a move refused for the round now open.
func (s *Service) rejectedThisRound(ctx context.Context, m Match, agentPublicID string) (bool, error) {
	if s.rejections == nil {
		return false, nil
	}
	return s.rejections.MoveRejected(ctx, m.PublicID, agentPublicID, m.State.Round)
}

// SetStyleRecorder installs the (optional) style aggregator. Nil keeps it off.
func (s *Service) SetStyleRecorder(r StyleRecorder) {
	if r != nil {
		s.style = r
	}
}

// SetNotifier installs the low-latency wake-up channel for long-polling agents
// after construction (mirrors wallet.SetPayoutGate). Nil keeps the no-op default,
// so tests and notifier-less deployments still work (long-poll falls back to its
// timeout). Wired in main once Redis is available.
func (s *Service) SetNotifier(n Notifier) {
	if n != nil {
		s.notify = n
	}
}

// SetBot installs the house-agent move picker used by sandbox matches, after
// construction (mirrors SetNotifier). Nil keeps the NoopBot default, so existing
// callers and tests that don't use sandbox mode are unaffected.
func (s *Service) SetBot(b Bot) {
	if b != nil {
		s.bot = b
	}
}

func New(repo Repo, lock Locker, limits Limits, wallet Wallet, bcast Broadcaster, ver Verifier, rater Rater, finish FinishHook, clock platform.Clock, cfg Config) *Service {
	if cfg.MoveWindow <= 0 {
		cfg.MoveWindow = 20 * time.Second
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = 10 * time.Second
	}
	if cfg.Rounds <= 0 {
		cfg.Rounds = 13
	}
	if rater == nil {
		rater = NoopRater{}
	}
	if finish == nil {
		finish = NoopFinishHook{}
	}
	return &Service{repo: repo, lock: lock, limits: limits, wallet: wallet, bcast: bcast, notify: NoopNotifier{}, ver: ver, rater: rater, finish: finish, bot: NoopBot{}, clock: clock, cfg: cfg}
}

func lockKey(matchPublicID string) string { return "match:lock:" + matchPublicID }

// agentJoinLockKey serializes a single agent's concurrent join attempts across the
// whole platform (shared prefix with mafia so the two games can't race the same
// agent into one match each). (M6)
func agentJoinLockKey(agentPublicID string) string { return "agent:join:lock:" + agentPublicID }

// publish pushes game events AND the resulting "thinking…" set.
//
// Both seats seal simultaneously in Goofspiel, so the pending set is normally BOTH
// agents; a seat drops out of it the moment its card_sealed lands, in the same tick.
func (s *Service) publish(matchPublicID string, state gs.State, events []gs.Event) {
	s.bcast.Broadcast(matchPublicID, events)
	s.bcast.BroadcastPending(matchPublicID, unsealedSeats(state))
	s.notify.Notify(matchPublicID)
}

// unsealedSeats is who still owes a card — derived from state, never invented.
func unsealedSeats(state gs.State) []int {
	if state.Finished {
		return nil
	}
	var out []int
	for seat := 0; seat < 2; seat++ {
		if state.Sealed[seat] == nil {
			out = append(out, seat)
		}
	}
	return out
}

func (s *Service) engine(m Match) *gs.Engine {
	cfg := gs.DefaultConfig()
	cfg.Rounds = m.TotalRounds
	cfg.FairnessMode = m.FairnessMode
	return gs.New(cfg)
}

// Lobby lists open matches the caller may join.
func (s *Service) Lobby(ctx context.Context, game string, bid int64, ownerPublicID string) ([]LobbyItem, error) {
	if game == "" {
		game = "goofspiel"
	}
	return s.repo.ListWaiting(ctx, game, bid, ownerPublicID, 50)
}

// CreateOpen opens a new waiting match seated by the creator at seat A.
// QueueClearer removes finished players' ranked-queue entries.
//
// # The bug this closes
//
// A queue entry was set to 'matched' at pairing and then never cleared. Live rows were still
// 'matched' against matches that had finished an hour earlier. autoplay's Queued() treats
// 'matched' as still-queued, so an autoplay agent played exactly ONE ranked match and then
// wedged forever — reporting "in a ranked match or waiting in the queue", which is the most
// reassuring possible way to be stuck. The orphaned entries are also unpairable (pairing selects
// 'waiting'), so they crowd the queue and newcomers starve behind them.
//
// Cleared at FINALIZE because that is the authoritative moment the match ends. Anywhere later is
// a sweeper racing the next autoplay tick; anywhere earlier and a crash mid-settlement would drop
// an agent out of a queue it is still legitimately in.
type QueueClearer interface {
	ClearQueue(ctx context.Context, agentPublicIDs ...string) error
}

// SetQueueClearer wires ranked-queue cleanup on match completion.
func (s *Service) SetQueueClearer(q QueueClearer) { s.queue = q }

// StakeFloor validates that a coin amount is a stake the game actually offers.
//
// Enforced HERE, at the service that escrows, rather than only in the HTTP handler. The handler
// was already correct — it called ResolveStake and would have rejected a free-form bid because
// goofspiel has tiers. But internal/bot/runner.go calls CreateOpen directly with a hardcoded
// bid := int64(50), and so never met that check. 870 matches were staked at 50 and 100 coins
// against a configured floor of 500, beginning two seconds after the tiers were seeded and
// continuing for two days without one error.
//
// The lesson is about PLACEMENT, not about the missing check: a guard beside one caller is one
// new caller away from being bypassed. Money is escrowed here, so the floor belongs here.
type StakeFloor interface {
	ValidStake(ctx context.Context, game string, coins int64) (ok bool, lowest int64, err error)
}

// SetStakeFloor wires tier enforcement into the service that escrows.
func (s *Service) SetStakeFloor(f StakeFloor) { s.stakes = f }

// checkStake rejects a stake the game does not offer. Fails CLOSED: an unreadable tier table is
// not permission to escrow an arbitrary amount, which is the exact failure mode that let
// sub-floor matches run unnoticed.
func (s *Service) checkStake(ctx context.Context, bid int64) error {
	if s.stakes == nil || bid <= 0 {
		return nil
	}
	ok, lowest, err := s.stakes.ValidStake(ctx, "goofspiel", bid)
	if err != nil {
		return httpx.NewError(http.StatusServiceUnavailable, "stakes_unavailable",
			"Stake tiers could not be read, so the stake cannot be verified. Try again shortly.")
	}
	if !ok {
		return httpx.NewError(http.StatusBadRequest, "stake_not_offered",
			fmt.Sprintf("A stake of %d coins is not offered for goofspiel. The lowest available stake is %d coins.", bid, lowest))
	}
	return nil
}

// checkSeat runs the spending limits for the agent that will sit.
//
// A private room uses CheckJoinCoveringStake when the limiter knows it: the
// sitting agent's wallet must cover the bid, without a leftover owner-treasury
// balance or a min_wallet_reserve stacked on top. Ranked / open lobby still
// use the full CheckJoin (bid + reserve). Stake floor and same-owner
// refusal stay shared. Playability does not — see checkPlayable.
func (s *Service) checkSeat(ctx context.Context, agentPublicID string, bid int64, private bool) error {
	if private {
		type covering interface {
			CheckJoinCoveringStake(context.Context, string, int64) error
		}
		if c, ok := s.limits.(covering); ok {
			return c.CheckJoinCoveringStake(ctx, agentPublicID, bid)
		}
	}
	return s.limits.CheckJoin(ctx, agentPublicID, bid)
}

// checkPlayable is who may sit. The open lobby is ranked: it uses CheckEligible
// (certified manifest, hosted-URL verify when one is declared). A private room
// is not the ranked lobby. When the verifier exposes CheckPrivateRoom, rooms
// use that instead — local CLI / connected-ranked / autoplay-without-URL, or a
// hosted verified endpoint — so "verify your endpoint for ranked" cannot block
// a friend invite. Verifiers that omit the method keep the old shared gate.
func (s *Service) checkPlayable(ctx context.Context, agentPublicID string, private bool) error {
	if private {
		type roomGate interface {
			CheckPrivateRoom(context.Context, string) error
		}
		if g, ok := s.ver.(roomGate); ok {
			return g.CheckPrivateRoom(ctx, agentPublicID)
		}
	}
	return s.ver.CheckEligible(ctx, agentPublicID)
}

// CreateRoom opens a PRIVATE waiting match: a room reachable only by its public id.
//
// # Why this delegates rather than duplicating
//
// A room is an open match with one bit flipped. Stake floor, escrow on join,
// and the refusal to join your own match stay on this path so they cannot
// drift — this codebase has already been bitten by a second caller that
// skipped the stake floor. Spending limits use the covering-stake check
// (see checkSeat). Playability is the one control that must differ: rooms
// are not the ranked lobby, so they must not inherit "verify your endpoint".
//
// # What the caller is buying
//
// Invisibility, plus a sit gate that accepts a local CLI agent the same way
// it accepts a hosted verified one. The room does not escape the rake and
// does not get a different settlement path.
func (s *Service) CreateRoom(ctx context.Context, agentPublicID, ownerPublicID string, bid int64) (string, error) {
	return s.createWaiting(ctx, agentPublicID, ownerPublicID, bid, true)
}

func (s *Service) CreateOpen(ctx context.Context, agentPublicID, ownerPublicID string, bid int64) (string, error) {
	return s.createWaiting(ctx, agentPublicID, ownerPublicID, bid, false)
}

// createWaiting is the one implementation behind both the open lobby and rooms.
//
// `private` is the listing bit, plus the room money check (see checkSeat) and
// the room playability gate (see checkPlayable). Stake floor, escrow on join,
// and the refusal to join your own match stay shared so they cannot drift.
func (s *Service) createWaiting(ctx context.Context, agentPublicID, ownerPublicID string, bid int64, private bool) (string, error) {
	if err := s.checkStake(ctx, bid); err != nil {
		return "", err
	}
	if bid <= 0 {
		return "", httpx.NewError(400, "invalid_request", "bid must be > 0")
	}
	if err := s.checkSeat(ctx, agentPublicID, bid, private); err != nil {
		return "", err
	}
	if err := s.checkPlayable(ctx, agentPublicID, private); err != nil {
		return "", err
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	m, err := s.repo.CreateWaitingMatch(ctx, CreateMatchInput{
		PublicID:      platform.NewID(platform.PrefixMatch),
		Game:          "goofspiel",
		Bid:           bid,
		RakePct:       s.rakePct(),
		TotalRounds:   s.cfg.Rounds,
		EngineVersion: gs.Version,
		Commit:        gs.Commit(seed),
		FairnessMode:  gs.FairnessShuffled,
		Seed:          seed,
		Creator:       Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: gs.SeatA},
		Private:       private,
	})
	if err != nil {
		return "", err
	}
	return m.PublicID, nil
}

// Cancel aborts a waiting lobby entry created by the caller. No stakes are locked
// until a second player joins, so cancellation is always safe.
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

// CreatePaired opens an already-active match between two server-matched agents
// (the matchmaking path): it runs both agents' join checks, escrows both stakes,
// deals the match, and persists it active in one step — no waiting window, so it
// never appears in the open lobby. Returns the new match's public id.
func (s *Service) CreatePaired(ctx context.Context, aAgent, aOwner, bAgent, bOwner string, bid int64) (string, error) {
	if err := s.checkStake(ctx, bid); err != nil {
		return "", err
	}
	if bid <= 0 {
		return "", httpx.NewError(400, "invalid_request", "bid must be > 0")
	}
	// Both seats must clear the spending limits and verification gate.
	if err := s.limits.CheckJoin(ctx, aAgent, bid); err != nil {
		return "", err
	}
	if err := s.limits.CheckJoin(ctx, bAgent, bid); err != nil {
		return "", err
	}
	if err := s.ver.CheckEligible(ctx, aAgent); err != nil {
		return "", err
	}
	if err := s.ver.CheckEligible(ctx, bAgent); err != nil {
		return "", err
	}

	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	cfg := gs.DefaultConfig()
	cfg.Rounds = s.cfg.Rounds
	cfg.FairnessMode = gs.FairnessShuffled
	eng := gs.New(cfg)
	state, events := eng.Init(seed)

	publicID := platform.NewID(platform.PrefixMatch)
	in := CreatePairedInput{
		PublicID: publicID, Game: "goofspiel", Bid: bid, RakePct: s.rakePct(),
		TotalRounds: s.cfg.Rounds, EngineVersion: gs.Version, Commit: gs.Commit(seed),
		FairnessMode: gs.FairnessShuffled, Seed: seed,
		SeatA: Player{AgentPublicID: aAgent, OwnerPublicID: aOwner, Seat: gs.SeatA},
		SeatB: Player{AgentPublicID: bAgent, OwnerPublicID: bOwner, Seat: gs.SeatB},
		State: state, Events: events,
	}

	// NO READY CHECK CONFIGURED ⇒ the original behaviour, unchanged: escrow now and start
	// now. A deployment that has not wired the sweeper must not create tables nothing can
	// release, which would look exactly like matchmaking having died.
	if s.readyRepo == nil {
		if err := s.wallet.StakeMatch(ctx, publicID, aAgent, bAgent, bid); err != nil {
			return "", err
		}
		in.Deadline = s.clock.Now().Add(s.moveWindow(ctx, aAgent, bAgent))
		if err := s.repo.CreatePairedActive(ctx, in); err != nil {
			// Persisting failed after staking — return both bids so no coins are stuck.
			//
			// The refund is idempotent (it shares disburse:{match} with settle, so a
			// settle can never land on top of it), which is what makes it safe to shout
			// about and retry rather than swallow. A DISCARDED error here was a silent
			// coin loss: stake taken, table never created, refund failed, and the only
			// evidence was an escrow balance that no longer added up. There is not even
			// a match row for the audit to hang it on — which is precisely the residue
			// the escrow_unattributed check now reports.
			if rerr := s.wallet.RefundStakes(ctx, publicID, aAgent, bAgent, bid); rerr != nil {
				slog.Error("STAKE NOT REFUNDED after failed activation — coins are held with no match",
					"match", publicID, "agent_a", aAgent, "agent_b", bAgent, "bid", bid,
					"activation_error", err, "refund_error", rerr)
			}
			return "", err
		}
		s.publish(publicID, state, events)
		s.maybeDrive(publicID, aAgent, bAgent)
		return publicID, nil
	}

	// THE READY PATH. Nothing is escrowed and no deadline is set: the table is dealt but not
	// started, and the sweeper takes it from here — asking each seat, then escrowing and
	// activating once both have answered. A developer who typed a command in a terminal now
	// gets the chance to open the match before any of their coins are committed, and an agent
	// that was paired mid-startup is dropped rather than staked.
	//
	// Deliberately NOT published and NOT driven yet. Publishing would announce a match that
	// has not begun, and driving would ask for a move on a table whose seats have not agreed
	// to play. Both happen in startAfterReady.
	if err := s.readyRepo.CreatePairedReadyCheck(ctx, in); err != nil {
		return "", err
	}
	return publicID, nil
}

// CreateSandbox opens an ACTIVE, risk-free practice match: the developer's agent
// at seat A versus a platform house agent at seat B, playing the given bot policy.
// Unlike the competitive paths it stakes NO coins, enforces NO spending limits,
// runs NO eligibility gate, and (on finish) updates NO rating — yet it deals,
// persists, broadcasts, and replays through the very same machinery, so the dev's
// client exercises the real wire contract. The house plays automatically (see
// commit). Returns the new match's public id.
func (s *Service) CreateSandbox(ctx context.Context, humanAgent, humanOwner, houseAgent, houseOwner, policy string) (string, error) {
	// Sandbox stakes nothing, so the money limits are skipped — but concurrency is a
	// THROUGHPUT limit, not a money one, and ignoring it multiplied an LLM agent's
	// inference bill by however many tables happened to be open. See CheckConcurrency.
	// The USER's side must be a verified LLM agent, even here.
	//
	// Sandbox stakes nothing, so it is tempting to let anything play. But a sandbox match is not
	// inert: it writes decision rows and benchmark rows, and those feed the P-Index, the model
	// board and the deception index. A scripted agent farming free tables would build a public
	// record it did not earn, which is the same fraud as winning coins with one — just paid in
	// reputation instead of currency.
	//
	// The HOUSE side is deliberately not checked. It is ours, it is labelled rules-engine, and it
	// is the opponent rather than the subject: nothing it does is published as a developer's
	// achievement. That asymmetry is the whole rule — our deterministic bots may fill a seat, a
	// user's may not.
	if err := s.ver.CheckEligible(ctx, humanAgent); err != nil {
		return "", err
	}
	if err := s.limits.CheckConcurrency(ctx, humanAgent); err != nil {
		return "", err
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", err
	}
	cfg := gs.DefaultConfig()
	cfg.Rounds = s.cfg.Rounds
	cfg.FairnessMode = gs.FairnessShuffled
	eng := gs.New(cfg)
	state, events := eng.Init(seed)

	publicID := platform.NewID(platform.PrefixMatch)
	deadline := s.clock.Now().Add(s.moveWindow(ctx, humanAgent))
	in := CreatePairedInput{
		PublicID: publicID, Game: "goofspiel", Mode: ModeSandbox, BotPolicy: policy,
		Bid: 0, RakePct: 0, TotalRounds: s.cfg.Rounds, EngineVersion: gs.Version,
		Commit: gs.Commit(seed), FairnessMode: gs.FairnessShuffled, Seed: seed,
		SeatA: Player{AgentPublicID: humanAgent, OwnerPublicID: humanOwner, Seat: gs.SeatA},
		SeatB: Player{AgentPublicID: houseAgent, OwnerPublicID: houseOwner, Seat: HouseSeat, IsHouse: true},
		State: state, Deadline: deadline, Events: events,
	}
	if err := s.repo.CreatePairedActive(ctx, in); err != nil {
		return "", err // no stake was taken, so nothing to unwind
	}
	s.publish(publicID, state, events)
	return publicID, nil
}

// Join seats the caller at seat B, deals the match, and starts round 1.
func (s *Service) Join(ctx context.Context, agentPublicID, ownerPublicID, matchPublicID string) (AgentView, error) {
	// Serialize an agent's joins so it can't race concurrent joins into DIFFERENT
	// matches and slip past the per-agent limits (CheckJoin, e.g. max-concurrent /
	// reserve) via TOCTOU — the per-match lock only serializes joins to the SAME
	// match. Agent lock FIRST, then match lock: a consistent global order that can't
	// deadlock. (M6)
	relAgent, okA, err := s.lock.Lock(ctx, agentJoinLockKey(agentPublicID), s.cfg.LockTTL)
	if err != nil {
		return AgentView{}, err
	}
	if !okA {
		return AgentView{}, ErrBusy
	}
	defer relAgent()

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
	if len(m.Players) > 0 && m.Players[0].OwnerPublicID == ownerPublicID {
		return AgentView{}, ErrSameOwner
	}
	if err := s.checkSeat(ctx, agentPublicID, m.Bid, m.Private); err != nil {
		return AgentView{}, err
	}
	if err := s.checkPlayable(ctx, agentPublicID, m.Private); err != nil {
		return AgentView{}, err
	}

	state, events := s.engine(m).Init(m.Seed)

	// Escrow BOTH stakes atomically as the match goes live. One balanced ledger
	// txn means we can never debit one seat and orphan its coins if the other
	// can't pay — it's all-or-nothing.
	creator := m.Players[0].AgentPublicID
	if err := s.wallet.StakeMatch(ctx, matchPublicID, creator, agentPublicID, m.Bid); err != nil {
		return AgentView{}, err
	}

	// The start countdown, and the reason the first move window opens at startsAt rather than
	// now. Mafia's startMatch and monopoly's startTable have both done this for a while;
	// goofspiel's lobby join did not, which is why every goofspiel row had a NULL starts_at
	// while mafia's had one. A table went from lobby to running with no moment in between, so
	// an agent still finishing startup lost the front of its first round to a match already
	// under way, and no surface had an absolute instant to count to.
	//
	// ONE clock reading feeds both values: taking now() twice would let the deadline be
	// computed from a moment before the countdown it is supposed to follow. Play is NOT gated
	// on startsAt — the match is active immediately, exactly as startAfterReady leaves it; what
	// the countdown buys is that the first window opens when play does.
	now := s.clock.Now()
	startsAt := readycheck.StartsAt(now, readycheck.DefaultPolicy("goofspiel").Countdown)
	deadline := startsAt.Add(s.moveWindow(ctx, agentPublicID))
	joiner := Player{AgentPublicID: agentPublicID, OwnerPublicID: ownerPublicID, Seat: gs.SeatB}
	if err := s.repo.Activate(ctx, matchPublicID, joiner, state, startsAt, deadline, events); err != nil {
		// Activation failed after staking — return both bids so no coins are stuck.
		if rerr := s.wallet.RefundStakes(ctx, matchPublicID, creator, agentPublicID, m.Bid); rerr != nil {
			slog.Error("STAKE NOT REFUNDED after failed join activation — coins are held with no live match",
				"match", matchPublicID, "creator", creator, "joiner", agentPublicID,
				"bid", m.Bid, "refund_error", rerr)
		}
		return AgentView{}, err
	}
	s.publish(matchPublicID, state, events)

	// Drive the table, exactly as the queue path does when it pairs two agents.
	//
	// This was missing, and it made the whole lobby route non-functional for
	// hosted-endpoint agents: a table could be created, an opponent could join, both
	// stakes went into escrow — and then NEITHER AGENT WAS EVER ASKED TO PLAY. The
	// sweeper force-timed-out every round and the match resolved entirely on fallbacks.
	// Observed on a real staked table: seven rounds in, timeouts [7,7], not one agent
	// ever asked to think.
	//
	// Only CreatePairedActive called it, so queue-matched games worked and lobby games
	// silently did not — the kind of split nobody notices until an agent is staked on the
	// wrong one.
	//
	// Spawned after the state is committed and published, so the driver's first read sees
	// an active match. maybeDrive only starts a goroutine when a seat is actually drivable
	// (socket or verified endpoint), and it can fire at most once per match because a
	// second Join can never re-activate a table that has left `waiting`.
	s.maybeDrive(matchPublicID, creator, agentPublicID)

	updated, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(updated, agentPublicID), nil
}

// Act applies an agent's card for the current round. Idempotent per (match,round,
// seat). If the agent registered a signing key, `signature` is REQUIRED and verified
// (proving the agent authored this exact move); the proof is persisted for replay.
//
// Concurrency model: the Redis lock is a FAST PATH that avoids wasted retries when
// it's held. Correctness comes from optimistic concurrency — the UNIQUE(match_id,
// seq) event-log constraint rejects a racing writer, surfacing as ErrConcurrentUpdate,
// and we re-read + retry. So a Redis outage degrades to a few extra retries, never a
// stuck, lost, or double-applied move.
// Act submits a move over the authenticated request path. If the agent registered
// a signing key the move must carry a valid Ed25519 signature.
func (s *Service) Act(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string) (AgentView, error) {
	return s.act(ctx, agentPublicID, matchPublicID, round, card, signature, false)
}

// DriveAct submits a move the PLATFORM decided on the agent's behalf after asking
// it over its authenticated socket connection. The socket auth (the agent's API key
// verified at register) is the authenticity guarantee, so a per-move Ed25519
// signature is NOT required here — this is what lets the server drive a signing-key
// agent's seat in a live match instead of wedging on ErrSignatureRequired.
func (s *Service) DriveAct(ctx context.Context, agentPublicID, matchPublicID string, round, card int) (AgentView, error) {
	return s.act(ctx, agentPublicID, matchPublicID, round, card, "", true)
}

// DriveTimeout applies the ENGINE's deterministic timeout for a seat whose agent did not
// answer, and records the miss.
//
// Why this exists rather than the driver just submitting the fallback card through
// DriveAct: State.Timeouts — the per-seat absence tally that settlement uses to tell a
// seat that went dark from one that played and could not prove itself — is incremented
// only by the engine's ForceTimeout. A driver that computes the same lowest card and
// submits it as an ordinary move produces an identical board and a tally that never moves.
//
// That was the shipped behaviour, and it was silently fatal to the absence rule: a
// hosted-endpoint agent could go dark for ten straight rounds and still finish with
// timeouts=[0,0], so seatWasAbsent always answered false and the forfeit could never
// arm on the very path most real agents use. Verified against a live match before the
// fix — 13 rounds, an agent dark from round 4, tally [0,0].
//
// Idempotent and race-safe on the same terms as DriveAct: a seat that has already sealed
// this round is returned unchanged rather than double-counted.
func (s *Service) DriveTimeout(ctx context.Context, agentPublicID, matchPublicID string, round int) (AgentView, error) {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusActive {
		return AgentView{}, ErrNotActive
	}
	p := m.playerByAgent(agentPublicID)
	if p == nil {
		return AgentView{}, ErrNotPlayer
	}
	if round != m.State.Round {
		return AgentView{}, ErrWrongRound
	}
	if m.State.Sealed[p.Seat] != nil {
		return s.view(m, agentPublicID), nil // already acted; nothing to force
	}

	eng := s.engine(m)
	state, events, err := eng.ForceTimeout(m.State, p.Seat)
	if err != nil {
		return AgentView{}, mapEngineErr(err)
	}
	updated, err := s.commit(ctx, m, eng, state, events)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(updated, agentPublicID), nil
}

func (s *Service) act(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string, platformDriven bool) (AgentView, error) {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return AgentView{}, ErrBusy
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless, relying on the OCC retry.)

	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.tryAct(ctx, agentPublicID, matchPublicID, round, card, signature, platformDriven)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue // another writer advanced first; re-read and retry
		}
		return view, err
	}
	return AgentView{}, ErrBusy // retries exhausted under heavy contention
}

// tryAct is one optimistic-concurrency attempt: read the current snapshot, validate,
// seal, and commit. A lost race returns ErrConcurrentUpdate for Act to retry.
func (s *Service) tryAct(ctx context.Context, agentPublicID, matchPublicID string, round, card int, signature string, platformDriven bool) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusActive {
		return AgentView{}, ErrNotActive
	}
	p := m.playerByAgent(agentPublicID)
	if p == nil {
		return AgentView{}, ErrNotPlayer
	}
	if round != m.State.Round {
		return AgentView{}, ErrWrongRound
	}
	// DB-backed idempotent replay: this seat already sealed this round → current
	// view, no error. Reads the committed snapshot, so it holds even with no lock.
	if m.State.Sealed[p.Seat] != nil {
		return s.view(m, agentPublicID), nil
	}

	// Move authenticity: if the agent registered a signing key, the move must carry
	// a valid Ed25519 signature over (match, round, seat, card). Verified BEFORE the
	// card is sealed, so a forged/altered move never enters the log.
	// Platform-driven moves (the server driving this seat over the agent's
	// authenticated socket) skip the per-move signature — the socket connection is
	// the authenticity guarantee. Self-drive (request-path) moves still require it.
	pubkey, err := s.repo.AgentSigningKey(ctx, agentPublicID)
	if err != nil {
		return AgentView{}, err
	}
	if pubkey != "" && !platformDriven {
		if signature == "" {
			return AgentView{}, ErrSignatureRequired
		}
		if !movesig.Verify(pubkey, matchPublicID, round, p.Seat, card, signature) {
			return AgentView{}, ErrBadSignature
		}
	}

	// Completion binding: the card must be the one this agent's MODEL chose, whenever the
	// gateway observed a model choosing one.
	//
	// Placed here, beside the signature check, and NOT on the HTTP handler. The stake floor
	// taught that lesson expensively: a control on the handler was simply bypassed by the
	// bot runner, which reaches the service directly. Every path that can seal a card comes
	// through tryAct.
	//
	// Applies to platform-driven moves too, unlike the signature check above. That exemption
	// exists because an authenticated socket already proves AUTHORSHIP; it says nothing
	// about whether a model chose the move, so it does not transfer to this question.
	if err := movebind.Enforce(ctx, s.boundMoves, slog.Default(), "match",
		matchPublicID, agentPublicID, round, movebind.CanonGoofspiel(card)); err != nil {
		// Note the refusal so the deadline sweep does not read this seat as "still thinking".
		// Best-effort and never fatal to the rejection itself: failing to record it costs an
		// extension budget, while failing the move would change what the control does.
		if s.rejections != nil {
			if rerr := s.rejections.RecordMoveRejection(ctx, matchPublicID, agentPublicID, round,
				"completion_binding"); rerr != nil {
				slog.Debug("match: could not record a move rejection",
					"match", matchPublicID, "agent", agentPublicID, "round", round, "error", rerr)
			}
		}
		return AgentView{}, err
	}

	// Record think-time for verification (best-effort).
	//
	// From the RECORDED round start, not a reconstruction. This number feeds
	// verification.Record, which builds the timing profile used to decide whether a human
	// is playing by hand — so a derived value that can be wrong in either direction is not
	// good enough for it.
	//
	// The old reconstruction, RoundDeadline minus the configured window, was wrong twice
	// over. RoundDeadline MOVES when an extension is granted, so after a grant the inferred
	// start slid later and the response time came out smaller — a genuinely slow agent
	// recorded as a fast one, masking exactly what the detector looks for. And the
	// configured constant is not the window in force once deadlines are adaptive, so for
	// any agent that had earned a longer window the start was placed too early and its
	// response times were inflated, pushing an honest slow agent toward being flagged.
	if started, ok := s.roundStart(m); ok {
		s.ver.Record(ctx, agentPublicID, &matchPublicID, int(s.clock.Now().Sub(started).Milliseconds()))
	}

	eng := s.engine(m)
	state, events, err := eng.Seal(m.State, p.Seat, card)
	if err != nil {
		return AgentView{}, mapEngineErr(err)
	}

	// Persist the authorship proof alongside the move (best-effort; off the
	// correctness path — the move is already sealed in the authoritative log).
	if pubkey != "" && !platformDriven {
		_ = s.repo.RecordMoveSignature(ctx, matchPublicID, round, p.Seat, card, signature, pubkey)
	}

	updated, err := s.commit(ctx, m, eng, state, events)
	if err != nil {
		return AgentView{}, err // ErrConcurrentUpdate bubbles to Act's retry loop
	}
	// P3: build the view from the just-committed state instead of re-reading the
	// full aggregate from Postgres — one fewer round trip on every move.
	return s.view(updated, agentPublicID), nil
}

// Roster returns the public seat → agent identities for a match. Public data only
// (name, owner, avatar) — a sealed card is the game's only secret and is never here,
// so this is safe to serve to any spectator mid-match.
func (s *Service) Roster(ctx context.Context, matchPublicID string) ([]RosterSeat, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return nil, ErrNotFound
	}
	return RosterOf(m.Players), nil
}

// Say posts one line of public table talk from a seated agent.
//
// Unlike Act this is NOT turn-gated and carries no round argument: an agent may
// speak whenever it likes during a live match — while the opponent is still
// deciding, between rounds, twice in a row. Talking never seals a card and never
// advances the round, so it cannot be used to stall or to skip a turn. Only the
// card itself is ordered.
//
// The line is appended to the authoritative event log (so it replays with the
// match) and broadcast to spectators, and it lands in State.Chat, which every
// agent view carries — that is what lets the other seat actually answer it.
func (s *Service) Say(ctx context.Context, agentPublicID, matchPublicID, text, kind string) (AgentView, error) {
	// TALK WAITS FOR THE LOCK; it does not get turned away by it.
	//
	// This used to take the lock once and return ErrBusy — HTTP 409 match_busy — the instant it
	// was held. Gameplay holds the same lock, so in a driven match there was almost never a gap,
	// and pushplay swallows the error by design ("a rejected line must never block the move").
	// The result was invisible: agents tried to speak, were refused, and nobody saw it.
	//
	// Measured in the lab: ZERO agent_says events across 11,064 finished Monopoly matches, and
	// talk in only 3.8%% of Goofspiel matches — while live runs of both games logged
	// "say failed: 409 match_busy" from agents that were trying.
	//
	// It also contradicted the documented rule: speaking is "deliberately NOT turn-gated ...
	// speaking never consumes a turn or blocks the round". A lock that refuses talk whenever the
	// table is mid-transition turn-gates it in practice.
	//
	// The lock is HELD FOR MILLISECONDS (a state read plus write), never across an agent's
	// thinking time, so a short bounded wait clears the ordinary collision. Bounded on purpose:
	// talk is not latency-critical, but a caller must never hang on it, and giving up still
	// returns ErrBusy so the behaviour is unchanged for a genuinely wedged table.
	//
	// The ENGINE still enforces the real speech rules — Mafia's night silence, Monopoly's
	// bankrupt seats, a finished match. This only stops a concurrency guard from standing in for
	// a game rule.
	var release func()
	for attempt := 0; attempt < 4; attempt++ {
		r, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL)
		if lerr != nil {
			break // Redis unreachable: proceed lockless, relying on the OCC retry below
		}
		if ok {
			release = r
			break
		}
		if attempt == 3 {
			return AgentView{}, ErrBusy
		}
		select {
		case <-ctx.Done():
			return AgentView{}, ctx.Err()
		case <-time.After(time.Duration(25*(attempt+1)) * time.Millisecond):
		}
	}
	if release != nil {
		defer release()
	}
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		view, err := s.trySay(ctx, agentPublicID, matchPublicID, text, kind)
		if errors.Is(err, ErrConcurrentUpdate) {
			continue
		}
		return view, err
	}
	return AgentView{}, ErrBusy
}

func (s *Service) trySay(ctx context.Context, agentPublicID, matchPublicID, text, kind string) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if m.Status != StatusActive {
		return AgentView{}, ErrNotActive
	}
	p := m.playerByAgent(agentPublicID)
	if p == nil {
		return AgentView{}, ErrNotPlayer
	}

	eng := s.engine(m)
	state, events, err := eng.Say(m.State, p.Seat, text, kind)
	if err != nil {
		// Rejections are traced too — see the mafia equivalent.
		if s.chatTracer != nil {
			s.chatTracer.EmitAgentSayRejected(telemetry.ChatEvent{
				Game: "goofspiel", MatchID: matchPublicID, AgentID: agentPublicID,
				Seat: p.Seat, Kind: kind, Text: text, Reason: "illegal",
			})
		}
		return AgentView{}, mapEngineErr(err)
	}
	if s.chatTracer != nil {
		s.chatTracer.EmitAgentSaid(telemetry.ChatEvent{
			Game: "goofspiel", MatchID: matchPublicID, AgentID: agentPublicID,
			Seat: p.Seat, Kind: kind, Text: text,
		})
	}
	updated, err := s.commit(ctx, m, eng, state, events)
	if err != nil {
		return AgentView{}, err
	}
	return s.view(updated, agentPublicID), nil
}

// commit resolves the round if both seats have sealed, persists, broadcasts, and
// returns the updated match aggregate (so callers avoid a redundant re-read). A
// lost optimistic-concurrency race propagates as ErrConcurrentUpdate.
func (s *Service) commit(ctx context.Context, m Match, eng *gs.Engine, state gs.State, events []gs.Event) (Match, error) {
	// Sandbox: the house agent auto-plays the instant the developer has sealed, so
	// the round resolves immediately and the dev always gets a complete round back
	// (no "waiting for opponent" stall). The house move is recorded in the event log
	// like any other, so the replay still verifies as a unit.
	if m.Mode == ModeSandbox {
		if hev, ok := s.playHouse(eng, m.BotPolicy, &state); ok {
			events = append(events, hev...)
		}
	}

	if state.Sealed[gs.SeatA] == nil || state.Sealed[gs.SeatB] == nil {
		// Still waiting on the opponent; same round, deadline unchanged.
		if err := s.repo.Advance(ctx, m.PublicID, state, m.RoundDeadline, nil, events); err != nil {
			return Match{}, err
		}
		s.publish(m.PublicID, state, events)
		m.State = state
		return m, nil
	}

	resolved, resolveEvents, err := eng.Resolve(state)
	if err != nil {
		return Match{}, err // both seats sealed here, so unreachable in practice — but never advance on a failed resolve
	}
	all := make([]gs.Event, 0, len(events)+len(resolveEvents))
	all = append(all, events...)
	all = append(all, resolveEvents...)

	if resolved.Finished {
		// G4 — durably persist the finishing SEAL(S) before settling. finalize does
		// Settle-before-Finish deliberately (crash-safety), but the finishing seal was
		// previously made durable only inside Finish. So a crash between Settle and
		// Finish would leave the match 'active' with the finishing seat UNSEALED in the
		// DB; the sweeper would then re-drive via ForceTimeout (lowest card) and record
		// a winner / Elo / replay hash that DIVERGES from the winner actually PAID.
		// Persisting the real sealed cards here means a re-drive resolves the real cards
		// (both already sealed ⇒ HandleTimeout forces nothing) and reproduces the paid
		// outcome. Money was always protected by settle idempotency; this protects
		// recorded-result integrity. The seal events are now persisted by this Advance,
		// so finalize appends only the resolve events (no double-append).
		if err := s.repo.Advance(ctx, m.PublicID, state, m.RoundDeadline, nil, events); err != nil {
			return Match{}, err
		}
		players, err := s.finalize(ctx, m, resolved, resolveEvents)
		if err != nil {
			return Match{}, err
		}
		s.publish(m.PublicID, resolved, all)
		m.State = resolved
		m.Players = players
		m.Status = StatusFinished
		m.RoundDeadline = nil
		return m, nil
	}

	// The ADAPTIVE window, not the configured constant.
	//
	// Every other path that opens a round already used it — the first deal (startPaired,
	// startSandbox, startHuman) and the ready-check activation all call s.moveWindow. This
	// one did not, so an agent that had earned a longer window got it for round 1 and then
	// silently dropped back to the 20s default for rounds 2..13. That is precisely the
	// failure the adaptive window exists to prevent: internal/deadline says the adaptive
	// term is there "to give a SLOW agent room", and a slow agent was getting that room
	// exactly once per match.
	//
	// It also fed a wrong number back into tryExtend, which reconstructs the round origin
	// as base.Add(-s.moveWindow(...)) — computed with the adaptive window against a
	// deadline set with the static one, so elapsed came out too large and extensions were
	// refused earlier than the policy allows.
	// One clock reading for both, so the start and the deadline cannot disagree about when
	// this round opened — the deadline IS the start plus the window, by construction.
	opened := s.clock.Now()
	next := opened.Add(s.moveWindow(ctx, m.agentIDs()...))
	// The only Advance in this function that opens a new round, so the only one that
	// stamps a round start. The two above pass nil deliberately.
	if err := s.repo.Advance(ctx, m.PublicID, resolved, &next, &opened, all); err != nil {
		return Match{}, err
	}
	s.publish(m.PublicID, resolved, all)
	m.State = resolved
	m.RoundDeadline = &next
	return m, nil
}

// playHouse seals the house agent's card for the current round in a sandbox match.
// It no-ops (ok=false) if the match is finished or the house seat is already
// sealed (e.g. a timeout sweep forced both seats). If the bot somehow returns an
// illegal card, it falls back to the engine's deterministic timeout move so the
// match can never wedge on a buggy strategy.
func (s *Service) playHouse(eng *gs.Engine, policy string, state *gs.State) ([]gs.Event, bool) {
	if state.Finished || state.Sealed[HouseSeat] != nil {
		return nil, false
	}
	card := s.bot.Pick(*state, HouseSeat, policy)
	ns, ev, err := eng.Seal(*state, HouseSeat, card)
	if err != nil {
		ns, ev, err = eng.ForceTimeout(*state, HouseSeat)
		if err != nil {
			return nil, false
		}
	}
	*state = ns
	return ev, true
}

// mustSettle reports whether a finished match has to disburse its escrow.
//
// The question is NOT "is this a sandbox table" but "was money actually staked". Those
// are the same thing only while every sandbox match has a zero bid, which is an
// assumption about the data rather than a guarantee the code enforces — and production
// produced the counterexample: six matches carrying a 500-coin bid that took both stakes
// and never settled, because the mode said sandbox and the whole settlement block was
// skipped. The CLI reported a +420 win the ledger never posted, and ~3,000 coins per
// agent sat in escrow with nothing to release them.
//
// Keying on the bid closes that hole regardless of HOW a staked match ends up mis-moded,
// which matters because the mis-moding itself has not been found yet. Staking already
// keys on the bid; this makes the disbursement agree with it. Escrow in and escrow out
// are now decided by the same fact.
//
// A genuine sandbox table is unaffected: its bid is 0, nothing was staked, and there is
// nothing to pay back.
func mustSettle(mode string, bid int64) bool {
	return mode != ModeSandbox || bid > 0
}

// finalize settles the match: compute winner + per-player deltas, settle coins,
// hash the full log, and persist the terminal state. Returns the players with their
// final scores + coin deltas so the caller can render the result without re-reading.
func (s *Service) finalize(ctx context.Context, m Match, state gs.State, newEvents []gs.Event) ([]Player, error) {
	winnerAgent := ""
	if state.Winner != gs.Tie {
		if wp := m.playerBySeat(state.Winner); wp != nil {
			winnerAgent = wp.AgentPublicID
		}
	}
	pool := m.Bid * 2
	// Sandbox matches move no coins: skip settlement entirely (bid is 0 anyway, so
	// this is also a belt-and-suspenders guard against any future money path).
	//
	// ORDER MATTERS (crash-safety): Settle runs BEFORE Finish deliberately. Settle is
	// idempotent on settle:{match}; Finish is the durable terminal marker. A crash
	// between them leaves the match 'active', so the sweeper (HandleTimeout →
	// SweepExpired) re-drives it and re-finalizes — Settle no-ops, Finish completes.
	// Do NOT reorder to Finish-first: that would strand escrow (winner never paid,
	// and Act/HandleTimeout early-return on a finished match, so nothing re-drives).
	// Hoisted out of the settlement block so the RATING path below can see the same
	// verdict. Before this, a match voided here for proving nothing was still rated a few
	// lines down, so a scripted agent was refunded every time and climbed for free.
	// Evaluated once and read twice: a second evaluation of the same table could disagree
	// with the first and leave a seat refunded but rated.
	integrityFailed, integrityAgent := false, ""

	if mustSettle(m.Mode, m.Bid) {
		// A ranked match that cannot show it was played by an LLM is VOIDED rather
		// than settled: both stakes go back and nobody is paid. Refund and Settle
		// share one idempotency key, so exactly one of them can ever take effect —
		// a re-drive after a crash cannot pay out a match that was voided, or void
		// one that already paid.
		//
		// Voiding rather than forfeiting is deliberate FOR A SEAT THAT PLAYED. Detection
		// is new and will have false positives (batching, caching, a direct provider
		// call), and taking a real developer's stake on a false positive is not
		// recoverable in the way an un-played match is.
		//
		// A seat that went DARK is the other case entirely and must not reach the void:
		// it forfeits and the opponent is paid. state.Timeouts carries how many rounds
		// the platform had to play for each seat, which is what separates the two.
		integrityFailed, integrityAgent = s.rankedIntegrityFailed(ctx, m, len(state.History), state.Timeouts)
		if agent := integrityAgent; integrityFailed {
			slog.Warn("match: VOIDED — seat could not prove its decisions were LLM-backed",
				"match", m.PublicID, "agent", agent, "decisions", len(state.History),
				"min_pct", s.integrityMinPct)
			if err := s.wallet.Refund(ctx, m.PublicID); err != nil {
				return nil, err
			}
		} else if err := s.wallet.Settle(ctx, m.PublicID, winnerAgent, pool, m.RakePct); err != nil {
			return nil, err
		}
	}

	// Compute the replay hash over the COMPLETE log (prior + new events).
	prior, err := s.repo.LoadEvents(ctx, m.PublicID)
	if err != nil {
		return nil, err
	}
	hash, err := replay.Hash(append(prior, newEvents...))
	if err != nil {
		return nil, err
	}

	players := finalizePlayers(m.Players, state, pool, m.Bid, m.RakePct)

	// Competitive matches emit a transactional match.finished fact (badges,
	// notifications, analytics project off it). Sandbox is off the growth path:
	// nil payload => no event.
	var finishedEvent []byte
	if m.Mode != ModeSandbox {
		seats := make([]matchFinishedSeat, 0, len(players))
		for _, p := range players {
			seats = append(seats, matchFinishedSeat{
				AgentID: p.AgentPublicID, Seat: p.Seat, Score: p.FinalScore, CoinsDelta: p.CoinsDelta,
			})
		}
		finishedEvent, err = json.Marshal(matchFinishedPayload{
			MatchID: m.PublicID, Game: m.Game, WinnerAgent: winnerAgent,
			Bid: m.Bid, RakePct: m.RakePct, Pool: pool, Seats: seats,
		})
		if err != nil {
			return nil, err
		}
	}
	// Clear both seats from the ranked queue. Best-effort and AFTER the terminal write: the
	// match is over either way, and failing a completed match because a queue row would not
	// delete would be strictly worse than a stale row the next enqueue overwrites anyway.
	if s.queue != nil {
		ids := make([]string, 0, len(m.Players))
		for _, p := range m.Players {
			ids = append(ids, p.AgentPublicID)
		}
		defer func() {
			if err := s.queue.ClearQueue(ctx, ids...); err != nil {
				slog.Warn("match: ranked queue entries not cleared; an autoplay agent may not "+
					"re-enter until its next enqueue overwrites the row",
					"match", m.PublicID, "error", err)
			}
		}()
	}
	if err := s.repo.Finish(ctx, m.PublicID, state, winnerAgent, hash, players, newEvents, finishedEvent); err != nil {
		return nil, err
	}

	// Sandbox is unrated and off the growth path: skip ratings, clips, and
	// notifications. Competitive matches update skill ratings and fire the
	// engagement hooks off the hot path. The match is already durably finished +
	// settled here, and Rate is idempotent on the match id (rating_updates marker),
	// so a transient rater error must not silently drop the leaderboard update:
	// retry a few times before surfacing it (nothing re-drives finalize once the
	// match is Finished — Act/HandleTimeout early-return on non-active).
	if m.Mode != ModeSandbox {
		rr := RatingResult{MatchPublicID: m.PublicID, Game: m.Game, WinnerSeat: state.Winner}
		for _, p := range players {
			rr.Players = append(rr.Players, RatingPlayer{AgentPublicID: p.AgentPublicID, Seat: p.Seat, CoinsDelta: p.CoinsDelta})
		}
		// A match voided for integrity must not move a rating either. The rater applies
		// integrity.FilterRatable to this verdict; for a 1v1 one blocked seat leaves no
		// comparison, so nothing is rated — matching the money rule exactly.
		if integrityFailed && integrityAgent != "" {
			rr.Integrity = integrity.Verdict{Armed: true, Unproven: map[string]bool{integrityAgent: true}}
		}
		if err := rateWithRetry(ctx, s.rater, rr, 3, 50*time.Millisecond); err != nil {
			return nil, err
		}
		s.finish.MatchFinished(ctx, m.PublicID)

		// Best-effort behavioral-style aggregates (read-only; never gate money or
		// completion). A recorder error is swallowed — the match is already durably
		// finished + settled + rated above.
		if s.style != nil {
			for _, p := range players {
				agg, eff := goofStyle(state, p.Seat)
				_ = s.style.RecordStyle(ctx, p.AgentPublicID, m.Game, agg, eff)
			}
		}
	}
	return players, nil
}

// goofStyle derives read-only behavioral metrics for one seat from the finished
// Goofspiel transcript. aggression = average bid strength (mean bid relative to
// the deck's top card, 0-100); efficiency = share of total prize value the seat
// captured (final score / total prize value, 0-100, carry-overs included).
func goofStyle(state gs.State, seat int) (aggression, efficiency int) {
	if len(state.History) == 0 || seat < 0 || seat > 1 {
		return 0, 0
	}
	maxCard, bidSum, prizeTotal := 0, 0, 0
	for _, r := range state.History {
		bidSum += r.Cards[seat]
		for _, c := range []int{r.Cards[0], r.Cards[1], r.Prize} {
			if c > maxCard {
				maxCard = c
			}
		}
		prizeTotal += r.Prize
	}
	rounds := len(state.History)
	if maxCard > 0 {
		aggression = bidSum * 100 / (rounds * maxCard)
	}
	if prizeTotal > 0 {
		efficiency = state.Scores[seat] * 100 / prizeTotal
	}
	return aggression, efficiency
}

// rateWithRetry applies the (idempotent) rating with a bounded retry so a transient
// error right after the match commits doesn't lose the leaderboard/Elo update. It
// honors context cancellation between attempts.
func rateWithRetry(ctx context.Context, rater Rater, rr RatingResult, attempts int, backoff time.Duration) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
		if err = rater.Rate(ctx, rr); err == nil {
			return nil
		}
	}
	return err
}

// HandleTimeout forces a missing seat to play (deterministically) once its window
// has expired, then resolves. Driven by the sweeper.
func (s *Service) HandleTimeout(ctx context.Context, matchPublicID string) error {
	if release, ok, lerr := s.lock.Lock(ctx, lockKey(matchPublicID), s.cfg.LockTTL); lerr == nil {
		if !ok {
			return nil // another instance holds it; skip this round
		}
		defer release()
	}
	// (lerr != nil — Redis unreachable: proceed lockless so the timeout still fires
	// and escrow can't stay wedged; OCC on commit protects against a racing writer.)

	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil || m.Status != StatusActive {
		return nil
	}
	if m.RoundDeadline == nil || s.clock.Now().Before(*m.RoundDeadline) {
		return nil // not actually expired (raced with a real action)
	}

	// Before forfeiting anyone's turn, find out whether they are actually gone.
	var unsealed []string
	for seat := 0; seat < 2; seat++ {
		if m.State.Sealed[seat] == nil && !(m.Mode == ModeSandbox && seat == HouseSeat) {
			if p := m.playerBySeat(seat); p != nil {
				unsealed = append(unsealed, p.AgentPublicID)
			}
		}
	}
	if s.tryExtend(ctx, m, unsealed) {
		return nil // still thinking; the sweeper will come back at the new deadline
	}

	eng := s.engine(m)
	state := m.State
	var events []gs.Event
	for seat := 0; seat < 2; seat++ {
		if state.Sealed[seat] == nil {
			// In sandbox, the house seat is never force-timed-out: commit lets it
			// play its real policy, so even an abandoned practice round resolves
			// with a genuine house move rather than a deterministic forfeit.
			if m.Mode == ModeSandbox && seat == HouseSeat {
				continue
			}
			ns, evs, terr := eng.ForceTimeout(state, seat)
			if terr != nil {
				return terr
			}
			state = ns
			events = append(events, evs...)
		}
	}
	// A racing live move (Act) may have advanced the match first — the forced
	// timeout is then moot, surfaced as ErrConcurrentUpdate by the OCC commit.
	if _, err = s.commit(ctx, m, eng, state, events); err != nil && !errors.Is(err, ErrConcurrentUpdate) {
		return err
	}
	return nil
}

// SweepExpired processes all matches whose move window has lapsed.
func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
	// Post-outage grace: a missed turn forfeits the seat's stake, which is right when
	// an agent quits but wrong when WE were the ones unreachable. During the grace
	// window opened after a detected platform outage we skip the sweep entirely, so
	// deadlines that lapsed while nobody could play are not converted into losses.
	// The matches stay active and are decided on play once agents reconnect.
	//
	// Nil tracker (feature unwired) or no grace ⇒ unchanged behaviour.
	if s.liveness.InGrace() {
		return 0, nil
	}
	ids, err := s.repo.ListActiveExpired(ctx, s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	var swept int
	var errs error
	for _, id := range ids {
		if err := s.HandleTimeout(ctx, id); err != nil {
			// Best-effort: one match that can't be resolved (e.g. a wedged/corrupt
			// state) must not block the timeout of every other expired match.
			// Collect and continue; the sweeper logs the joined error.
			errs = errors.Join(errs, fmt.Errorf("timeout %s: %w", id, err))
			continue
		}
		swept++
	}
	return swept, errs
}

// pollSafetyTick bounds the long-poll wait if a push wake-up is ever missed
// (e.g. a Redis blip). On the happy path the notifier wakes the waiter the moment
// the state changes, so this timer almost never fires — it's a backstop, not the
// mechanism. Far fewer idle reads than the old fixed 400ms poll.
const pollSafetyTick = 2 * time.Second

// State returns the redacted agent view; with wait it long-polls until the state
// advances (opponent acts / round resolves) or the timeout elapses. The wait is
// driven by the Notifier (Redis pub/sub) so it returns on the actual state change
// rather than a fixed timer — low latency without hammering Postgres.
func (s *Service) State(ctx context.Context, matchPublicID, viewerAgentPublicID string, wait bool, timeout time.Duration) (AgentView, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return AgentView{}, ErrNotFound
	}
	if !wait || m.Status == StatusFinished {
		return s.view(m, viewerAgentPublicID), nil
	}
	startSeq := m.State.NextSeq

	// Subscribe BEFORE the recheck below so a change landing between them can't be
	// missed (it either bumps NextSeq on the recheck or delivers on the channel).
	wake, cancel := s.notify.Subscribe(matchPublicID)
	defer cancel()

	deadline := s.clock.Now().Add(timeout)
	for {
		remaining := deadline.Sub(s.clock.Now())
		if remaining <= 0 {
			break
		}
		tick := remaining
		if tick > pollSafetyTick {
			tick = pollSafetyTick
		}
		select {
		case <-ctx.Done():
			return s.view(m, viewerAgentPublicID), nil
		case <-wake: // a real state change was published
		case <-time.After(tick): // backstop in case a wake-up was missed
		}
		m, err = s.repo.Get(ctx, matchPublicID)
		if err != nil {
			return AgentView{}, ErrNotFound
		}
		if m.State.NextSeq != startSeq || m.Status == StatusFinished {
			break
		}
	}
	return s.view(m, viewerAgentPublicID), nil
}

// Replay returns the full event log; the seed is revealed only once finished.
func (s *Service) Replay(ctx context.Context, matchPublicID string) (ReplayDoc, error) {
	m, err := s.repo.Get(ctx, matchPublicID)
	if err != nil {
		return ReplayDoc{}, ErrNotFound
	}
	events, err := s.repo.LoadEvents(ctx, matchPublicID)
	if err != nil {
		return ReplayDoc{}, err
	}
	// WITHHOLD SECRETS UNTIL THE MATCH IS OVER.
	//
	// This document is public and unauthenticated, and it serves every match id —
	// including Mafia's, whose night actions sit in this same event log. Served
	// raw, a single GET on a LIVE match returned every role and every night target
	// ({"actor":"Mafia","seat":9,"secret":"Target → seat 4"}), which is the whole
	// game. The seed below is already gated on StatusFinished for the same reason;
	// the events needed the identical gate and never had it.
	//
	// Once finished, the full log is served as-is: that is the post-match reveal
	// the replay exists for, and the replay hash is computed over the complete log.
	if m.Status != StatusFinished {
		safe := make([]gs.Event, 0, len(events))
		for _, ev := range events {
			if redact.SafeForLive(string(ev.Type), ev.Payload) {
				safe = append(safe, ev)
			}
		}
		events = safe
	}

	doc := ReplayDoc{
		MatchID:       m.PublicID,
		EngineVersion: m.EngineVersion,
		Commit:        m.Commit,
		ReplayHash:    m.ReplayHash,
		Status:        m.Status,
		Events:        events,
		Roster:        RosterOf(m.Players),
	}
	// Pacing side-car: same order as Events, never mixed into them (Events is hashed).
	// Best-effort — a replay without timing is still correct, just unpaced.
	if timed, terr := s.repo.LoadEventsTimed(ctx, matchPublicID); terr == nil && len(timed) > 0 {
		start := timed[0].At
		doc.Timing = make([]EventTiming, 0, len(timed))
		for _, te := range timed {
			doc.Timing = append(doc.Timing, EventTiming{
				Seq: te.Event.Seq, At: te.At.UTC(), OffsetMs: te.At.Sub(start).Milliseconds(),
			})
		}
	}
	if m.Status == StatusFinished {
		doc.Seed = m.Seed // provable-fairness reveal
	}

	// Per-move authenticity: surface the signatures and re-verify them so the
	// reader gets a server-checked verdict AND the raw proofs to re-check offline.
	if proofs, perr := s.repo.LoadMoveSignatures(ctx, matchPublicID); perr == nil && len(proofs) > 0 {
		doc.MoveProofs = proofs
		allValid := true
		for _, mp := range proofs {
			if !movesig.Verify(mp.Pubkey, matchPublicID, mp.Round, mp.Seat, mp.Card, mp.Signature) {
				allValid = false
				break
			}
		}
		doc.MovesVerified = &allValid
	}
	return doc, nil
}

func mapEngineErr(err error) error {
	switch {
	case errors.Is(err, gs.ErrIllegalCard):
		return errIllegalCard("that card is not in your hand")
	case errors.Is(err, gs.ErrFinished):
		return ErrNotActive
	default:
		return errIllegalCard(err.Error())
	}
}

// finalizePlayers computes per-seat final score + (display) coin delta.
func finalizePlayers(players []Player, state gs.State, pool, bid int64, rakePct int) []Player {
	rake := pool * int64(rakePct) / 100
	out := make([]Player, len(players))
	copy(out, players)
	for i := range out {
		seat := out[i].Seat
		out[i].FinalScore = state.Scores[seat]
		switch {
		case state.Winner == gs.Tie:
			out[i].CoinsDelta = 0
		case state.Winner == seat:
			out[i].CoinsDelta = (pool - rake) - bid
		default:
			out[i].CoinsDelta = -bid
		}
	}
	return out
}

// SetRakeSource wires the live platform commission (Super Admin → config bus) into
// new-match pricing. Nil, or a value outside 0..50%, falls back to the static config
// — the bound guards against a corrupt or hostile publisher setting a 100% rake and
// taking the entire pot.
func (s *Service) SetRakeSource(f func() int) { s.rake = f }

// rakePct resolves the commission for a match about to be created.
func (s *Service) rakePct() int {
	if s.rake != nil {
		if p := s.rake(); p >= 0 && p <= 50 {
			return p
		}
	}
	return s.cfg.RakePct
}
