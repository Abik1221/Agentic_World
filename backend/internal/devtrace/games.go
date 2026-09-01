package devtrace

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

)

// PER-GAME EVENT MAPPING — turning the match log into sentences.
//
// Two problems were being solved badly here, and both were invisible until you looked
// at a real developer's page.
//
// ONE: only Goofspiel existed. The mapper handled prize_revealed / card_sealed /
// round_revealed / agent_says, and the SQL allowlist admitted only those. Mafia emits
// phase|moderator|night|message|vote|eliminate|victory and Monopoly emits
// turn_started|dice_rolled|moved|…|match_finished — so a Mafia match contributed ZERO
// rows and a Monopoly match contributed two bare lines. A developer with a hundred
// Mafia matches saw a page that looked broken, and reasonably concluded the platform
// had lost their games.
//
// TWO: what did render was numbers. `{"target": 4}`, `{"property": 39}`,
// `{"seat": 2, "delta": -350}`. The developer had to hold the board layout and the
// seating chart in their head to read their own agent's history. So every entry now
// carries a `summary`: one finished sentence, composed HERE where the seat names, the
// board and the payload shapes are all in scope. The client renders prose it is given
// rather than inventing prose from integers.
//
// WHAT IS DELIBERATELY NOT MAPPED — `night`. Mafia's night payload carries the
// mafia's chosen target (and a `secret` field). It is excluded from the SQL allowlist
// entirely, not merely skipped here, because during an ACTIVE match that is the one
// fact a living player must not be able to read. Two gates, same as the rest of this
// package: a mistake in one mapper must not be able to leak it.
//
// OTHER SEATS are admitted only for a FINISHED match (see mapOptions.Finished). Their
// votes, table talk and eliminations are public game facts — a Mafia timeline without
// them is unreadable, since "why was I voted out" is the whole question — but a live
// match must not become a live scoreboard of everyone else's play.

// mapOptions carries the per-match context the mapper needs to write sentences.
type mapOptions struct {
	// SeatName resolves a seat to a display name in THIS match. Nil, or a miss,
	// degrades to "seat N" rather than dropping the event.
	SeatName func(seat int) string
	// Finished admits other seats' public events. False while a match is live.
	Finished bool
}

func (o mapOptions) seat(n int) string {
	if o.SeatName != nil {
		if name := o.SeatName(n); name != "" {
			return fmt.Sprintf("%s (seat %d)", name, n)
		}
	}
	return fmt.Sprintf("seat %d", n)
}

// Entry types the mapper produces beyond the existing agent_* set.
//
//   - match_note is a public fact about the match that the agent did not choose: a
//     phase change, a dice roll, rent falling due. Separated from agent_decision on
//     purpose — "what my agent chose" and "what happened to my agent" are different
//     questions, and a timeline that mixes them makes the first one unanswerable.
const (
	TypeDecision   = "agent_decision"
	TypeSaid       = "agent_said"
	TypeNote       = "match_note"
	TypeStarted    = "match_started"
	TypeFinished   = "match_finished"
	TypeEliminated = "agent_eliminated"
)

// mapRows turns raw match_event rows into timeline entries, newest first.
//
// Each row arrives already attributed to one of the caller's agents and to that
// agent's seat (see matchActivityQuery). meta supplies per-match context; it may
// return the zero value for a match we know nothing extra about.
func mapRows(rows []MatchRow, meta func(matchPublicID string) mapOptions) []Entry {
	lat := goofspielLatency(rows)
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		opts := mapOptions{}
		if meta != nil {
			opts = meta(r.MatchPublicID)
		}
		base := Entry{
			At:      r.CreatedAt,
			Status:  "ok",
			Game:    r.Game,
			MatchID: r.MatchPublicID,
			AgentID: r.AgentPublicID,
		}
		switch r.Game {
		case "mafia":
			out = append(out, mapMafia(base, r, opts)...)
		default: // goofspiel, and anything sharing its log shape
			out = append(out, mapGoofspiel(base, r, opts, lat)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// ── Goofspiel ────────────────────────────────────────────────────────────────

type roundKey struct {
	match string
	agent string
	round int
}

// goofspielLatency pairs each round's prize reveal with this agent sealing its card.
// The gap is real thinking time — a better measure than the sampled timings table,
// whose match_id was never populated. Only Goofspiel's log supports it: Mafia and
// Monopoly have no per-decision start marker, so their entries carry no latency
// rather than a fabricated one (the per-match average comes from the benchmark row).
func goofspielLatency(rows []MatchRow) map[roundKey]int64 {
	prizeAt := map[roundKey]time.Time{}
	sealedAt := map[roundKey]time.Time{}
	for _, r := range rows {
		switch r.Type {
		case "prize_revealed":
			var p struct{ Round int }
			if json.Unmarshal(r.Payload, &p) == nil {
				k := roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}
				// Keep the FIRST reveal: a retry or duplicate row must not move the
				// start of the clock and make a decision look instant.
				if _, seen := prizeAt[k]; !seen {
					prizeAt[k] = r.CreatedAt
				}
			}
		case "card_sealed":
			var p struct {
				Round int
				Seat  int
			}
			if json.Unmarshal(r.Payload, &p) == nil && p.Seat == r.Seat {
				sealedAt[roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}] = r.CreatedAt
			}
		}
	}
	out := make(map[roundKey]int64, len(sealedAt))
	for k, end := range sealedAt {
		start, ok := prizeAt[k]
		if !ok {
			continue
		}
		ms := end.Sub(start).Milliseconds()
		// A negative or absurd gap means these two rows are not the pair we think they
		// are; reporting it would put a fictional number on a latency chart.
		if ms < 0 || ms > int64(6*time.Hour/time.Millisecond) {
			continue
		}
		out[k] = ms
	}
	return out
}

func mapGoofspiel(base Entry, r MatchRow, opts mapOptions, lat map[roundKey]int64) []Entry {
	switch r.Type {
	case "round_revealed":
		// The decision WITH its value. card_sealed deliberately carries no card (a
		// spectator must not learn a move early), so the reveal is where a developer
		// finally sees what their agent actually played.
		var p struct {
			Round  int    `json:"round"`
			Prize  int    `json:"prize"`
			Cards  [2]int `json:"cards"`
			Winner int    `json:"winner"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || r.Seat < 0 || r.Seat > 1 {
			return nil
		}
		won := p.Winner == r.Seat
		e := base
		e.Type = TypeDecision
		e.Operation = "bid"
		e.LatencyMS = lat[roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}]
		verdict := "lost it"
		if won {
			verdict = "won it"
		}
		e.Detail = map[string]any{
			"round":   p.Round,
			"action":  fmt.Sprintf("played %d", p.Cards[r.Seat]),
			"prize":   p.Prize,
			"won":     won,
			"by_me":   true,
			"summary": fmt.Sprintf("Round %d: bid %d for the %d-point prize and %s (opponent bid %d)", p.Round, p.Cards[r.Seat], p.Prize, verdict, p.Cards[1-r.Seat]),
		}
		return []Entry{e}

	case "agent_says":
		var p struct {
			Round int    `json:"round"`
			Seat  int    `json:"seat"`
			Text  string `json:"text"`
			Kind  string `json:"kind"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		if p.Seat != r.Seat && !opts.Finished {
			return nil
		}
		e := base
		e.Type = TypeSaid
		e.Detail = map[string]any{"round": p.Round, "by_me": p.Seat == r.Seat}
		// A rationale is the agent explaining its own move; table talk is what it said
		// to the opponent. The client renders them differently, so they stay distinct.
		if p.Kind == "rationale" {
			e.Detail["rationale"] = p.Text
			e.Detail["summary"] = fmt.Sprintf("Round %d reasoning: %s", p.Round, p.Text)
		} else {
			e.Detail["text"] = p.Text
			e.Detail["summary"] = fmt.Sprintf("%s said: %s", opts.seat(p.Seat), p.Text)
		}
		return []Entry{e}

	case "match_finished":
		var p struct {
			Scores [2]int `json:"scores"`
			Winner int    `json:"winner"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		e := base
		e.Type = TypeFinished
		won := p.Winner == r.Seat
		e.Detail = map[string]any{"won": won}
		if r.Seat >= 0 && r.Seat <= 1 {
			e.Detail["score"] = p.Scores[r.Seat]
			e.Detail["summary"] = fmt.Sprintf("Match finished %d–%d — %s",
				p.Scores[r.Seat], p.Scores[1-r.Seat], outcomeWord(won, p.Winner < 0))
		}
		return []Entry{e}

	case "match_created":
		e := base
		e.Type = TypeStarted
		e.Detail = map[string]any{"summary": "Match started"}
		return []Entry{e}
	}
	return nil
}

// ── Mafia ────────────────────────────────────────────────────────────────────

func mapMafia(base Entry, r MatchRow, opts mapOptions) []Entry {
	switch r.Type {
	case "vote":
		var p struct {
			From   int `json:"from"`
			Target int `json:"target"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		mine := p.From == r.Seat
		if !mine && !opts.Finished {
			return nil
		}
		e := base
		if mine {
			e.Type = TypeDecision
			e.Operation = "vote"
		} else {
			e.Type = TypeNote
		}
		e.Detail = map[string]any{
			"by_me":   mine,
			"action":  fmt.Sprintf("voted %s", opts.seat(p.Target)),
			"target":  p.Target,
			"summary": fmt.Sprintf("%s voted to eliminate %s", opts.seat(p.From), opts.seat(p.Target)),
		}
		return []Entry{e}

	case "message":
		var p struct {
			From   int    `json:"from"`
			Tone   string `json:"tone"`
			Text   string `json:"text"`
			Target *int   `json:"target"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		mine := p.From == r.Seat
		if !mine && !opts.Finished {
			return nil
		}
		e := base
		e.Type = TypeSaid
		at := ""
		if p.Target != nil {
			at = " → " + opts.seat(*p.Target)
		}
		e.Detail = map[string]any{
			"by_me": mine, "text": p.Text, "tone": p.Tone,
			"summary": fmt.Sprintf("%s (%s)%s: %s", opts.seat(p.From), toneWord(p.Tone), at, p.Text),
		}
		return []Entry{e}

	case "eliminate":
		var p struct {
			Target int    `json:"target"`
			Cause  string `json:"cause"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		mine := p.Target == r.Seat
		if !mine && !opts.Finished {
			return nil
		}
		e := base
		e.Type = TypeNote
		if mine {
			// Its own death is the single most important line in a Mafia timeline: it
			// is where the agent stops being able to act at all.
			e.Type = TypeEliminated
		}
		by := "the town's vote"
		if p.Cause == "mafia" {
			by = "the mafia"
		}
		who := opts.seat(p.Target)
		if mine {
			who = "Your agent"
		}
		e.Detail = map[string]any{
			"by_me": mine, "cause": p.Cause, "target": p.Target,
			"summary": fmt.Sprintf("%s was eliminated by %s", who, by),
		}
		return []Entry{e}

	case "phase":
		var p struct {
			Day   int    `json:"day"`
			Phase string `json:"phase"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		e := base
		e.Type = TypeNote
		e.Detail = map[string]any{
			"round": p.Day, "phase": p.Phase, "by_me": false,
			"summary": fmt.Sprintf("Day %d — %s", p.Day, phaseWord(p.Phase)),
		}
		return []Entry{e}

	case "moderator":
		var p struct{ Text string }
		if json.Unmarshal(r.Payload, &p) != nil || p.Text == "" {
			return nil
		}
		e := base
		e.Type = TypeNote
		e.Detail = map[string]any{"by_me": false, "summary": p.Text}
		return []Entry{e}

	case "victory":
		var p struct {
			Team string `json:"team"`
			Text string `json:"text"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return nil
		}
		e := base
		e.Type = TypeFinished
		// Which team the agent was on is NOT in this payload, and guessing it from a
		// win/loss the caller has not been told would be inventing data. The roster on
		// the detail endpoint carries the role; this line reports the fact only.
		e.Detail = map[string]any{
			"team": p.Team, "by_me": false,
			"summary": fmt.Sprintf("Match finished — %s win. %s", p.Team, p.Text),
		}
		return []Entry{e}

	case "match_created":
		e := base
		e.Type = TypeStarted
		e.Detail = map[string]any{"summary": "Match started"}
		return []Entry{e}

	case "match_finished":
		e := base
		e.Type = TypeFinished
		e.Detail = map[string]any{"summary": "Match finished"}
		return []Entry{e}
	}
	return nil
}

// ── Wording helpers ──────────────────────────────────────────────────────────
//
// Small on purpose. Each one exists because the raw enum value is a token, not
// English, and the whole reason this file exists is that the developer was being
// handed tokens.

func outcomeWord(won, tie bool) string {
	switch {
	case tie:
		return "a draw"
	case won:
		return "your agent won"
	default:
		return "your agent lost"
	}
}

func toneWord(t string) string {
	switch t {
	case "accuse":
		return "accusing"
	case "defend":
		return "defending"
	case "claim":
		return "claiming a role"
	case "alliance":
		return "proposing an alliance"
	case "info":
		return "sharing information"
	case "":
		return "talking"
	default:
		return t
	}
}

func phaseWord(p string) string {
	switch p {
	case "night":
		return "night falls"
	case "morning":
		return "morning"
	case "discussion":
		return "discussion opens"
	case "voting":
		return "voting opens"
	case "result":
		return "the result is read"
	case "":
		return "phase change"
	default:
		return p
	}
}

