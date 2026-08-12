package readycheck

import (
	"testing"
	"time"
)

// These tests are about who loses money and who does not.
//
// The rule the whole package exists to enforce: nothing is escrowed until every seat has
// said it is ready. So the cases that matter most are the ones where a seat is DROPPED —
// each of those is someone who keeps their coins because the table never started — and the
// one case where Start is returned, because that is the only moment escrow is allowed.

var t0 = time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

func seat(id string, ready bool, asks int, askedAgo time.Duration) Seat {
	return Seat{AgentPublicID: id, Ready: ready, Asks: asks, AskedAt: t0.Add(-askedAgo)}
}

func TestStartOnlyWhenEveryLiveSeatIsReady(t *testing.T) {
	p := DefaultPolicy("goofspiel")
	d := Evaluate(p, []Seat{seat("a", true, 1, time.Second), seat("b", true, 1, time.Second)}, t0)
	if d.Action != Start {
		t.Fatalf("both ready should Start, got %v", d.Action)
	}
	if d.StartsIn != p.Countdown {
		t.Errorf("countdown = %v, want the policy's %v", d.StartsIn, p.Countdown)
	}
}

func TestOneSeatReadyIsNotAStart(t *testing.T) {
	// The money rule. If this ever returns Start, the caller escrows a stake for a seat
	// that never answered — exactly what CreatePaired does today and what this replaces.
	d := Evaluate(DefaultPolicy("goofspiel"),
		[]Seat{seat("a", true, 1, time.Second), seat("b", false, 1, time.Second)}, t0)
	if d.Action == Start {
		t.Fatal("started with a seat that has not acknowledged — its owner would be escrowed for a match it never agreed to play")
	}
}

func TestASilentSeatIsAskedAgainBeforeItIsDropped(t *testing.T) {
	// A missed ask is indistinguishable from a network blip. Dropping on the first miss
	// would remove honest agents whose provider hiccuped, which is the same mistake as
	// voiding an honest developer for an unbound turn.
	p := DefaultPolicy("goofspiel")
	d := Evaluate(p, []Seat{
		seat("a", true, 1, time.Second),
		seat("b", false, 1, p.Window+time.Second), // expired, but only one ask so far
	}, t0)
	if d.Action != Ask {
		t.Fatalf("first expiry should re-ask, got %v", d.Action)
	}
	if len(d.Seats) != 1 || d.Seats[0] != "b" {
		t.Errorf("re-ask targeted %v, want [b]", d.Seats)
	}
}

func TestASeatOutOfAsksIsDropped(t *testing.T) {
	p := DefaultPolicy("goofspiel")
	d := Evaluate(p, []Seat{
		seat("a", true, 1, time.Second),
		seat("b", false, p.MaxAsks, p.Window+time.Second),
	}, t0)
	if d.Action != Drop {
		t.Fatalf("out of asks should Drop, got %v", d.Action)
	}
	if len(d.Seats) != 1 || d.Seats[0] != "b" {
		t.Errorf("dropped %v, want [b]", d.Seats)
	}
}

func TestAnAckOnTheLastAskBeatsTheSweep(t *testing.T) {
	// The race that decides whether someone is removed for doing everything right. A seat
	// that acknowledged is READY, and readiness is checked before expiry — so a sweep
	// arriving a moment after the ack must not drop it.
	p := DefaultPolicy("goofspiel")
	d := Evaluate(p, []Seat{
		seat("a", true, 1, time.Second),
		seat("b", true, p.MaxAsks, p.Window+time.Hour), // acked, but its ask long expired
	}, t0)
	if d.Action != Start {
		t.Fatalf("an acknowledged seat must never be dropped for a stale ask; got %v", d.Action)
	}
}

func TestAnUnaskedSeatIsAskedNotDropped(t *testing.T) {
	// Asks == 0 is "nobody has spoken to them", not silence. Dropping here would remove a
	// seat for failing to answer a question it was never asked.
	d := Evaluate(DefaultPolicy("goofspiel"),
		[]Seat{seat("a", true, 1, time.Second), seat("b", false, 0, 0)}, t0)
	if d.Action != Ask {
		t.Fatalf("an unasked seat should be asked, got %v", d.Action)
	}
}

func TestATableThatCanNeverStartIsAbandoned(t *testing.T) {
	// Goofspiel needs both seats. With one already dropped there is no table, and holding
	// the survivor is worse than releasing them to requeue.
	p := DefaultPolicy("goofspiel")
	p.MinReady = 2
	d := Evaluate(p, []Seat{seat("a", true, 1, time.Second)}, t0)
	if d.Action != Abandon {
		t.Fatalf("a table below MinReady with nothing pending should Abandon, got %v", d.Action)
	}
}

func TestShortHandedGamesStartBelowAFullRoster(t *testing.T) {
	// Mafia seats twelve and would never start if it needed all of them — which is why the
	// group queue already publishes min_seats and a short-handed clock. Four ready agents
	// is a table; the house fills the rest.
	p := DefaultPolicy("mafia")
	if p.MinReady >= p.Seats {
		t.Fatalf("mafia MinReady %d must be below its %d seats or a table can never start", p.MinReady, p.Seats)
	}
	seats := []Seat{}
	for _, id := range []string{"a", "b", "c", "d"} {
		seats = append(seats, seat(id, true, 1, time.Second))
	}
	if d := Evaluate(p, seats, t0); d.Action != Start {
		t.Fatalf("four ready mafia seats should Start, got %v", d.Action)
	}
}

func TestDropsAreReportedTogether(t *testing.T) {
	// Two dead seats are one decision, not two sweeps. Reporting them separately would
	// dissolve a Monopoly table one seat at a time instead of replacing both from the pool.
	p := DefaultPolicy("monopoly")
	d := Evaluate(p, []Seat{
		seat("a", true, 1, time.Second),
		seat("b", true, 1, time.Second),
		seat("c", false, p.MaxAsks, p.Window+time.Second),
		seat("d", false, p.MaxAsks, p.Window+time.Second),
	}, t0)
	if d.Action != Drop || len(d.Seats) != 2 {
		t.Fatalf("want both dead seats in one Drop, got %v %v", d.Action, d.Seats)
	}
}

func TestOutstandingAsksInsideTheWindowJustWait(t *testing.T) {
	p := DefaultPolicy("goofspiel")
	d := Evaluate(p, []Seat{
		seat("a", true, 1, time.Second),
		seat("b", false, 1, p.Window/2),
	}, t0)
	if d.Action != Wait {
		t.Fatalf("an ask inside its window should Wait, got %v", d.Action)
	}
}

func TestStartsAtIsAbsolute(t *testing.T) {
	// The countdown must leave here as an instant. A duration counted down independently by
	// a terminal and a browser drifts apart immediately, and the two disagreeing is the
	// whole failure a synchronised countdown exists to prevent.
	if got := StartsAt(t0, 10*time.Second); !got.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("StartsAt = %v, want %v", got, t0.Add(10*time.Second))
	}
}

func TestPolicyDefaultsAreSane(t *testing.T) {
	for _, game := range []string{"goofspiel", "mafia", "monopoly"} {
		p := DefaultPolicy(game).withDefaults()
		if p.MaxAsks < 2 {
			t.Errorf("%s: MaxAsks %d gives no second chance to a blipped agent", game, p.MaxAsks)
		}
		if p.MinReady > p.Seats {
			t.Errorf("%s: MinReady %d exceeds %d seats — the table could never start", game, p.MinReady, p.Seats)
		}
		if p.Countdown <= 0 || p.Window <= 0 {
			t.Errorf("%s: non-positive window/countdown", game)
		}
	}
}

func TestZeroPolicyIsUsableRatherThanDangerous(t *testing.T) {
	// A zero Policy must not mean "start immediately with nobody ready". Defaults fill in,
	// and MinReady falls back to the full roster rather than to zero.
	d := Evaluate(Policy{}, []Seat{seat("a", false, 0, 0), seat("b", false, 0, 0)}, t0)
	if d.Action == Start {
		t.Fatal("a zero policy started a table where nobody was ready")
	}
}
