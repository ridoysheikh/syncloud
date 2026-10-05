package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"syncloud/internal/events"
)

const streamPingEvery = 20 * time.Second

// handleStream is the live-update WebSocket (§5.1). It forwards bus events to
// the dashboard. Subscription scoping per view and IAM filtering come with the
// resources that need them.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// websocket.Accept rejects cross-origin upgrades by default (Origin must match Host).
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// The dashboard never sends data messages; CloseRead handles control frames
	// and cancels ctx when the client goes away.
	ctx = c.CloseRead(ctx)

	sub := s.bus.Subscribe(256)
	defer sub.Close()

	if err := writeEvent(ctx, c, events.Event{Topic: "stream.hello", At: s.now()}); err != nil {
		return
	}
	ping := time.NewTicker(streamPingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			if err := writeEvent(ctx, c, e); err != nil {
				return
			}
		}
	}
}

func writeEvent(ctx context.Context, c *websocket.Conn, e events.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}
