package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Ping intervals (§6.1).
const (
	IdlePingInterval   = 100 * time.Millisecond
	LoadedPingInterval = 250 * time.Millisecond
)

const wsWriteTimeout = 5 * time.Second

// rttSample is one ping round trip, timestamped at pong receipt.
type rttSample struct {
	at time.Time
	ms float64
}

// statsSample is one server stats message with its client receipt time.
type statsSample struct {
	recv time.Time
	t    int64 // server ns since session start
	up   int64 // cumulative uploaded bytes
	down int64
}

// wsMsg is any server → client control message.
type wsMsg struct {
	Type    string `json:"type"`
	ID      int64  `json:"id"`
	T       int64  `json:"t"`
	Up      int64  `json:"up"`
	Down    int64  `json:"down"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// control is the /api/ws connection: ping/pong latency and the server
// stats subscription (§5.5). RTTs use Go's monotonic clock.
type control struct {
	conn     *websocket.Conn
	interval atomic.Int64 // ping interval in ns; 0 = paused
	wake     chan struct{}

	mu      sync.Mutex
	pending map[int64]time.Time
	rtts    []rttSample
	stats   []statsSample
	err     error

	cancel  context.CancelFunc
	closing atomic.Bool
	wg      sync.WaitGroup
}

// wsURL turns an http(s) origin into the ws(s) control URL.
func wsURL(base, sid string) string {
	u := base
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + "/api/ws?sid=" + url.QueryEscape(sid)
}

// dialControl opens the WebSocket. The Authorization header travels on the
// upgrade request; a 401 or 3xx answer is an AuthError.
func dialControl(ctx context.Context, hc *http.Client, base, sid string, header http.Header) (*control, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout+tlsHandshakeTimeout)
	defer cancel()
	conn, resp, err := websocket.Dial(dctx, wsURL(base, sid), &websocket.DialOptions{
		HTTPClient:      hc,
		HTTPHeader:      header.Clone(),
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized || (resp.StatusCode >= 300 && resp.StatusCode < 400) {
				return nil, &AuthError{Status: resp.StatusCode, Location: resp.Header.Get("Location"), URL: base + "/api/ws"}
			}
			return nil, fmt.Errorf("websocket: HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("websocket: %w", err)
	}
	conn.SetReadLimit(64 << 10)
	return &control{conn: conn, wake: make(chan struct{}, 1), pending: make(map[int64]time.Time)}, nil
}

// start runs the reader and pinger goroutines until close.
func (c *control) start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.readLoop(ctx)
	}()
	go func() {
		defer c.wg.Done()
		c.pingLoop(ctx)
	}()
}

// setInterval changes the ping interval (0 pauses). A ping is sent
// immediately when pinging (re)starts or the interval changes.
func (c *control) setInterval(d time.Duration) {
	c.interval.Store(int64(d))
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *control) setErr(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

// Err returns the first control-channel error (nil while healthy).
func (c *control) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *control) readLoop(ctx context.Context) {
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil && !c.closing.Load() {
				c.setErr(fmt.Errorf("websocket closed: %w", err))
			}
			return
		}
		now := time.Now()
		if typ != websocket.MessageText {
			continue
		}
		var m wsMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "pong":
			c.mu.Lock()
			if sent, ok := c.pending[m.ID]; ok {
				delete(c.pending, m.ID)
				c.rtts = append(c.rtts, rttSample{at: now, ms: float64(now.Sub(sent)) / float64(time.Millisecond)})
			}
			c.mu.Unlock()
		case "stats":
			c.mu.Lock()
			c.stats = append(c.stats, statsSample{recv: now, t: m.T, up: m.Up, down: m.Down})
			c.mu.Unlock()
		case "error":
			c.setErr(errors.New("server: " + m.Code + ": " + m.Message))
		}
	}
}

func (c *control) pingLoop(ctx context.Context) {
	var (
		id    int64
		timer *time.Timer
		tick  <-chan time.Time
		buf   = make([]byte, 0, 48)
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	arm := func() {
		d := time.Duration(c.interval.Load())
		if timer != nil {
			timer.Stop()
			tick = nil
		}
		if d <= 0 {
			return
		}
		timer = time.NewTimer(d)
		tick = timer.C
	}
	send := func() bool {
		if c.interval.Load() <= 0 {
			return true
		}
		id++
		buf = append(buf[:0], `{"type":"ping","id":`...)
		buf = strconv.AppendInt(buf, id, 10)
		buf = append(buf, '}')
		c.mu.Lock()
		c.pending[id] = time.Now()
		c.mu.Unlock()
		wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
		err := c.conn.Write(wctx, websocket.MessageText, buf)
		cancel()
		if err != nil {
			if ctx.Err() == nil && !c.closing.Load() {
				c.setErr(fmt.Errorf("websocket write: %w", err))
			}
			return false
		}
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			if !send() {
				return
			}
			arm()
		case <-tick:
			if !send() {
				return
			}
			arm()
		}
	}
}

// rttsBetween returns the RTTs received in [a, b], in order.
func (c *control) rttsBetween(a, b time.Time) []float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []float64
	for _, s := range c.rtts {
		if !s.at.Before(a) && !s.at.After(b) {
			out = append(out, s.ms)
		}
	}
	return out
}

// lastRTT returns the most recent RTT in ms (0 if none).
func (c *control) lastRTT() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rtts) == 0 {
		return 0
	}
	return c.rtts[len(c.rtts)-1].ms
}

// statsSnapshot returns a copy of the stats received so far.
func (c *control) statsSnapshot() []statsSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]statsSample(nil), c.stats...)
}

// close performs the WebSocket close handshake (which marks the session as
// finishing on the server) and stops the goroutines. A dead peer can delay
// it by at most 2 s.
func (c *control) close() {
	c.closing.Store(true)
	c.setInterval(0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.conn.Close(websocket.StatusNormalClosure, "done")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.conn.CloseNow()
		<-done
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}
