// Package control implements the /api/ws control channel (§5.5): immediate
// ping→pong for latency measurement and server-side stats pushed every 100 ms.
package control

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/CicerBro/speedtest/internal/session"
)

// StatsInterval is the stats push period.
const StatsInterval = 100 * time.Millisecond

// CloseSessionExpired is the WebSocket close code used when the session ends.
const CloseSessionExpired websocket.StatusCode = 4410

const writeTimeout = 5 * time.Second

// Options configure the handler.
type Options struct {
	// OriginPatterns are extra allowed Origin hosts (coder/websocket syntax);
	// same-host origins are always allowed.
	OriginPatterns []string
	Logger         *slog.Logger
}

// Handler serves GET /api/ws?sid=….
type Handler struct {
	store *session.Store
	opts  Options
	log   *slog.Logger
}

// New creates the handler.
func New(store *session.Store, opts Options) *Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Handler{store: store, opts: opts, log: opts.Logger}
}

type inMsg struct {
	Type string          `json:"type"`
	ID   json.RawMessage `json:"id"`
}

// StatsMsg is the server → client stats message.
type StatsMsg struct {
	Type    string  `json:"type"` // "stats"
	T       int64   `json:"t"`    // ns since session start
	Up      int64   `json:"up"`   // cumulative bytes
	Down    int64   `json:"down"` // cumulative bytes
	Streams Streams `json:"streams"`
}

// Streams are the active stream counts.
type Streams struct {
	Up   int32 `json:"up"`
	Down int32 `json:"down"`
}

// ErrorMsg is the server → client error message.
type ErrorMsg struct {
	Type    string `json:"type"` // "error"
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sess, sessOK := h.store.Get(r.URL.Query().Get("sid"))
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  h.opts.OriginPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		h.log.Debug("ws accept failed", "err", err)
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(4096)

	if !sessOK {
		writeJSON(r.Context(), c, ErrorMsg{Type: "error", Code: "session_expired", Message: "unknown or expired session"})
		c.Close(CloseSessionExpired, "session_expired")
		return
	}

	sess.WSOpened()
	defer sess.WSClosed()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Stats ticker: reads only atomics, shares no locks with the pong path.
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		h.pushStats(ctx, c, sess)
	}()

	pong := make([]byte, 0, 64)
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		var m inMsg
		if json.Unmarshal(data, &m) != nil {
			writeJSON(ctx, c, ErrorMsg{Type: "error", Code: "bad_message", Message: "invalid JSON"})
			continue
		}
		switch m.Type {
		case "ping":
			pong = appendPong(pong[:0], m.ID)
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err = c.Write(wctx, websocket.MessageText, pong)
			wcancel()
			if err != nil {
				cancel()
			}
		default:
			writeJSON(ctx, c, ErrorMsg{Type: "error", Code: "bad_message", Message: "unknown message type"})
		}
		if ctx.Err() != nil {
			break
		}
	}
	cancel()
	<-tickerDone
}

// appendPong builds {"type":"pong","id":<id>} echoing the raw id.
func appendPong(b []byte, id json.RawMessage) []byte {
	b = append(b, `{"type":"pong"`...)
	if len(id) > 0 {
		b = append(b, `,"id":`...)
		b = append(b, id...)
	}
	return append(b, '}')
}

func (h *Handler) pushStats(ctx context.Context, c *websocket.Conn, sess *session.Session) {
	t := time.NewTicker(StatsInterval)
	defer t.Stop()
	buf := make([]byte, 0, 160)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Done():
			writeJSON(context.Background(), c, ErrorMsg{Type: "error", Code: "session_expired", Message: "session " + sess.Reason()})
			c.Close(CloseSessionExpired, "session_expired")
			return
		case <-t.C:
			buf = appendStats(buf[:0], sess)
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.Write(wctx, websocket.MessageText, buf)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func appendStats(b []byte, s *session.Session) []byte {
	b = append(b, `{"type":"stats","t":`...)
	b = strconv.AppendInt(b, s.Elapsed().Nanoseconds(), 10)
	b = append(b, `,"up":`...)
	b = strconv.AppendInt(b, s.UpBytes.Load(), 10)
	b = append(b, `,"down":`...)
	b = strconv.AppendInt(b, s.DownBytes.Load(), 10)
	b = append(b, `,"streams":{"up":`...)
	b = strconv.AppendInt(b, int64(s.ActiveUp.Load()), 10)
	b = append(b, `,"down":`...)
	b = strconv.AppendInt(b, int64(s.ActiveDown.Load()), 10)
	return append(b, `}}`...)
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_ = c.Write(wctx, websocket.MessageText, data)
}
