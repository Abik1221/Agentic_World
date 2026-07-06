package agentgw

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// conn is one live agent socket. All writes funnel through sendCh so a single
// writer goroutine owns the socket (coder/websocket permits one concurrent
// writer); the read loop owns reads. Pending correlates outstanding requests.
type conn struct {
	gw         *Gateway
	ws         *websocket.Conn
	agentID    string
	name       string
	games      []string
	sdkVersion string

	sendCh   chan Frame
	lastSeen atomicInt64 // unixnano of the last frame received

	pmu     sync.Mutex
	pending map[string]chan Frame

	closed    chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
}

func (c *conn) addPending(id string, ch chan Frame) {
	c.pmu.Lock()
	c.pending[id] = ch
	c.pmu.Unlock()
}

func (c *conn) removePending(id string) {
	c.pmu.Lock()
	delete(c.pending, id)
	c.pmu.Unlock()
}

func (c *conn) deliver(f Frame) {
	c.pmu.Lock()
	ch := c.pending[f.ID]
	c.pmu.Unlock()
	if ch != nil {
		select {
		case ch <- f:
		default: // buffered(1); a duplicate/late response is dropped
		}
	}
}

// send enqueues a frame for the writer. It never blocks on the socket; if the
// send buffer is full (a stuck/slow agent) it fails fast so the caller can fall
// back rather than pile up.
func (c *conn) send(ctx context.Context, f Frame) error {
	select {
	case c.sendCh <- f:
		return nil
	case <-c.closed:
		return ErrNotConnected
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Buffer full: don't wait forever, but give the writer a brief moment.
		select {
		case c.sendCh <- f:
			return nil
		case <-c.closed:
			return ErrNotConnected
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return ErrNotConnected
		}
	}
}

func (c *conn) close(status websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.cancel()
		_ = c.ws.Close(status, reason)
	})
}

// writeLoop is the sole writer of the socket.
func (c *conn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case f := <-c.sendCh:
			wctx, cancel := context.WithTimeout(ctx, c.gw.opts.WriteTimeout)
			err := wsjson.Write(wctx, c.ws, f)
			cancel()
			if err != nil {
				c.gw.log.Warn("agentgw: write failed, closing", "agent", c.agentID, "err", err)
				c.close(websocket.StatusInternalError, "write failed")
				return
			}
		}
	}
}

// readLoop is the sole reader; it dispatches frames and drives connection life.
// Returning ends the connection.
func (c *conn) readLoop(ctx context.Context) {
	for {
		var f Frame
		if err := wsjson.Read(ctx, c.ws, &f); err != nil {
			return // remote closed, read error, or ctx cancelled
		}
		c.lastSeen.Store(time.Now().UnixNano())
		switch f.T {
		case FramePing:
			_ = c.send(ctx, Frame{T: FramePong, ID: f.ID})
		case FramePong:
			// liveness already refreshed above
		case FrameResponse:
			c.deliver(f)
		case FrameAck:
			// one-way acknowledgement; nothing to correlate
		default:
			c.gw.log.Warn("agentgw: unexpected frame from agent", "agent", c.agentID, "type", f.T)
		}
	}
}

// heartbeatLoop probes an idle socket and enforces liveness: if no frame has
// arrived within LivenessTimeout the agent is considered gone and the socket is
// closed (which unregisters it → Connected() returns false → drivers fall back).
func (c *conn) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(c.gw.opts.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, c.lastSeen.Load())) > c.gw.opts.LivenessTimeout {
				c.gw.log.Info("agentgw: liveness timeout, closing", "agent", c.agentID)
				c.close(websocket.StatusGoingAway, "heartbeat timeout")
				return
			}
			_ = c.send(ctx, Frame{T: FramePing})
		}
	}
}
