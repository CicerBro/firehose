package client

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CicerBro/speedtest/internal/config"
	"github.com/CicerBro/speedtest/internal/server"
	"github.com/CicerBro/speedtest/internal/session"
)

const testToken = "test-token-0123456789abcdef"

// testServer is an in-process speedtest server on loopback.
type testServer struct {
	URL    string
	srv    *server.Server
	offset atomic.Int64 // store clock offset (ns), to fast-forward expiry
	mu     sync.Mutex
	ports  map[string]map[string]bool // path → remote ports seen
}

type serverOpts struct {
	listeners []net.Listener // default: 127.0.0.1:0
	publicURL string
	wrap      bool // serve srv.Handler() wrapped to record remote ports
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func startServer(t *testing.T, so serverOpts) *testServer {
	t.Helper()
	if len(so.listeners) == 0 {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		so.listeners = []net.Listener{ln}
	}
	if so.publicURL == "" {
		so.publicURL = "http://" + so.listeners[0].Addr().String()
	}
	ts := &testServer{URL: so.publicURL, ports: map[string]map[string]bool{}}
	cfg := config.Default()
	cfg.Listen = so.listeners[0].Addr().String()
	cfg.PublicURL = so.publicURL
	cfg.APIToken = testToken
	cfg.RTCEnabled = false
	cfg.ServerName = "itest"
	cfg.MaxUploadBytes = 16 << 20
	now := func() time.Time { return time.Now().Add(time.Duration(ts.offset.Load())) }
	srv, err := server.New(cfg, server.WithLogger(quietLogger()), server.WithVersion("9.9.9"), server.WithClock(now))
	if err != nil {
		t.Fatal(err)
	}
	ts.srv = srv
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if so.wrap {
		h := srv.Handler()
		hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, port, _ := net.SplitHostPort(r.RemoteAddr)
			ts.mu.Lock()
			if ts.ports[r.URL.Path] == nil {
				ts.ports[r.URL.Path] = map[string]bool{}
			}
			ts.ports[r.URL.Path][port] = true
			ts.mu.Unlock()
			h.ServeHTTP(w, r)
		})}
		for _, ln := range so.listeners {
			wg.Add(1)
			go func() { defer wg.Done(); _ = hs.Serve(ln) }()
		}
		t.Cleanup(func() {
			cancel()
			srv.Close()
			_ = hs.Close()
			wg.Wait()
		})
		return ts
	}
	ln := so.listeners[0]
	if len(so.listeners) > 1 {
		ln = newMultiListener(so.listeners...)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Serve(ctx, ln); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })
	return ts
}

func (ts *testServer) portsSeen(path string) int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.ports[path])
}

// multiListener accepts from several listeners (127.0.0.1 and ::1 on the
// same port, so "localhost" works with -4 and -6).
type multiListener struct {
	lns    []net.Listener
	conns  chan net.Conn
	errc   chan error
	once   sync.Once
	closed chan struct{}
}

func newMultiListener(lns ...net.Listener) *multiListener {
	m := &multiListener{lns: lns, conns: make(chan net.Conn), errc: make(chan error, len(lns)), closed: make(chan struct{})}
	for _, ln := range lns {
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					m.errc <- err
					return
				}
				select {
				case m.conns <- c:
				case <-m.closed:
					c.Close()
					return
				}
			}
		}()
	}
	return m
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case c := <-m.conns:
		return c, nil
	case <-m.closed:
		return nil, net.ErrClosed
	case err := <-m.errc:
		return nil, err
	}
}

func (m *multiListener) Close() error {
	m.once.Do(func() {
		close(m.closed)
		for _, ln := range m.lns {
			ln.Close()
		}
	})
	return nil
}

func (m *multiListener) Addr() net.Addr { return m.lns[0].Addr() }

func testOptions(url string, n int) Options {
	return Options{
		Server:          url,
		Token:           testToken,
		Connections:     n,
		Duration:        2 * time.Second,
		Grace:           500 * time.Millisecond,
		LatencyDuration: 1 * time.Second,
		Version:         "test",
		DownloadBytes:   256 << 20,
	}
}

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ts := startServer(t, serverOpts{})
	for _, n := range []int{1, 8} {
		t.Run("n="+strconv.Itoa(n), func(t *testing.T) {
			var progress atomic.Int32
			o := testOptions(ts.URL, n)
			o.Progress = func(Progress) { progress.Add(1) }
			o.Warn = func(s string) { t.Logf("warn: %s", s) }
			res, err := Run(context.Background(), o)
			if err != nil {
				t.Fatalf("Run: %v (exit %d)", err, ExitCode(err))
			}
			checkResult(t, res, n)
			if progress.Load() < 5 {
				t.Errorf("only %d progress callbacks", progress.Load())
			}

			// Server-counted upload bytes ≈ client-sent bytes (± one request).
			sess, ok := ts.srv.Sessions().Get(res.Stats.SID)
			if !ok {
				t.Fatal("session gone right after the run")
			}
			server, client := sess.UpBytes.Load(), res.Stats.UpBytesSent
			t.Logf("down %s, up %s; upload bytes: server %d, client sent %d", FormatBps(res.Download.Bps), FormatBps(res.Upload.Bps), server, client)
			if diff := client - server; diff < 0 || diff > 16<<20 {
				t.Errorf("upload bytes: server %d, client sent %d (diff %d, want 0…%d)", server, client, diff, 16<<20)
			}
			if res.Stats.DownBytes <= 0 || sess.DownBytes.Load() < res.Stats.DownBytes {
				t.Errorf("download bytes: client %d, server %d", res.Stats.DownBytes, sess.DownBytes.Load())
			}

			// Session expiry: the WS is closed, so the session expires 10 s
			// (store clock) later and is cleaned up.
			// Server handlers may still be unwinding the cancelled streams
			// and the WS (each touches the session on exit), so poll,
			// fast-forwarding the store clock past the 10 s grace each time.
			deadline := time.Now().Add(3 * time.Second)
			for {
				ts.offset.Add(int64(11 * time.Second))
				ts.srv.Sessions().Sweep()
				if _, ok := ts.srv.Sessions().Get(res.Stats.SID); !ok {
					break
				}
				if time.Now().After(deadline) {
					t.Errorf("session still active after expiry (active up %d down %d)", sess.ActiveUp.Load(), sess.ActiveDown.Load())
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if r := sess.Reason(); r != session.ReasonFinished {
				t.Errorf("session ended with reason %q, want %q", r, session.ReasonFinished)
			}
			if l := ts.srv.Sessions().Len(); l != 0 {
				t.Errorf("%d sessions left after expiry", l)
			}
		})
	}
}

func checkResult(t *testing.T, res Result, n int) {
	t.Helper()
	if res.Version != 1 || res.Timestamp == "" {
		t.Errorf("version/timestamp: %d %q", res.Version, res.Timestamp)
	}
	if res.Server.Name != "itest" || res.Server.Version != "9.9.9" {
		t.Errorf("server: %+v", res.Server)
	}
	if res.Client.UserAgent != "speedtest-cli/test" || res.Client.IP == "" {
		t.Errorf("client: %+v", res.Client)
	}
	if res.Config.Connections != n || res.Config.DurationSec != 2 || res.Config.GraceSec != 0.5 || res.Config.Loss {
		t.Errorf("config: %+v", res.Config)
	}
	if !res.Flags.SingleOrigin || res.Flags.Aborted {
		t.Errorf("flags: %+v", res.Flags)
	}
	if res.Download.Bps <= 0 || res.Download.Bytes <= 0 || res.Download.P90Bps <= 0 || res.Download.Short {
		t.Errorf("download: %+v", res.Download)
	}
	if res.Upload.Bps <= 0 || res.Upload.Bytes <= 0 || res.Upload.Approx || res.Upload.Short {
		t.Errorf("upload: %+v", res.Upload)
	}
	if res.Latency.Idle.Samples < 3 || res.Latency.Idle.MedianMs <= 0 {
		t.Errorf("idle latency: %+v", res.Latency.Idle)
	}
	if res.Latency.Download.Samples < 1 || res.Latency.Upload.Samples < 1 {
		t.Errorf("loaded latency: %+v", res.Latency)
	}
	if res.Loss.Available {
		t.Errorf("loss: %+v", res.Loss)
	}
	if len(res.Stats.StreamOrigins) != n {
		t.Errorf("stream origins: %v", res.Stats.StreamOrigins)
	}
}

// TestDistinctConnections verifies that -n 8 really uses 8 TCP connections.
func TestDistinctConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ts := startServer(t, serverOpts{wrap: true})
	o := testOptions(ts.URL, 8)
	o.Phases = Phases{Download: true}
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Download.Bps <= 0 {
		t.Errorf("download: %+v", res.Download)
	}
	if got := ts.portsSeen("/api/down"); got != 8 {
		t.Errorf("server saw %d distinct remote ports on /api/down, want 8", got)
	}
}

func TestIPFamilies(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ln4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln4.Addr().(*net.TCPAddr).Port
	lns := []net.Listener{ln4}
	ln6, err6 := net.Listen("tcp6", "[::1]:"+strconv.Itoa(port))
	if err6 == nil {
		lns = append(lns, ln6)
	}
	ts := startServer(t, serverOpts{listeners: lns, publicURL: "http://localhost:" + strconv.Itoa(port)})

	run := func(t *testing.T, family int, want string) {
		o := testOptions(ts.URL, 2)
		o.IPFamily = family
		o.Duration = 1500 * time.Millisecond
		res, err := Run(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if res.Client.Family != want {
			t.Errorf("family %q (ip %s), want %s", res.Client.Family, res.Client.IP, want)
		}
		if res.Download.Bps <= 0 || res.Upload.Bps <= 0 {
			t.Errorf("throughput: down %v up %v", res.Download.Bps, res.Upload.Bps)
		}
	}
	t.Run("-4", func(t *testing.T) { run(t, 4, "ipv4") })
	t.Run("-6", func(t *testing.T) {
		if err6 != nil {
			t.Skipf("no IPv6 loopback: %v", err6)
		}
		run(t, 6, "ipv6")
	})
}

func TestAuth(t *testing.T) {
	ts := startServer(t, serverOpts{})
	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", ExitAuth},
		{"wrong token", "nope", ExitAuth},
		{"valid token", testToken, ExitOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := testOptions(ts.URL, 1)
			o.Token = c.token
			o.Phases = Phases{Latency: true}
			o.LatencyDuration = 500 * time.Millisecond
			_, err := Run(context.Background(), o)
			if got := ExitCode(err); got != c.want {
				t.Fatalf("exit %d, want %d (err %v)", got, c.want, err)
			}
			var ae *AuthError
			if c.want == ExitAuth && (!errors.As(err, &ae) || ae.Status != http.StatusUnauthorized) {
				t.Errorf("want 401 AuthError, got %v", err)
			}
		})
	}
}

// TestAuthRedirect: an Authelia login redirect is an auth failure (exit 3)
// and is not followed.
func TestAuthRedirect(t *testing.T) {
	var hits atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			hits.Add(1)
		}
		http.Redirect(w, r, "/login?rd=x", http.StatusFound)
	}))
	defer hs.Close()
	_, err := Run(context.Background(), testOptions(hs.URL, 1))
	var ae *AuthError
	if ExitCode(err) != ExitAuth || !errors.As(err, &ae) || ae.Status != http.StatusFound {
		t.Fatalf("got %v (exit %d), want redirect AuthError", err, ExitCode(err))
	}
	if hits.Load() != 0 {
		t.Error("redirect was followed")
	}
}

func TestUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = Run(context.Background(), testOptions("http://"+addr, 1))
	if ExitCode(err) != ExitUnreachable {
		t.Fatalf("got %v (exit %d), want exit 2", err, ExitCode(err))
	}
}

func TestNotASpeedtestServer(t *testing.T) {
	hs := httptest.NewServer(http.NotFoundHandler())
	defer hs.Close()
	_, err := Run(context.Background(), testOptions(hs.URL, 1))
	if ExitCode(err) != ExitUnreachable {
		t.Fatalf("got %v (exit %d), want exit 2", err, ExitCode(err))
	}
}

func TestTooManyConnections(t *testing.T) {
	ts := startServer(t, serverOpts{})
	_, err := Run(context.Background(), testOptions(ts.URL, 33))
	if ExitCode(err) != ExitUsage {
		t.Fatalf("got %v (exit %d), want exit 1", err, ExitCode(err))
	}
}

// TestAbort cancels mid-download: partial results, aborted flag, exit 130,
// and no goroutines left behind.
func TestAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ts := startServer(t, serverOpts{})
	// Warm up once so lazily started runtime/net goroutines are counted.
	o := testOptions(ts.URL, 2)
	o.Phases = Phases{Latency: true}
	o.LatencyDuration = 700 * time.Millisecond
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	o = testOptions(ts.URL, 4)
	o.Duration = 10 * time.Second
	o.Progress = func(p Progress) {
		if p.Phase == PhaseDownload && p.Elapsed > 1500*time.Millisecond {
			cancel()
		}
	}
	start := time.Now()
	res, err := Run(ctx, o)
	if ExitCode(err) != ExitInterrupted || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v (exit %d), want 130", err, ExitCode(err))
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("abort took %v", d)
	}
	if !res.Flags.Aborted {
		t.Error("aborted flag not set")
	}
	if res.Download.Bps <= 0 || !res.Phases.Download || res.Phases.Upload {
		t.Errorf("partial download: %+v phases %+v", res.Download, res.Phases)
	}
	if res.Latency.Idle.Samples == 0 {
		t.Error("idle latency missing from partial result")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= before {
			break
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines: %d before, %d after abort\n%s", before, n, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ts := startServer(t, serverOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	o := testOptions(ts.URL, 1)
	res, err := Run(ctx, o)
	if ExitCode(err) != ExitPhaseFailed {
		t.Fatalf("got %v (exit %d), want 4", err, ExitCode(err))
	}
	if !res.Flags.Aborted {
		t.Error("aborted flag not set on timeout")
	}
}
