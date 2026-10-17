// Package session implements the test-session store (§5.8): sessions with
// atomic byte/stream counters, idle/max-age/WS-close expiry, a janitor and
// "newest wins" preemption at the active-session limit.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Expiry reasons passed to logs and OnExpire.
const (
	ReasonIdle      = "idle"
	ReasonMaxAge    = "max_age"
	ReasonFinished  = "finished" // WS closed and FinishGrace elapsed
	ReasonPreempted = "preempted"
	ReasonShutdown  = "shutdown"
)

// Session is one speed test. Counters are updated lock-free by handlers.
type Session struct {
	ID          string
	ClientIP    netip.Addr
	User        string // Remote-User value, "token" for bearer auth, "dev" in dev mode
	Connections int
	Start       time.Time // from the store clock (monotonic with the real clock)

	UpBytes    atomic.Int64
	DownBytes  atomic.Int64
	ActiveUp   atomic.Int32
	ActiveDown atomic.Int32

	store      *Store
	lastSeen   atomic.Int64 // store-clock unix nanos
	wsOpen     atomic.Int32
	finishedAt atomic.Int64 // unix nanos of last WS close while no WS is open; 0 = not finishing
	ctx        context.Context
	cancel     context.CancelFunc
	reason     atomic.Value // string, set once on expiry
}

// Context is cancelled when the session expires, is preempted or the store
// shuts down.
func (s *Session) Context() context.Context { return s.ctx }

// Done is shorthand for Context().Done().
func (s *Session) Done() <-chan struct{} { return s.ctx.Done() }

// Alive reports whether the session has not been cancelled.
func (s *Session) Alive() bool { return s.ctx.Err() == nil }

// Reason returns why the session ended ("" while alive).
func (s *Session) Reason() string {
	r, _ := s.reason.Load().(string)
	return r
}

// Touch records activity (a request started or ended, a keep-alive). It
// postpones idle expiry and the WS-close finishing deadline.
func (s *Session) Touch() { s.lastSeen.Store(s.store.now().UnixNano()) }

// Elapsed is the time since session start on the store clock.
func (s *Session) Elapsed() time.Duration { return s.store.now().Sub(s.Start) }

// WSOpened marks a control WebSocket as connected (the session is not idle
// while one is open).
func (s *Session) WSOpened() {
	s.wsOpen.Add(1)
	s.finishedAt.Store(0)
	s.Touch()
}

// WSClosed marks a control WebSocket as closed. When the last one closes the
// session is "finishing" and expires FinishGrace later unless requests arrive.
func (s *Session) WSClosed() {
	now := s.store.now().UnixNano()
	if s.wsOpen.Add(-1) <= 0 {
		s.finishedAt.Store(now)
	}
	s.lastSeen.Store(now)
}

// AddUp counts n uploaded bytes (session and store totals). Call after every Read.
func (s *Session) AddUp(n int64) {
	s.UpBytes.Add(n)
	s.store.totalUp.Add(n)
}

// AddDown counts n downloaded bytes (session and store totals).
func (s *Session) AddDown(n int64) {
	s.DownBytes.Add(n)
	s.store.totalDown.Add(n)
}

// AcquireDown reserves a download stream slot; false if max are active.
func (s *Session) AcquireDown(max int) bool { return acquire(&s.ActiveDown, max) }

// ReleaseDown frees a slot taken with AcquireDown.
func (s *Session) ReleaseDown() { s.ActiveDown.Add(-1) }

// AcquireUp reserves an upload stream slot; false if max are active.
func (s *Session) AcquireUp(max int) bool { return acquire(&s.ActiveUp, max) }

// ReleaseUp frees a slot taken with AcquireUp.
func (s *Session) ReleaseUp() { s.ActiveUp.Add(-1) }

func acquire(c *atomic.Int32, max int) bool {
	if int(c.Add(1)) > max {
		c.Add(-1)
		return false
	}
	return true
}

// Options configure a Store. Zero values get defaults.
type Options struct {
	MaxActive       int              // default 1
	TTL             time.Duration    // idle expiry, default 120 s
	MaxAge          time.Duration    // hard expiry, default 5 min
	FinishGrace     time.Duration    // expiry after WS close, default 10 s
	JanitorInterval time.Duration    // default 5 s
	Now             func() time.Time // injectable clock, default time.Now
	Logger          *slog.Logger     // default slog.Default()
	// OnExpire, if set, is called (outside the store lock) after a session
	// is cancelled, with the reason.
	OnExpire func(s *Session, reason string)
}

// Stats are cumulative store counters (for /metrics).
type Stats struct {
	Active    int
	Created   uint64
	Preempted uint64
	Expired   uint64 // all ends, including preempted and shutdown
	UpBytes   int64  // total over all sessions, live and ended
	DownBytes int64
}

// Store holds the active sessions.
type Store struct {
	opts Options
	now  func() time.Time
	log  *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*Session
	closed   bool

	created, preempted, expired atomic.Uint64
	totalUp, totalDown          atomic.Int64

	stopJanitor context.CancelFunc
	janitorDone chan struct{}
}

// ErrClosed is returned by Create after Close.
var ErrClosed = errors.New("session store closed")

// NewStore creates a store. Call Start to run the janitor and Close to stop it.
func NewStore(opts Options) *Store {
	if opts.MaxActive <= 0 {
		opts.MaxActive = 1
	}
	if opts.TTL <= 0 {
		opts.TTL = 120 * time.Second
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = 5 * time.Minute
	}
	if opts.FinishGrace <= 0 {
		opts.FinishGrace = 10 * time.Second
	}
	if opts.JanitorInterval <= 0 {
		opts.JanitorInterval = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Store{
		opts:     opts,
		now:      opts.Now,
		log:      opts.Logger,
		sessions: make(map[string]*Session),
	}
}

// TTL returns the idle expiry.
func (st *Store) TTL() time.Duration { return st.opts.TTL }

// Start runs the janitor goroutine (every JanitorInterval) until Close.
func (st *Store) Start() {
	st.mu.Lock()
	if st.stopJanitor != nil || st.closed {
		st.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.stopJanitor = cancel
	st.janitorDone = make(chan struct{})
	st.mu.Unlock()
	go func() {
		defer close(st.janitorDone)
		t := time.NewTicker(st.opts.JanitorInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				st.Sweep()
			}
		}
	}()
}

// Close stops the janitor and cancels every session (reason "shutdown").
// Idempotent.
func (st *Store) Close() {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	st.closed = true
	stop, done := st.stopJanitor, st.janitorDone
	all := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		all = append(all, s)
	}
	clear(st.sessions)
	st.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	for _, s := range all {
		st.end(s, ReasonShutdown)
	}
}

// Create makes a new session. At the MaxActive limit the oldest sessions are
// cancelled first (newest wins).
func (st *Store) Create(clientIP netip.Addr, connections int, user string) (*Session, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := st.now()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:          id,
		ClientIP:    clientIP,
		User:        user,
		Connections: connections,
		Start:       now,
		store:       st,
		ctx:         ctx,
		cancel:      cancel,
	}
	s.lastSeen.Store(now.UnixNano())

	var victims []*Session
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		cancel()
		return nil, ErrClosed
	}
	if n := len(st.sessions) - st.opts.MaxActive + 1; n > 0 {
		victims = st.oldestLocked(n)
		for _, v := range victims {
			delete(st.sessions, v.ID)
		}
	}
	st.sessions[id] = s
	st.mu.Unlock()
	st.created.Add(1)

	for _, v := range victims {
		st.preempted.Add(1)
		st.end(v, ReasonPreempted)
	}
	return s, nil
}

func (st *Store) oldestLocked(n int) []*Session {
	all := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	return all[:n]
}

// Get returns a live session by ID.
func (st *Store) Get(id string) (*Session, bool) {
	st.mu.RLock()
	s, ok := st.sessions[id]
	st.mu.RUnlock()
	if !ok || !s.Alive() {
		return nil, false
	}
	return s, true
}

// Len is the number of active sessions.
func (st *Store) Len() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.sessions)
}

// Expire ends a session now with the given reason. False if it was not active.
func (st *Store) Expire(id, reason string) bool {
	st.mu.Lock()
	s, ok := st.sessions[id]
	if ok {
		delete(st.sessions, id)
	}
	st.mu.Unlock()
	if ok {
		st.end(s, reason)
	}
	return ok
}

// Sweep runs one janitor pass: expire sessions past max age, idle past TTL,
// or finishing (WS closed) past FinishGrace. Idle/finishing expiry is skipped
// while transfer streams or a WS are active.
func (st *Store) Sweep() {
	now := st.now()
	type victim struct {
		s      *Session
		reason string
	}
	var victims []victim
	st.mu.Lock()
	for id, s := range st.sessions {
		reason := st.expiryReason(s, now)
		if reason == "" {
			continue
		}
		delete(st.sessions, id)
		victims = append(victims, victim{s, reason})
	}
	st.mu.Unlock()
	for _, v := range victims {
		st.end(v.s, v.reason)
	}
}

func (st *Store) expiryReason(s *Session, now time.Time) string {
	if !s.Alive() {
		return ReasonShutdown
	}
	if now.Sub(s.Start) >= st.opts.MaxAge {
		return ReasonMaxAge
	}
	if s.ActiveUp.Load() > 0 || s.ActiveDown.Load() > 0 || s.wsOpen.Load() > 0 {
		return ""
	}
	last := s.lastSeen.Load()
	if fin := s.finishedAt.Load(); fin != 0 {
		ref := max(fin, last)
		if now.UnixNano()-ref >= int64(st.opts.FinishGrace) {
			return ReasonFinished
		}
	}
	if now.UnixNano()-last >= int64(st.opts.TTL) {
		return ReasonIdle
	}
	return ""
}

// end cancels a session already removed from the map and logs a summary.
func (st *Store) end(s *Session, reason string) {
	s.reason.CompareAndSwap(nil, reason)
	s.cancel()
	st.expired.Add(1)
	up, down := s.UpBytes.Load(), s.DownBytes.Load()
	st.log.Info("session ended",
		"sid", s.ID,
		"reason", reason,
		"client_ip", s.ClientIP.String(),
		"user", s.User,
		"down_bytes", down,
		"up_bytes", up,
		"duration", st.now().Sub(s.Start).Round(time.Millisecond).String(),
		"connections", s.Connections,
	)
	if st.opts.OnExpire != nil {
		st.opts.OnExpire(s, reason)
	}
}

// Stats returns cumulative counters (bytes counted via AddUp/AddDown).
func (st *Store) Stats() Stats {
	return Stats{
		Active:    st.Len(),
		Created:   st.created.Load(),
		Preempted: st.preempted.Load(),
		Expired:   st.expired.Load(),
		UpBytes:   st.totalUp.Load(),
		DownBytes: st.totalDown.Load(),
	}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
