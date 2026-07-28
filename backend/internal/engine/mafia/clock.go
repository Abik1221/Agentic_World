package mafia

import (
	"strings"
	"time"
)

// Phase pacing — the table's clock, modelled on how a moderated Mafia game is
// actually run rather than one flat timer for every phase.
//
// Night is short: only the mafia, doctor and detective act, they act in secret and
// in parallel, and nobody is waiting on debate. Discussion is the long one — it is
// the whole game, where agents accuse, defend, bluff and build cases. Voting is
// deliberately tighter than discussion: the arguing is over, this is the show of
// hands. Morning and result are announcement beats, not decision windows, so they
// pass quickly.
//
// This is the SINGLE source of truth for phase length: the engine stamps it onto
// every phase event (so spectators can run the same countdown) and the match
// service derives the enforcement deadline from it. If the two ever disagreed, a
// watcher's clock would hit zero while the table was still accepting actions.
const (
	NightDuration      = 30 * time.Second
	MorningDuration    = 8 * time.Second
	DiscussionDuration = 75 * time.Second
	VotingDuration     = 30 * time.Second
	ResultDuration     = 8 * time.Second
)

// PhaseDuration is how long `phase` runs. An unknown phase falls back to the
// discussion window, the most forgiving of the three decision phases.
func PhaseDuration(phase string) time.Duration {
	switch phase {
	case PhaseNight:
		return NightDuration
	case PhaseMorning:
		return MorningDuration
	case PhaseDiscussion:
		return DiscussionDuration
	case PhaseVoting:
		return VotingDuration
	case PhaseResult:
		return ResultDuration
	default:
		return DiscussionDuration
	}
}

// CanSpeak reports whether table talk is allowed in `phase`.
//
// Silence at night is a RULE, not a UI affordance: the town is asleep, so nobody
// may speak, and the engine already refuses a `message` action outside discussion.
// This helper exists so the service and the view can state the same rule to agents
// and spectators ahead of time, instead of them discovering it via a rejection.
func CanSpeak(phase string) bool { return phase == PhaseDiscussion }

// MaxSayLen caps one spoken line. Mafia is won and lost on what gets said, so the
// limit is generous — but not unbounded, or one agent could bury the table.
const MaxSayLen = 600

// Say is free-form table talk during discussion: an agent may speak as often as it
// likes, in any order, without waiting for a turn. This is the heart of Mafia — a
// single scripted statement per seat is a roll-call, not a debate, and an agent
// that cannot respond to an accusation cannot defend itself.
//
// Two rules it deliberately keeps:
//   - Silence outside discussion. The town is asleep at night, the ballot is closed
//     during voting; speech is only legal while the floor is open.
//   - Talking never advances the phase. It does NOT touch the Messages counter or
//     the per-seat action slot, so chatter cannot rush discussion to a close (nor
//     stall it — the phase clock still ends it). Only the seat's one formal
//     statement, or the timer, closes the floor.
//
// The line is emitted as a normal `message` event, so every spectator and every
// agent's public transcript picks it up through the path they already read.
func (e *Engine) Say(s State, seat int, text, tone string, target int) (State, []Event, error) {
	if s.Finished {
		return s, nil, ErrFinished
	}
	if !s.Alive[seat] {
		return s, nil, ErrNotAlive
	}
	if !CanSpeak(s.Phase) {
		return s, nil, ErrIllegalAction // night / voting / result: the floor is closed
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return s, nil, ErrIllegalAction
	}
	if len(text) > MaxSayLen {
		text = strings.TrimSpace(text[:MaxSayLen])
	}
	ns := s.clone()
	payload := MessagePayload{From: seat, Tone: tone, Text: text}
	// A target only makes sense when it names a living seat other than the speaker.
	if target > 0 && target != seat && ns.Alive[target] {
		t := target
		payload.Target = &t
	}
	ev := e.emit(&ns, EvMessage, payload)
	return ns, []Event{ev}, nil
}

// phasePayload builds a phase event with its duration already stamped on, so no
// emit site can forget the clock.
func phasePayload(day int, phase string) PhasePayload {
	return PhasePayload{Day: day, Phase: phase, DurationMs: PhaseDuration(phase).Milliseconds()}
}
