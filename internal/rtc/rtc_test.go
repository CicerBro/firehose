package rtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/CicerBro/firehose/internal/config"
	"github.com/CicerBro/firehose/internal/server"
	"github.com/CicerBro/firehose/internal/session"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestProbeEncodeDecode(t *testing.T) {
	buf := make([]byte, DefaultSize)
	EncodeProbe(buf, KindUp, 0xdeadbeef, 1<<40+7)
	want := []byte{2, 0xde, 0xad, 0xbe, 0xef, 0, 0, 1, 0, 0, 0, 0, 7}
	if !bytes.Equal(buf[:ProbeHeaderSize], want) {
		t.Fatalf("header = % x, want % x", buf[:ProbeHeaderSize], want)
	}
	for _, b := range buf[ProbeHeaderSize:] {
		if b != 0 {
			t.Fatal("padding not zero")
		}
	}
	kind, seq, us, ok := DecodeProbe(buf)
	if !ok || kind != KindUp || seq != 0xdeadbeef || us != 1<<40+7 {
		t.Fatalf("decode = %d %d %d %v", kind, seq, us, ok)
	}
	if _, _, _, ok := DecodeProbe(buf[:12]); ok {
		t.Fatal("short probe decoded")
	}
}

func TestReceiver(t *testing.T) {
	r := newReceiver(10)
	for _, seq := range []uint32{0, 2, 1, 1, 3, 5, 4, 42} { // dup 1, 42 beyond limit
		r.add(seq, float64(seq)*1000, float64(seq)*1000+500)
	}
	if r.reordered != 2 { // 1 after 2, 4 after 5
		t.Fatalf("reordered = %d, want 2", r.reordered)
	}
	n, j := r.summary(10)
	if n != 6 {
		t.Fatalf("received = %d, want 6", n)
	}
	if n, _ := r.summary(3); n != 3 {
		t.Fatalf("received(sent=3) = %d, want 3", n)
	}
	if j < 0 || math.IsNaN(j) {
		t.Fatalf("jitter = %v", j)
	}
}

type testEnv struct {
	h     *Handler
	store *session.Store
	ts    *httptest.Server
}

func newEnv(t *testing.T, o Options) *testEnv {
	t.Helper()
	store := session.NewStore(session.Options{MaxActive: 64, Logger: quiet})
	o.ListenAddr = "127.0.0.1:0"
	o.IncludeLoopback = true
	o.Logger = quiet
	h, err := NewHandler(store, o)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(func() {
		ts.Close()
		start := time.Now()
		if err := h.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("Close took %v", d)
		}
		store.Close()
	})
	return &testEnv{h: h, store: store, ts: ts}
}

func (e *testEnv) offerURL(t *testing.T) string {
	t.Helper()
	s, err := e.store.Create(netip.MustParseAddr("127.0.0.1"), 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	return e.ts.URL + "/api/rtc/offer?sid=" + s.ID
}

// §13.2: drop every 10th probe in each direction → ≈ 10 % loss both ways.
func TestLossWithInjectedDrops(t *testing.T) {
	e := newEnv(t, Options{DropProbe: func(_ byte, seq uint32) bool { return seq%10 == 9 }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	res, err := RunClient(ctx, ClientOptions{OfferURL: e.offerURL(t), PPS: 100, DurationMs: 2000, Network: "udp4"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result %+v in %v", res, time.Since(start))
	if !res.Available || res.SentDown != 200 || res.SentUp != 200 {
		t.Fatalf("unexpected result %+v", res)
	}
	if math.Abs(res.DownPct-10) > 0.5 || math.Abs(res.UpPct-10) > 0.5 {
		t.Fatalf("loss down=%v up=%v, want ≈ 10", res.DownPct, res.UpPct)
	}
	if res.JitterDownMs < 0 || res.JitterUpMs < 0 {
		t.Fatalf("negative jitter %+v", res)
	}
}

func TestNoLoss(t *testing.T) {
	e := newEnv(t, Options{Grace: 200 * time.Millisecond})
	res, err := RunClient(context.Background(), ClientOptions{OfferURL: e.offerURL(t), PPS: 200, DurationMs: 500, Grace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || res.SentDown != 100 || res.SentUp != 100 || res.DownPct != 0 || res.UpPct != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
}

// blackhole discards every inbound datagram (UDP blocked).
type blackhole struct{ net.PacketConn }

func (b blackhole) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		if _, _, err := b.PacketConn.ReadFrom(p); err != nil {
			return 0, nil, err
		}
	}
}

// §8.3: UDP blocked → available:false (not an error) within ~5 s.
func TestUDPBlockedUnavailable(t *testing.T) {
	e := newEnv(t, Options{wrapConn: func(c net.PacketConn) net.PacketConn { return blackhole{c} }})
	start := time.Now()
	res, err := RunClient(context.Background(), ClientOptions{OfferURL: e.offerURL(t), Network: "udp4"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if res.Available {
		t.Fatalf("available with UDP blocked: %+v", res)
	}
	if elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("took %v, want ≈ 5 s", elapsed)
	}
}

func TestOfferErrors(t *testing.T) {
	e := newEnv(t, Options{})
	post := func(url, body string) int {
		resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(e.ts.URL+"/api/rtc/offer?sid=nope", `{"sdp":"x"}`); c != http.StatusGone {
		t.Fatalf("unknown sid: %d", c)
	}
	url := e.offerURL(t)
	if c := post(url, `nope`); c != http.StatusBadRequest {
		t.Fatalf("bad json: %d", c)
	}
	if c := post(url, `{"sdp":"garbage"}`); c != http.StatusBadRequest {
		t.Fatalf("bad sdp: %d", c)
	}
	_, err := RunClient(context.Background(), ClientOptions{OfferURL: e.ts.URL + "/api/rtc/offer?sid=nope"})
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusGone {
		t.Fatalf("client err = %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return e.h.ActivePeers() == 0 })
}

// The PC closes when its session ends.
func TestPeerClosesWithSession(t *testing.T) {
	running := make(chan struct{}, 1)
	e := newEnv(t, Options{DropProbe: func(kind byte, _ uint32) bool {
		if kind == KindUp {
			select {
			case running <- struct{}{}:
			default:
			}
		}
		return false
	}})
	url := e.offerURL(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunClient(ctx, ClientOptions{OfferURL: url, DurationMs: 5000})
		done <- err
	}()
	select {
	case <-running:
	case <-time.After(5 * time.Second):
		t.Fatal("test did not start")
	}
	closed := time.Now()
	e.store.Close() // ends every session
	waitFor(t, 3*time.Second, func() bool { return e.h.ActivePeers() == 0 })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("client succeeded after session end")
		}
		t.Logf("client returned %v after %v", err, time.Since(closed))
	case <-time.After(10 * time.Second):
		t.Fatal("client did not return after the server closed the PC")
	}
}

// M6 AC: no leaked peer connections or goroutines after many tests.
func TestNoLeaks(t *testing.T) {
	e := newEnv(t, Options{Grace: 100 * time.Millisecond}) // MaxPeers 1: each test needs the previous PC gone
	// Warm up once so lazily started pion/runtime goroutines are in the baseline.
	run := func() {
		res, err := RunClient(context.Background(), ClientOptions{OfferURL: e.offerURL(t), PPS: 100, DurationMs: 200, Grace: 100 * time.Millisecond})
		if err != nil || !res.Available {
			t.Fatalf("run: %+v %v", res, err)
		}
	}
	run()
	waitFor(t, 5*time.Second, func() bool { return e.h.ActivePeers() == 0 })
	time.Sleep(200 * time.Millisecond)
	base := runtime.NumGoroutine()
	n := 30
	if testing.Short() {
		n = 5
	}
	for range n {
		run()
	}
	waitFor(t, 5*time.Second, func() bool { return e.h.ActivePeers() == 0 })
	ok := false
	var g int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if g = runtime.NumGoroutine(); g <= base+2 {
			ok = true
			break
		}
	}
	if !ok {
		buf := make([]byte, 1<<20)
		t.Fatalf("goroutines %d, baseline %d\n%s", g, base, buf[:runtime.Stack(buf, true)])
	}
}

// Full stack: server.New + NewFactory, bearer auth, /api/config.
func TestServerIntegration(t *testing.T) {
	cfg := config.Default()
	cfg.PublicURL = "http://127.0.0.1"
	cfg.APIToken = "test-token"
	srv, err := server.New(cfg, server.WithLogger(quiet),
		server.WithRTC(NewFactory(Options{ListenAddr: "127.0.0.1:0", IncludeLoopback: true})))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer func() { ts.Close(); srv.Close() }()

	req, _ := http.NewRequest("GET", ts.URL+"/api/config", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ RTC struct{ Enabled bool } }
	_ = json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()
	if !c.RTC.Enabled {
		t.Fatal("rtc.enabled false")
	}

	req, _ = http.NewRequest("POST", ts.URL+"/api/session", bytes.NewBufferString(`{"connections":1}`))
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ SID string }
	_ = json.NewDecoder(resp.Body).Decode(&s)
	resp.Body.Close()
	if s.SID == "" {
		t.Fatalf("no sid (status %d)", resp.StatusCode)
	}
	url := ts.URL + "/api/rtc/offer?sid=" + s.SID

	_, err = RunClient(context.Background(), ClientOptions{OfferURL: url})
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("without token: %v", err)
	}
	res, err := RunClient(context.Background(), ClientOptions{
		OfferURL: url, Header: http.Header{"Authorization": {"Bearer test-token"}},
		PPS: 100, DurationMs: 500,
	})
	if err != nil || !res.Available || res.SentUp != 50 || res.SentDown != 50 {
		t.Fatalf("with token: %+v %v", res, err)
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("condition not met within %v", d)
}

// TestLive runs one default test (100 pps × 6 s) against a running server,
// e.g. for the §13.4 netem checks:
//
//	FIREHOSE_RTC_LIVE=http://127.0.0.1:8080 [FIREHOSE_RTC_TOKEN=…] go test -run TestLive -v ./internal/rtc/
func TestLive(t *testing.T) {
	base := os.Getenv("FIREHOSE_RTC_LIVE")
	if base == "" {
		t.Skip("FIREHOSE_RTC_LIVE not set")
	}
	hdr := http.Header{}
	if tok := os.Getenv("FIREHOSE_RTC_TOKEN"); tok != "" {
		hdr.Set("Authorization", "Bearer "+tok)
	}
	req, _ := http.NewRequest("POST", base+"/api/session", bytes.NewBufferString(`{"connections":1}`))
	req.Header = hdr.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ SID string }
	_ = json.NewDecoder(resp.Body).Decode(&s)
	resp.Body.Close()
	if s.SID == "" {
		t.Fatalf("no sid (status %d)", resp.StatusCode)
	}
	res, err := RunClient(context.Background(), ClientOptions{OfferURL: base + "/api/rtc/offer?sid=" + s.SID, Header: hdr})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res)
}
