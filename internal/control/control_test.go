package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/CicerBro/speedtest/internal/session"
)

func setup(t *testing.T) (*httptest.Server, *session.Store, *session.Session) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := session.NewStore(session.Options{Logger: log})
	t.Cleanup(st.Close)
	ts := httptest.NewServer(New(st, Options{Logger: log}))
	t.Cleanup(ts.Close)
	sess, err := st.Create(netip.MustParseAddr("127.0.0.1"), 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	return ts, st, sess
}

func dial(t *testing.T, ts *httptest.Server, sid string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/?sid="+sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

type msg struct {
	Type    string          `json:"type"`
	ID      json.RawMessage `json:"id"`
	T       int64           `json:"t"`
	Up      int64           `json:"up"`
	Down    int64           `json:"down"`
	Code    string          `json:"code"`
	Streams *Streams        `json:"streams"`
}

func read(t *testing.T, c *websocket.Conn) msg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("bad json %q: %v", data, err)
	}
	return m
}

func TestPingPongAndStats(t *testing.T) {
	ts, _, sess := setup(t)
	sess.AddUp(1234)
	sess.AddDown(99)
	sess.AcquireUp(10)
	c := dial(t, ts, sess.ID)
	ctx := context.Background()

	start := time.Now()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"ping","id":17}`)); err != nil {
		t.Fatal(err)
	}
	var gotPong, gotStats bool
	for i := 0; i < 50 && !(gotPong && gotStats); i++ {
		m := read(t, c)
		switch m.Type {
		case "pong":
			if string(m.ID) != "17" {
				t.Fatalf("pong id %s", m.ID)
			}
			gotPong = true
			t.Logf("ping RTT %v", time.Since(start))
		case "stats":
			if m.Up != 1234 || m.Down != 99 || m.Streams == nil || m.Streams.Up != 1 || m.T <= 0 {
				t.Fatalf("stats %+v", m)
			}
			gotStats = true
		}
	}
	if !gotPong || !gotStats {
		t.Fatalf("pong=%v stats=%v", gotPong, gotStats)
	}
}

func TestStatsCadence(t *testing.T) {
	ts, _, sess := setup(t)
	c := dial(t, ts, sess.ID)
	start := time.Now()
	n := 0
	for n < 5 {
		if read(t, c).Type == "stats" {
			n++
		}
	}
	if el := time.Since(start); el < 400*time.Millisecond || el > 2*time.Second {
		t.Fatalf("5 stats took %v", el)
	}
}

func TestUnknownSession(t *testing.T) {
	ts, _, _ := setup(t)
	c := dial(t, ts, "bogus")
	m := read(t, c)
	if m.Type != "error" || m.Code != "session_expired" {
		t.Fatalf("got %+v", m)
	}
}

func TestSessionExpiryClosesWS(t *testing.T) {
	ts, st, sess := setup(t)
	c := dial(t, ts, sess.ID)
	read(t, c)
	st.Expire(sess.ID, session.ReasonPreempted)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != CloseSessionExpired {
				t.Fatalf("close status %v (%v)", websocket.CloseStatus(err), err)
			}
			return
		}
		if strings.Contains(string(data), `"session_expired"`) {
			continue
		}
	}
}

func TestWSCloseMarksFinishing(t *testing.T) {
	ts, st, sess := setup(t)
	c := dial(t, ts, sess.ID)
	read(t, c)
	c.Close(websocket.StatusNormalClosure, "")
	// The handler calls WSClosed asynchronously; with FinishGrace default
	// (10 s) the session must still be alive right away.
	time.Sleep(50 * time.Millisecond)
	if _, ok := st.Get(sess.ID); !ok {
		t.Fatal("session should survive WS close for the grace period")
	}
}

func TestBadMessage(t *testing.T) {
	ts, _, sess := setup(t)
	c := dial(t, ts, sess.ID)
	c.Write(context.Background(), websocket.MessageText, []byte(`nope`))
	for i := 0; i < 20; i++ {
		if m := read(t, c); m.Type == "error" {
			if m.Code != "bad_message" {
				t.Fatalf("code %q", m.Code)
			}
			return
		}
	}
	t.Fatal("no error message")
}

func TestAppendPong(t *testing.T) {
	if got := string(appendPong(nil, json.RawMessage(`"a"`))); got != `{"type":"pong","id":"a"}` {
		t.Fatal(got)
	}
	if got := string(appendPong(nil, nil)); got != `{"type":"pong"}` {
		t.Fatal(got)
	}
}
