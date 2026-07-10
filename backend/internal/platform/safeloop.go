package platform

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// safeLoopBackoff is the pause before restarting a worker that returned or panicked
// before its context was cancelled, so a persistent panic can't become a hot loop.
const safeLoopBackoff = time.Second

// SafeLoop runs a long-lived background worker fn and, if it panics, recovers, logs
// the panic with a stack, and restarts fn after a short backoff — until ctx is
// cancelled. An unrecovered panic in ANY goroutine aborts the entire Go process, so
// every background worker (event/webhook dispatchers, sweepers, reconcilers, the
// matcher, chain watchers, …) is launched through this: one bad iteration logs and
// restarts instead of taking the whole server — including in-flight matches, money
// settlement, and SSE streams — down with it.
//
// fn is expected to run until ctx is done; a clean return also ends the loop
// (after the same backoff, in case it returned early unexpectedly).
func SafeLoop(ctx context.Context, log *slog.Logger, name string, fn func(context.Context)) {
	for ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil && log != nil {
					log.Error("background worker panicked; restarting",
						"worker", name, "panic", r, "stack", string(debug.Stack()))
				}
			}()
			fn(ctx)
		}()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(safeLoopBackoff):
		}
	}
}
