package session

import (
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var ip = netip.MustParseAddr("192.0.2.1")

func newTestStore(maxActive int) (*Store, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	st := NewStore(Options{
		MaxActive: maxActive,
		TTL:       120 * time.Second,
		MaxAge:    5 * time.Minute,
		Now:       clk.Now,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return st, clk
}

func TestCreateLookup(t *testing.T) {
	st, _ := newTestStore(2)
	defer st.Close()
	s, err := st.Create(ip, 8, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ID) != 32 {
		t.Fatalf("sid %q not 32 hex chars", s.ID)
	}
	got, ok := st.Get(s.ID)
	if !ok || got != s {
		t.Fatal("lookup failed")
	}
	if _, ok := st.Get("nope"); ok {
		t.Fatal("unknown sid found")
	}
	s2, _ := st.Create(ip, 1, "alice")
	if s2.ID == s.ID {
		t.Fatal("duplicate IDs")
	}
}

func TestIdleExpiry(t *testing.T) {
	st, clk := newTestStore(1)
	defer st.Close()
	s, _ := st.Create(ip, 1, "u")
	clk.Advance(119 * time.Second)
	st.Sweep()
	if !s.Alive() {
		t.Fatal("expired too early")
	}
	s.Touch()
	clk.Advance(119 * time.Second)
	st.Sweep()
	if !s.Alive() {
		t.Fatal("touch did not extend")
	}
	clk.Advance(2 * time.Second)
	st.Sweep()
	if s.Alive() || s.Reason() != ReasonIdle {
		t.Fatalf("want idle expiry, alive=%v reason=%q", s.Alive(), s.Reason())
	}
	if _, ok := st.Get(s.ID); ok {
		t.Fatal("expired session still retrievable")
	}
	select {
	case <-s.Context().Done():
	default:
		t.Fatal("context not cancelled")
	}
}

func TestActiveStreamsOrWSPreventIdleExpiry(t *testing.T) {
	st, clk := newTestStore(1)
	defer st.Close()
	s, _ := st.Create(ip, 1, "u")
	if !s.AcquireDown(1) {
		t.Fatal("acquire")
	}
	clk.Advance(130 * time.Second)
	st.Sweep()
	if !s.Alive() {
		t.Fatal("expired with active stream")
	}
	s.ReleaseDown()
	s.WSOpened()
	clk.Advance(130 * time.Second)
	st.Sweep()
	if !s.Alive() {
		t.Fatal("expired with open WS")
	}
}

func TestMaxAge(t *testing.T) {
	st, clk := newTestStore(1)
	defer st.Close()
	s, _ := st.Create(ip, 1, "u")
	s.WSOpened()
	s.AcquireUp(4)
	for i := 0; i < 10; i++ {
		clk.Advance(30 * time.Second)
		s.Touch()
	}
	st.Sweep()
	if s.Alive() || s.Reason() != ReasonMaxAge {
		t.Fatalf("want max age expiry, reason=%q", s.Reason())
	}
}

func TestWSCloseFinishing(t *testing.T) {
	st, clk := newTestStore(1)
	defer st.Close()
	s, _ := st.Create(ip, 1, "u")
	s.WSOpened()
	clk.Advance(30 * time.Second)
	s.WSClosed()
	clk.Advance(9 * time.Second)
	st.Sweep()
	if !s.Alive() {
		t.Fatal("expired before 10 s")
	}
	s.Touch() // a new request postpones it
	clk.Advance(9 * time.Second)
	st.Sweep()
	if !s.Alive() {
		t.Fatal("request did not postpone finishing expiry")
	}
	clk.Advance(1 * time.Second)
	st.Sweep()
	if s.Alive() || s.Reason() != ReasonFinished {
		t.Fatalf("want finished expiry, reason=%q", s.Reason())
	}
}

func TestWSReopenClearsFinishing(t *testing.T) {
	st, clk := newTestStore(1)
	defer st.Close()
	s, _ := st.Create(ip, 1, "u")
	s.WSOpened()
	s.WSClosed()
	s.WSOpened()
	clk.Advance(60 * time.Second)
	st.Sweep()
	if !s.Alive() {
		t.Fatal("reopened WS should keep session alive")
	}
}

func TestNewestWins(t *testing.T) {
	st, clk := newTestStore(2)
	defer st.Close()
	a, _ := st.Create(ip, 1, "u")
	clk.Advance(time.Second)
	b, _ := st.Create(ip, 1, "u")
	clk.Advance(time.Second)
	c, _ := st.Create(ip, 1, "u")
	if a.Alive() || a.Reason() != ReasonPreempted {
		t.Fatalf("oldest should be preempted, reason=%q", a.Reason())
	}
	if !b.Alive() || !c.Alive() {
		t.Fatal("newer sessions should survive")
	}
	if st.Len() != 2 {
		t.Fatalf("len=%d", st.Len())
	}
	stats := st.Stats()
	if stats.Created != 3 || stats.Preempted != 1 || stats.Active != 2 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestStreamCapsAndCounters(t *testing.T) {
	st, _ := newTestStore(1)
	defer st.Close()
	s, _ := st.Create(ip, 2, "u")
	for i := 0; i < 4; i++ {
		if !s.AcquireDown(4) {
			t.Fatalf("acquire %d failed", i)
		}
	}
	if s.AcquireDown(4) {
		t.Fatal("cap exceeded")
	}
	if s.ActiveDown.Load() != 4 {
		t.Fatalf("active=%d", s.ActiveDown.Load())
	}
	s.ReleaseDown()
	if !s.AcquireDown(4) {
		t.Fatal("slot not released")
	}
	s.AddUp(100)
	s.AddDown(50)
	if s.UpBytes.Load() != 100 || s.DownBytes.Load() != 50 {
		t.Fatal("session counters")
	}
	st.Expire(s.ID, ReasonIdle)
	stats := st.Stats()
	if stats.UpBytes != 100 || stats.DownBytes != 50 || stats.Expired != 1 {
		t.Fatalf("store totals %+v", stats)
	}
}

func TestCloseCancelsAll(t *testing.T) {
	st, _ := newTestStore(3)
	st.Start()
	a, _ := st.Create(ip, 1, "u")
	b, _ := st.Create(ip, 1, "u")
	st.Close()
	st.Close()
	if a.Alive() || b.Alive() || a.Reason() != ReasonShutdown {
		t.Fatal("sessions not cancelled on close")
	}
	if _, err := st.Create(ip, 1, "u"); err != ErrClosed {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}

func TestJanitorRuns(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	st := NewStore(Options{TTL: time.Second, JanitorInterval: 10 * time.Millisecond, Now: clk.Now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	st.Start()
	defer st.Close()
	s, _ := st.Create(ip, 1, "u")
	clk.Advance(2 * time.Second)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("janitor did not expire session")
	}
}

func TestOnExpireAndElapsed(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	var got string
	st := NewStore(Options{Now: clk.Now, OnExpire: func(s *Session, r string) { got = r },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer st.Close()
	s, _ := st.Create(ip, 1, "u")
	clk.Advance(1500 * time.Millisecond)
	if s.Elapsed() != 1500*time.Millisecond {
		t.Fatalf("elapsed %v", s.Elapsed())
	}
	st.Expire(s.ID, ReasonFinished)
	if got != ReasonFinished {
		t.Fatalf("OnExpire reason %q", got)
	}
}
