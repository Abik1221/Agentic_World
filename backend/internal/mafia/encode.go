package mafia

import (
	"bytes"
	"encoding/json"
	"fmt"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
)

// EncodeEvent renders one engine event as an SSE frame.
func EncodeEvent(ev mf.Event) frame {
	body, _ := json.Marshal(wireEvent{Seq: ev.Seq, Type: string(ev.Type), Payload: ev.Payload})
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, body)
	return frame{seq: ev.Seq, data: buf.Bytes()}
}
