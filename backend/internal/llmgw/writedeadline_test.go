package llmgw

// The bug this pins cost a real benchmark run and would have corrupted a published result.
//
// The router arms a FIXED write deadline when a request arrives, because the server runs
// WriteTimeout=0 so SSE can work. A proxied model call then spends its whole life waiting on the
// upstream and only afterwards starts writing — so a model that thinks for longer than that
// deadline reaches its first write with the deadline already expired. The client gets a few
// hundred bytes of a multi-kilobyte JSON body and an unexpected EOF.
//
// Nothing downstream reported it as a timeout. It surfaced as "the model returned no tool call",
// "usage unreadable, costed at zero", and "the agent did not play" — and the arena covered the
// last one with a fallback move. The severity scales with thinking time, so it punished precisely
// the reasoning models a benchmark exists to measure.
//
// Both halves are tested. Without a rolling deadline the body MUST arrive truncated; with one it
// MUST arrive whole. A guard whose failure mode nobody has watched is not a guard.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// bodyBiggerThanOneWrite is larger than copyFlushing's 32 KiB buffer, so delivering it takes
// several writes and a rolling deadline has something to roll over.
var bodyBiggerThanOneWrite = bytes.Repeat([]byte("m"), 256<<10)

// slowUpstreamServer mimics the production shape: the router's fixed deadline, then a model that
// thinks past it, then the proxy's copy loop. arm decides whether the proxy re-arms.
func slowUpstreamServer(t *testing.T, arm bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// What mw.WriteDeadline does for every ordinary request.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(150 * time.Millisecond))
		// What a reasoning model does.
		time.Sleep(400 * time.Millisecond)
		if arm {
			armWrite(w)
		}
		w.WriteHeader(http.StatusOK)
		if arm {
			_, _ = copyFlushing(w, bytes.NewReader(bodyBiggerThanOneWrite), w)
			return
		}
		// The copy loop EXACTLY as it stood before the fix. copyFlushing now re-arms internally,
		// so calling it here would hand the unfixed path the fix and the defect would stop
		// reproducing — which is what the first draft of this test did.
		copyLoopBeforeTheFix(w, bytes.NewReader(bodyBiggerThanOneWrite), w)
	}))
}

// copyLoopBeforeTheFix is the pre-fix copyFlushing verbatim, minus the arming.
func copyLoopBeforeTheFix(dst io.Writer, src io.Reader, w http.ResponseWriter) {
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			_, werr := dst.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
			if werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

func TestProxyDeliversAWholeBodyAfterTheModelThinksPastTheRouterDeadline(t *testing.T) {
	srv := slowUpstreamServer(t, true)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("the body ended early after %d of %d bytes — a model that thinks longer than the "+
			"router's write deadline is having its answer truncated, which downstream reads as the "+
			"model failing to answer: %v", len(got), len(bodyBiggerThanOneWrite), err)
	}
	if len(got) != len(bodyBiggerThanOneWrite) {
		t.Fatalf("got %d bytes, want %d — truncated", len(got), len(bodyBiggerThanOneWrite))
	}
}

// TestWithoutRearmingASlowModelsAnswerIsTruncated is the other half: it demonstrates the original
// defect still reproduces, so the test above is known to be testing the fix rather than passing
// because the environment is forgiving.
func TestWithoutRearmingASlowModelsAnswerIsTruncated(t *testing.T) {
	srv := slowUpstreamServer(t, false)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		// The connection failing outright is the same defect, reported earlier.
		return
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err == nil && len(got) == len(bodyBiggerThanOneWrite) {
		t.Fatalf("the whole %d-byte body arrived without re-arming the write deadline — this test "+
			"is no longer reproducing the defect, so its sibling proves nothing and the rolling "+
			"deadline could be removed without any test noticing", len(got))
	}
}
