package skill

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

type fakeSource struct {
	queue  []Unscored
	saved  []Scored
	calls  int
	failOn int // 1-based pass number to fail on; 0 never fails
}

func (f *fakeSource) NextUnscored(_ context.Context, limit int) ([]Unscored, error) {
	f.calls++
	if f.failOn == f.calls {
		return nil, errors.New("database is having a moment")
	}
	if len(f.queue) == 0 {
		return nil, nil
	}
	if limit > len(f.queue) {
		limit = len(f.queue)
	}
	out := f.queue[:limit]
	f.queue = f.queue[limit:]
	return out, nil
}

func (f *fakeSource) SaveScores(_ context.Context, s []Scored) error {
	f.saved = append(f.saved, s...)
	return nil
}

const scorableView = `{
  "game":"goofspiel","round":2,"current_prize":9,"prize_pool":9,
  "your_hand":[2,5,9,12],"legal_actions":[2,5,9,12],
  "history":[{"round":1,"prize":4,"prize_pool":4,"your_card":1,"opp_card":7,"winner":1}]
}`

func quietWorker(src Source) *Worker {
	return NewWorker(src, WorkerConfig{Batch: 10}, slog.New(slog.DiscardHandler))
}

func TestWorkerScoresAndPersists(t *testing.T) {
	src := &fakeSource{queue: []Unscored{
		{MatchID: "m1", AgentID: 7, Seq: 1, Game: "goofspiel", Action: "12", Input: []byte(scorableView)},
	}}
	n, err := quietWorker(src).Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if n != 1 {
		t.Fatalf("scored %d want 1", n)
	}
	if len(src.saved) != 1 {
		t.Fatalf("persisted %d verdicts want 1", len(src.saved))
	}
	got := src.saved[0]
	if got.Regret == nil {
		t.Fatal("a scorable decision was persisted with a NULL regret")
	}
	if *got.Regret < 0 || *got.Regret > 1 {
		t.Fatalf("regret %.4f outside [0,1]", *got.Regret)
	}
	if got.Best == "" {
		t.Error("no best action recorded; the trace UI cannot say what the agent should have done")
	}
	if got.MatchID != "m1" || got.AgentID != 7 || got.Seq != 1 {
		t.Fatalf("verdict written against the wrong row: %+v", got)
	}
}

// THE correctness property for the whole pipeline.
//
// A decision that cannot be scored must persist a NULL regret, not a zero. Zero regret
// means "played the best available move". Writing that for a decision nobody could score
// would hand a perfect record to every agent in an arena that has no scorer — Monopoly,
// today — and the P-Index average would silently include invented perfection.
func TestUnscorableDecisionsPersistNullNotZero(t *testing.T) {
	src := &fakeSource{queue: []Unscored{
		{MatchID: "m1", AgentID: 1, Seq: 0, Game: "monopoly", Action: "buy", Input: []byte(`{"game":"monopoly"}`)},
		{MatchID: "m1", AgentID: 1, Seq: 1, Game: "goofspiel", Action: "not-a-number", Input: []byte(scorableView)},
		{MatchID: "m1", AgentID: 1, Seq: 2, Game: "goofspiel", Action: "12", Input: []byte(`{ broken`)},
		{MatchID: "m1", AgentID: 1, Seq: 3, Game: "goofspiel", Action: "99", Input: []byte(scorableView)}, // not in hand
	}}
	n, err := quietWorker(src).Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if n != 0 {
		t.Fatalf("scored %d of 4 unscorable decisions", n)
	}
	if len(src.saved) != 4 {
		t.Fatalf("persisted %d rows want 4 — unscorable rows must still be stamped", len(src.saved))
	}
	for _, s := range src.saved {
		if s.Regret != nil {
			t.Fatalf("seq %d persisted regret %.4f; an unscorable decision must be NULL, or "+
				"an arena with no scorer reads as a table of perfect agents", s.Seq, *s.Regret)
		}
	}
}

// An unscorable row must still be STAMPED, or the worker re-reads the same rows on every
// pass forever and never reaches the rest of the backlog. The stamp is applied by
// SaveScores writing the row at all — so the test is that every input produces an output.
func TestEveryFetchedRowIsWrittenBack(t *testing.T) {
	src := &fakeSource{queue: []Unscored{
		{MatchID: "m", AgentID: 1, Seq: 0, Game: "goofspiel", Action: "12", Input: []byte(scorableView)},
		{MatchID: "m", AgentID: 1, Seq: 1, Game: "chess", Action: "e4", Input: []byte(`{}`)},
	}}
	if _, err := quietWorker(src).Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(src.saved) != 2 {
		t.Fatalf("wrote back %d of 2 fetched rows — the unwritten one would be re-fetched "+
			"on every pass and the backlog would never drain", len(src.saved))
	}
}

// An empty queue is not an error and must not write anything.
func TestIdlePassIsANoOp(t *testing.T) {
	src := &fakeSource{}
	n, err := quietWorker(src).Once(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("idle pass: n=%d err=%v", n, err)
	}
	if len(src.saved) != 0 {
		t.Fatal("an idle pass wrote verdicts")
	}
}

// A read failure must surface, not be swallowed into "nothing to do" — Run distinguishes
// the two to decide whether to back off.
func TestReadFailureSurfaces(t *testing.T) {
	src := &fakeSource{failOn: 1}
	if _, err := quietWorker(src).Once(context.Background()); err == nil {
		t.Fatal("a failing read reported success")
	}
}

// Dispatch must refuse games it has no scorer for rather than inventing a verdict.
func TestScoreDecisionRefusesUnknownGames(t *testing.T) {
	for _, game := range []string{"monopoly", "mafia", "chess", ""} {
		if _, ok := ScoreDecision(game, []byte(scorableView), "12"); ok {
			t.Errorf("%q was scored by the Goofspiel scorer", game)
		}
	}
	if _, ok := ScoreDecision("goofspiel", []byte(scorableView), "12"); !ok {
		t.Error("goofspiel was refused")
	}
}

// Actions are parsed strictly: an action we cannot read exactly is one we must not score.
func TestActionParsingIsStrict(t *testing.T) {
	for _, bad := range []string{"", " 12", "12 ", "12a", "-3", "1.0", "twelve"} {
		if _, err := atoiStrict(bad); err == nil {
			t.Errorf("%q parsed as a valid action", bad)
		}
	}
	if n, err := atoiStrict("13"); err != nil || n != 13 {
		t.Fatalf("atoiStrict(\"13\") = %d, %v", n, err)
	}
}

// Scoring the same decision twice must produce the same verdict — that is what makes
// concurrent workers safe without locking, and what makes a recompute trustworthy.
func TestWorkerVerdictsAreReproducible(t *testing.T) {
	row := Unscored{MatchID: "m", AgentID: 1, Seq: 0, Game: "goofspiel", Action: "9", Input: []byte(scorableView)}
	var first float64
	for i := 0; i < 20; i++ {
		src := &fakeSource{queue: []Unscored{row}}
		if _, err := quietWorker(src).Once(context.Background()); err != nil {
			t.Fatalf("Once: %v", err)
		}
		got := *src.saved[0].Regret
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d scored %.12f, first run scored %.12f — two workers racing on the "+
				"same row would write different verdicts", i, got, first)
		}
	}
}
