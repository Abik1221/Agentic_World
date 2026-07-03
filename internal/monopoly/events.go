package monopoly

import (
	"bytes"
	"encoding/json"
	"fmt"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
)

// wireEvent is the JSON envelope for a streamed Monopoly event — the same
// {seq,type,payload} shape the Goofspiel/Mafia spectator streams use, so the
// frontend reuses one StreamEvent contract. Every Monopoly event is a record of
// something that already happened (a roll, a purchase, a card draw), so unlike
// Mafia there are no hidden fields to redact from the log.
type wireEvent struct {
	Seq     int    `json:"seq"`
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// EncodeEvent renders one engine event as an SSE frame. The id: line carries the
// monotonic sequence so a client can resume precisely via Last-Event-ID.
func EncodeEvent(ev mono.Event) frame {
	body, _ := json.Marshal(wireEvent{Seq: ev.Seq, Type: string(ev.Type), Payload: ev.Payload})
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, body)
	return frame{seq: ev.Seq, data: buf.Bytes()}
}
