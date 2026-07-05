package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeRepo is an in-memory outbox.
type fakeRepo struct {
	pending   []Event
	published map[string]bool
	attempts  map[string]int
}

func newFakeRepo(evs ...Event) *fakeRepo {
	return &fakeRepo{pending: evs, published: map[string]bool{}, attempts: map[string]int{}}
}

func (f *fakeRepo) Unpublished(_ context.Context, limit, maxAttempts int) ([]Event, error) {
	var out []Event
	for _, e := range f.pending {
		if f.published[e.ID] || f.attempts[e.ID] >= maxAttempts {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
func (f *fakeRepo) MarkPublished(_ context.Context, id string) error {
	f.published[id] = true
	return nil
}
func (f *fakeRepo) BumpAttempts(_ context.Context, id string) error { f.attempts[id]++; return nil }

func TestDispatcher_DeliversAndPublishes(t *testing.T) {
	repo := newFakeRepo(Event{ID: "evt_1", Type: TypeAgentCertified})
	d := New(repo, quietLog(), time.Second)

	var got []string
	d.On(TypeAgentCertified, func(_ context.Context, e Event) error { got = append(got, e.ID); return nil })

	if err := d.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "evt_1" {
		t.Fatalf("handler not called: %v", got)
	}
	if !repo.published["evt_1"] {
		t.Fatal("event not marked published")
	}

	// Second tick: already published → not redelivered.
	got = nil
	_ = d.tick(context.Background())
	if len(got) != 0 {
		t.Fatalf("published event was redelivered: %v", got)
	}
}

func TestDispatcher_FailedHandlerRetries(t *testing.T) {
	repo := newFakeRepo(Event{ID: "evt_2", Type: TypeAgentCertified})
	d := New(repo, quietLog(), time.Second)

	calls := 0
	d.On(TypeAgentCertified, func(_ context.Context, _ Event) error {
		calls++
		if calls < 3 {
			return errors.New("boom")
		}
		return nil
	})

	_ = d.tick(context.Background()) // fail 1
	_ = d.tick(context.Background()) // fail 2
	if repo.published["evt_2"] {
		t.Fatal("should not publish while handler fails")
	}
	if repo.attempts["evt_2"] != 2 {
		t.Fatalf("expected 2 attempts, got %d", repo.attempts["evt_2"])
	}
	_ = d.tick(context.Background()) // success
	if !repo.published["evt_2"] {
		t.Fatal("expected publish after handler recovers")
	}
}

func TestDispatcher_PoisonEventSkippedAfterMaxAttempts(t *testing.T) {
	repo := newFakeRepo(Event{ID: "evt_3", Type: TypeAgentCertified})
	repo.attempts["evt_3"] = 10 // already at cap
	d := New(repo, quietLog(), time.Second)
	called := false
	d.On(TypeAgentCertified, func(_ context.Context, _ Event) error { called = true; return nil })

	_ = d.tick(context.Background())
	if called {
		t.Fatal("poison event past maxAttempts must not be delivered")
	}
}

func TestDispatcher_MultipleHandlersAllMustSucceed(t *testing.T) {
	repo := newFakeRepo(Event{ID: "evt_4", Type: TypeAgentCertified})
	d := New(repo, quietLog(), time.Second)
	d.On(TypeAgentCertified, func(_ context.Context, _ Event) error { return nil })
	d.On(TypeAgentCertified, func(_ context.Context, _ Event) error { return errors.New("second fails") })

	_ = d.tick(context.Background())
	if repo.published["evt_4"] {
		t.Fatal("must not publish if any handler fails")
	}
}

func TestDispatcher_UnknownTypeIsTriviallyPublished(t *testing.T) {
	repo := newFakeRepo(Event{ID: "evt_5", Type: "nobody.listens"})
	d := New(repo, quietLog(), time.Second)
	_ = d.tick(context.Background())
	if !repo.published["evt_5"] {
		t.Fatal("event with no handlers should be marked published")
	}
}
