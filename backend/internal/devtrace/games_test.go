package devtrace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The mapper is where a developer's history is either readable or a pile of integers, and
// where the visibility rules for other seats are enforced a second time. Both are tested
// here; the first because it silently degraded before (two of three games mapped to
// nothing at all), the second because getting it wrong leaks another developer's play.

func row(game, typ string, seat int, payload any, at time.Time) MatchRow {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return MatchRow{
		MatchPublicID: "mt_1", Game: game, AgentPublicID: "ag_me",
		Seat: seat, Type: typ, Payload: b, CreatedAt: at,
	}
}

func finishedMeta(names map[int]string) func(string) mapOptions {
	return func(string) mapOptions {
		return mapOptions{SeatName: func(n int) string { return names[n] }, Finished: true}
	}
}

// Mafia contributed ZERO entries before this: the mapper only knew Goofspiel's event
// kinds, so a developer with a hundred Mafia matches saw an empty page and reasonably
// concluded the platform had lost their games.
func TestMapRows_MafiaProducesAnAgentTimeline(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []MatchRow{
		row("mafia", "phase", 3, map[string]any{"day": 1, "phase": "voting"}, t0),
		row("mafia", "message", 3, map[string]any{"from": 3, "tone": "accuse", "text": "seat 5 is quiet", "target": 5}, t0.Add(time.Second)),
		row("mafia", "vote", 3, map[string]any{"from": 3, "target": 5}, t0.Add(2*time.Second)),
		row("mafia", "eliminate", 3, map[string]any{"target": 3, "cause": "mafia"}, t0.Add(3*time.Second)),
		row("mafia", "victory", 3, map[string]any{"team": "mafia", "text": "The mafia take the town."}, t0.Add(4*time.Second)),
	}
	got := mapRows(rows, finishedMeta(map[int]string{3: "MY_AGENT", 5: "ORACLE_v9"}))
	if len(got) != len(rows) {
		t.Fatalf("mapped %d of %d rows — a dropped kind is an invisible gap in the timeline", len(got), len(rows))
	}

	byType := map[string]Entry{}
	for _, e := range got {
		byType[e.Type] = e
		if summary(e) == "" {
			t.Errorf("%s carries no summary — the client would fall back to a bare label", e.Type)
		}
	}
	// Its own vote is a DECISION; the phase change is not. That distinction is what makes
	// "show me only what my agent chose" answerable.
	if _, ok := byType[TypeDecision]; !ok {
		t.Error("the agent's own vote did not map to a decision")
	}
	if _, ok := byType[TypeNote]; !ok {
		t.Error("the phase change did not map to a note")
	}
	// Death is the most important line in a Mafia timeline: it is where the agent stops
	// being able to act at all.
	if _, ok := byType[TypeEliminated]; !ok {
		t.Error("being eliminated did not get its own entry type")
	}

	// Seat numbers must be resolved to names, or the developer is left holding the
	// seating chart in their head — the exact complaint this mapper answers.
	if s := summary(byType[TypeDecision]); !strings.Contains(s, "ORACLE_v9") {
		t.Errorf("vote summary %q does not name the target", s)
	}
}

// Monopoly rendered as two bare lines before this, and its payloads are almost all board
// indices — the case where raw numbers are least readable.

// Goofspiel is the one game with a real per-decision clock: the gap between the prize
// being revealed and this agent sealing its card.
func TestMapRows_GoofspielLatencyAndOutcome(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []MatchRow{
		row("goofspiel", "prize_revealed", 0, map[string]any{"round": 1, "prize": 11}, t0),
		row("goofspiel", "card_sealed", 0, map[string]any{"round": 1, "seat": 0}, t0.Add(1200*time.Millisecond)),
		row("goofspiel", "round_revealed", 0, map[string]any{"round": 1, "prize": 11, "cards": []int{9, 4}, "winner": 0}, t0.Add(1300*time.Millisecond)),
	}
	got := mapRows(rows, nil)

	var dec *Entry
	for i := range got {
		if got[i].Type == TypeDecision {
			dec = &got[i]
		}
	}
	if dec == nil {
		t.Fatal("no decision entry")
	}
	if dec.LatencyMS != 1200 {
		t.Errorf("latency = %d ms, want 1200 (reveal → seal)", dec.LatencyMS)
	}
	if s := summary(*dec); !strings.Contains(s, "bid 9") || !strings.Contains(s, "won it") {
		t.Errorf("summary %q should state the bid and the outcome", s)
	}
}

// A duplicate or retried prize reveal must not move the start of the clock and make a
// decision look instant.
func TestGoofspielLatency_IgnoresDuplicateReveals(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []MatchRow{
		row("goofspiel", "prize_revealed", 0, map[string]any{"round": 1, "prize": 11}, t0),
		row("goofspiel", "prize_revealed", 0, map[string]any{"round": 1, "prize": 11}, t0.Add(2*time.Second)),
		row("goofspiel", "card_sealed", 0, map[string]any{"round": 1, "seat": 0}, t0.Add(3*time.Second)),
	}
	lat := goofspielLatency(rows)
	if got := lat[roundKey{"mt_1", "ag_me", 1}]; got != 3000 {
		t.Errorf("latency = %d, want 3000 (measured from the FIRST reveal)", got)
	}
}

// An absurd or negative gap means the two rows are not the pair we think they are.
// Reporting it would put a fictional number on a latency chart.
func TestGoofspielLatency_RejectsImpossibleGaps(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		seal time.Duration
	}{
		{"negative", -time.Second},
		{"absurd", 7 * time.Hour},
	} {
		rows := []MatchRow{
			row("goofspiel", "prize_revealed", 0, map[string]any{"round": 1}, t0),
			row("goofspiel", "card_sealed", 0, map[string]any{"round": 1, "seat": 0}, t0.Add(tc.seal)),
		}
		if got := goofspielLatency(rows); len(got) != 0 {
			t.Errorf("%s gap produced a latency of %v, want none", tc.name, got)
		}
	}
}

// VISIBILITY — the rule that matters most. While a match is LIVE, other seats' events
// must not come back: a Mafia player reading the vote tally and the table talk of every
// other seat mid-game is cheating, and this mapper is the second of the two gates.
func TestMapRows_LiveMatchHidesOtherSeats(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []MatchRow{
		row("mafia", "vote", 3, map[string]any{"from": 3, "target": 5}, t0),        // mine
		row("mafia", "vote", 3, map[string]any{"from": 7, "target": 3}, t0.Add(1)), // someone else's
		row("mafia", "message", 3, map[string]any{"from": 8, "text": "trust me"}, t0.Add(2)),
		row("mafia", "eliminate", 3, map[string]any{"target": 9, "cause": "vote"}, t0.Add(3)),
	}

	live := mapRows(rows, func(string) mapOptions {
		return mapOptions{Finished: false}
	})
	for _, e := range live {
		if b, ok := e.Detail["by_me"].(bool); ok && !b {
			t.Errorf("a live match returned another seat's event: %s / %q", e.Type, summary(e))
		}
	}
	if len(live) != 1 {
		t.Fatalf("live match returned %d entries, want only the agent's own vote", len(live))
	}

	// Once finished, the same rows ARE admitted: a Mafia timeline without the votes that
	// removed you is unreadable, and by then nothing is hidden from anyone.
	done := mapRows(rows, finishedMeta(nil))
	if len(done) != len(rows) {
		t.Errorf("finished match returned %d of %d rows — public events should be admitted once it is over", len(done), len(rows))
	}
}

// A phase or moderator line carries no seat and belongs to the whole table, so it must
// survive the live-match filter — it is the context that makes the agent's own moves
// legible, and hiding it would leave a timeline of votes with no days in it.
func TestMapRows_PublicNarrationSurvivesTheLiveFilter(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []MatchRow{
		row("mafia", "phase", 3, map[string]any{"day": 2, "phase": "night"}, t0),
		row("mafia", "moderator", 3, map[string]any{"text": "The town gathers."}, t0.Add(1)),
	}
	got := mapRows(rows, func(string) mapOptions { return mapOptions{Finished: false} })
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 — narration is table-wide, not another seat's private play", len(got))
	}
}

// Malformed or unknown payloads must be skipped, never panic: the log is append-only and
// spans engine versions, so a row this build does not understand is expected.
func TestMapRows_SurvivesGarbage(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []MatchRow{
		{MatchPublicID: "mt_1", Game: "mafia", AgentPublicID: "ag_me", Seat: 1, Type: "vote", Payload: json.RawMessage(`not json`), CreatedAt: t0},
		{MatchPublicID: "mt_1", Game: "monopoly", AgentPublicID: "ag_me", Seat: 1, Type: "moved", Payload: json.RawMessage(`{"seat":"one"}`), CreatedAt: t0},
		{MatchPublicID: "mt_1", Game: "goofspiel", AgentPublicID: "ag_me", Seat: 1, Type: "who_knows", Payload: json.RawMessage(`{}`), CreatedAt: t0},
		{MatchPublicID: "mt_1", Game: "brand_new_game", AgentPublicID: "ag_me", Seat: 1, Type: "match_created", Payload: json.RawMessage(`{}`), CreatedAt: t0},
	}
	got := mapRows(rows, nil)
	// The last row is a known kind under an unknown game, which falls through to the
	// Goofspiel shape and is a legitimate "match started".
	if len(got) != 1 || got[0].Type != TypeStarted {
		t.Fatalf("got %d entries (%+v), want just the one valid match_started", len(got), got)
	}
}

// Entries come back newest-first, which is what every caller assumes.
func TestMapRows_NewestFirst(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []MatchRow{
		row("mafia", "moderator", 0, map[string]any{"text": "first"}, t0),
		row("mafia", "moderator", 0, map[string]any{"text": "second"}, t0.Add(time.Minute)),
	}
	got := mapRows(rows, nil)
	if len(got) != 2 || !strings.Contains(summary(got[0]), "second") {
		t.Fatalf("not newest-first: %+v", got)
	}
}

// summary reads the sentence the mapper composed. Mirrors what the client does.
func summary(e Entry) string {
	if s, ok := e.Detail["summary"].(string); ok {
		return s
	}
	return ""
}

func TestValidMatchMode(t *testing.T) {
	for in, want := range map[string]MatchMode{
		"":            ModeAny,
		"all":         ModeAny,
		"sandbox":     ModeSandbox,
		"competitive": ModeCompetitive,
		"ranked":      ModeCompetitive,
		"real":        ModeCompetitive,
	} {
		got, ok := ValidMatchMode(in)
		if !ok || got != want {
			t.Errorf("ValidMatchMode(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	// A typo must NOT widen to "all". Quietly returning real-money matches under a
	// sandbox heading is the one failure this filter cannot have.
	for _, bad := range []string{"sandbx", "SANDBOX", "competitive ", "'; DROP", "practice"} {
		if _, ok := ValidMatchMode(bad); ok {
			t.Errorf("ValidMatchMode(%q) accepted an unknown mode", bad)
		}
	}
}
