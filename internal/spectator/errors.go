package spectator

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// ErrTooManyWatchers is returned when this instance's per-match watcher cap is
// reached; the load balancer routes additional spectators to other instances.
var ErrTooManyWatchers = httpx.NewError(http.StatusServiceUnavailable, "watchers_full", "This match has too many watchers on this node; retry.")
