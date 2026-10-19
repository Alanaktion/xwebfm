package api

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/alanaktion/xwebfm/internal/jobs"
)

// handleWS streams job and change events. The first message is a
// {"type":"snapshot"} with all jobs; later messages are jobs.Event values.
// The connection is server-to-client only; actions go through the REST API
// so they share its CSRF protection.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Accept rejects cross-origin handshakes unless the origin matches
	// OriginPatterns, preventing cross-site WebSocket hijacking.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.OriginPatterns})
	if err != nil {
		return // Accept has already written the error response
	}
	defer c.CloseNow()

	snapshot, events, unsubscribe := s.Jobs.Subscribe()
	defer unsubscribe()
	if snapshot == nil {
		snapshot = []jobs.Job{}
	}
	ctx := c.CloseRead(r.Context())
	// A dedicated type so "jobs" is present even when empty.
	type snapshotEvent struct {
		Type string     `json:"type"`
		Jobs []jobs.Job `json:"jobs"`
	}
	if err := write(ctx, c, snapshotEvent{"snapshot", snapshot}); err != nil {
		return
	}

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// Fell behind or the server is stopping; the client
				// reconnects and gets a fresh snapshot.
				c.Close(websocket.StatusTryAgainLater, "resubscribe")
				return
			}
			if err := write(ctx, c, ev); err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func write(ctx context.Context, c *websocket.Conn, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c, v)
}
