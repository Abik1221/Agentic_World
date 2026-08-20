package spectator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/agent-arena/arena/internal/commentary"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/redact"
)

// wireEvent is the JSON shape of a broadcast event. It carries the engine's
// already-redacted payload plus, for resolved rounds, deterministic commentary
// and a dramatic flag (consumed by Stage 8 clips).
type wireEvent struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	Payload    any    `json:"payload"`
	Commentary string `json:"commentary,omitempty"`
	Dramatic   bool   `json:"dramatic,omitempty"`
}

// encodeEvent renders one engine event as an SSE frame. The `id:` line is the
// sequence number so a client can resume precisely via Last-Event-ID.
func (h *Hub) encodeEvent(ev gs.Event) frame {
	we := wireEvent{Seq: ev.Seq, Type: string(ev.Type), Payload: ev.Payload}
	if ev.Type == gs.EvRoundRevealed {
		if r, ok := h.roundFor(ev.Payload); ok {
			we.Commentary = commentary.Line(r)
			we.Dramatic = commentary.Dramatic(r)
		}
	}
	body, _ := json.Marshal(we)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, body)
	return frame{seq: ev.Seq, data: buf.Bytes()}
}

// roundFor converts a round_revealed payload (typed when live, a map when loaded
// from the store) into a commentary.Round via a marshal round-trip.
func (h *Hub) roundFor(payload any) (commentary.Round, bool) {
	b, err := json.Marshal(payload)
	if err != nil {
		return commentary.Round{}, false
	}
	var rp gs.RoundRevealedPayload
	if err := json.Unmarshal(b, &rp); err != nil {
		return commentary.Round{}, false
	}
	return commentary.Round{
		Round:       rp.Round,
		TotalRounds: h.defaultRound,
		Prize:       rp.Prize,
		PrizePool:   rp.PrizePool,
		CardA:       rp.Cards[gs.SeatA],
		CardB:       rp.Cards[gs.SeatB],
		Winner:      rp.Winner,
		ScoreA:      rp.Scores[gs.SeatA],
		ScoreB:      rp.Scores[gs.SeatB],
	}, true
}

// spectatorSafe reports whether an event may be sent to a LIVE spectator on the
// generic /v1/match/{id}/watch stream.
//
// The rule itself lives in internal/redact, because the public replay endpoint
// needs exactly the same one and two copies of a security predicate drift — the
// copy nobody updated keeps answering "safe". See that package for the leak this
// closed.
func spectatorSafe(ev gs.Event) bool {
	return redact.SafeForLive(string(ev.Type), ev.Payload)
}

// backlog returns the encoded frames for events after lastSeq, used to fulfil a
// Last-Event-ID resume before the live stream takes over.
func (h *Hub) backlog(ctx context.Context, matchPublicID string, lastSeq int) ([]frame, error) {
	evs, err := h.events.LoadEvents(ctx, matchPublicID)
	if err != nil {
		return nil, err
	}
	var out []frame
	for _, ev := range evs {
		if ev.Seq <= lastSeq {
			continue
		}
		// The history replay leaks just as readily as the live tail — a fresh
		// viewer gets the WHOLE log here, so this is the path that handed over
		// every night action at once.
		if !spectatorSafe(ev) {
			continue
		}
		out = append(out, h.encodeEvent(ev))
	}
	return out, nil
}
