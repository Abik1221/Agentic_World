package spectator

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeEvents struct{ evs []gs.Event }

func (f fakeEvents) LoadEvents(context.Context, string) ([]gs.Event, error) { return f.evs, nil }

func newHub(t *testing.T, ev EventLog) *Hub {
	t.Helper()
	return NewHub(ev, 13, slog.Default(), prometheus.NewRegistry())
}

// decodeFrame pulls the JSON out of an SSE frame's `data:` line.
func decodeFrame(t *testing.T, data []byte) struct {
	Seq        int            `json:"seq"`
	Type       string         `json:"type"`
	Payload    map[string]any `json:"payload"`
	Commentary string         `json:"commentary"`
	Dramatic   bool           `json:"dramatic"`
} {
	t.Helper()
	var out struct {
		Seq        int            `json:"seq"`
		Type       string         `json:"type"`
		Payload    map[string]any `json:"payload"`
		Commentary string         `json:"commentary"`
		Dramatic   bool           `json:"dramatic"`
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "data: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &out); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			return out
		}
	}
	t.Fatalf("no data line in frame: %q", data)
	return out
}

func roundEvent(seq int) gs.Event {
	return gs.Event{Seq: seq, Type: gs.EvRoundRevealed, Payload: gs.RoundRevealedPayload{
		Round: seq, Prize: 5, PrizePool: 5, Cards: [2]int{9, 4}, Winner: gs.SeatA, Scores: [2]int{14, 8},
	}}
}

func TestBroadcastDeliversWithCommentary(t *testing.T) {
	h := newHub(t, fakeEvents{})
	s, _ := h.Subscribe("m1")
	defer h.Unsubscribe("m1", s)

	h.Broadcast("m1", []gs.Event{roundEvent(3)})

	select {
	case fr := <-s.ch:
		we := decodeFrame(t, fr.data)
		if we.Type != string(gs.EvRoundRevealed) {
			t.Fatalf("type = %s", we.Type)
		}
		if we.Commentary == "" {
			t.Fatal("expected commentary on a revealed round")
		}
	case <-time.After(time.Second):
		t.Fatal("no frame delivered")
	}
}

// A sealed-card event must never carry a card value to spectators.
func TestRedactionSealedCardHasNoValue(t *testing.T) {
	h := newHub(t, fakeEvents{})
	s, _ := h.Subscribe("m1")
	defer h.Unsubscribe("m1", s)

	h.Broadcast("m1", []gs.Event{{Seq: 1, Type: gs.EvCardSealed, Payload: gs.CardSealedPayload{Round: 1, Seat: gs.SeatA}}})

	fr := <-s.ch
	we := decodeFrame(t, fr.data)
	for _, leak := range []string{"card", "cards", "value"} {
		if _, ok := we.Payload[leak]; ok {
			t.Fatalf("sealed-card event leaked %q: %v", leak, we.Payload)
		}
	}
	if _, ok := we.Payload["seat"]; !ok {
		t.Fatal("expected seat in sealed payload")
	}
}

// Broadcast must never block on a slow consumer; the consumer is dropped instead.
func TestSlowConsumerDroppedNeverBlocks(t *testing.T) {
	h := newHub(t, fakeEvents{})
	s, _ := h.Subscribe("m1")
	defer h.Unsubscribe("m1", s)

	evs := make([]gs.Event, 100) // far more than the per-sub buffer (32)
	for i := range evs {
		evs[i] = gs.Event{Seq: i, Type: gs.EvPrizeRevealed, Payload: gs.PrizeRevealedPayload{Round: i, Prize: 1, PrizePool: 1}}
	}

	done := make(chan struct{})
	go func() { h.Broadcast("m1", evs); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a non-draining subscriber")
	}

	select {
	case <-s.dead:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber was not dropped")
	}
}

// R4: the instance-wide subscriber ceiling spans all matches, and Unsubscribe
// frees a slot (total accounting stays correct).
func TestGlobalConnCapAndRelease(t *testing.T) {
	h := newHub(t, fakeEvents{})
	h.SetMaxConns(2)

	s1, err := h.Subscribe("m1")
	if err != nil {
		t.Fatalf("sub1: %v", err)
	}
	s2, err := h.Subscribe("m2") // a DIFFERENT match still counts toward the cap
	if err != nil {
		t.Fatalf("sub2: %v", err)
	}
	if _, err := h.Subscribe("m3"); err != ErrTooManyWatchers {
		t.Fatalf("instance-wide cap not enforced: %v", err)
	}

	// Freeing a slot lets a new watcher in (total decremented on unsubscribe).
	h.Unsubscribe("m1", s1)
	s3, err := h.Subscribe("m3")
	if err != nil {
		t.Fatalf("subscribe after release: %v", err)
	}
	h.Unsubscribe("m2", s2)
	h.Unsubscribe("m3", s3)
}

func TestBacklogResumesAfterLastEventID(t *testing.T) {
	evs := []gs.Event{roundEvent(0), roundEvent(1), roundEvent(2), roundEvent(3), roundEvent(4)}
	h := newHub(t, fakeEvents{evs: evs})

	frames, err := h.backlog(context.Background(), "m1", 2)
	if err != nil {
		t.Fatalf("backlog: %v", err)
	}
	if len(frames) != 2 || frames[0].seq != 3 || frames[1].seq != 4 {
		t.Fatalf("resume from seq 2 = %+v, want seqs [3,4]", frames)
	}
}
