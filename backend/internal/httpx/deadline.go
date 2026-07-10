package httpx

import (
	"net/http"
	"time"
)

// StreamWriteGrace bounds a single SSE-frame or long-poll response write. It must
// exceed the SSE heartbeat interval (25s) so an idle-but-healthy stream is never
// reaped, while still capping how long a stalled ("black-hole") client can wedge a
// write goroutine.
//
// The HTTP server runs with WriteTimeout=0 on purpose: an absolute per-connection
// write deadline set once at request start (what WriteTimeout does) force-closes
// every SSE stream and any long-poll that outlives it. This rolling per-write
// deadline replaces it on the long-lived paths; ordinary request/response handlers
// get a fixed bound from middleware.WriteDeadline instead.
const StreamWriteGrace = 40 * time.Second

// ArmWriteDeadline (re)arms a rolling write deadline of StreamWriteGrace on the
// underlying connection. Call it immediately before each SSE frame/keepalive write
// and before a long-poll's response write, so the deadline covers the write about to
// happen and the previous (possibly expired) deadline is overwritten. Best-effort: a
// ResponseWriter that cannot expose the deadline is left unbounded rather than
// erroring (the same wrappers already support rc.Flush(), so this reaches the conn).
func ArmWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(StreamWriteGrace))
}
