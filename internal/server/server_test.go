package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/CicerBro/speedtest/internal/config"
	"github.com/CicerBro/speedtest/internal/session"
)

const (
	publicURL = "https://speed.example.com"
	token     = "test-token"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var testUI = fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><title>t</title>")},
	"assets/app-abc.js":  {Data: []byte("console.log(1)")},
	"assets/app-abc.css": {Data: []byte("body{}")},
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.PublicURL = publicURL
	cfg.TestHosts = []string{
		"https://s1.speed.example.com", "https://s2.speed.example.com",
		"https://s3.speed.example.com", "https://s4.speed.example.com",
	}
	cfg.APIToken = token
	cfg.MaxDownloadBytes = 10 << 20
	cfg.MaxUploadBytes = 10 << 20
	cfg.MaxConnections = 16
	return cfg
}

func newTestServer(t *testing.T, cfg config.Config, opts ...Option) (*Server, *httptest.Server) {
	t.Helper()
	opts = append([]Option{WithLogger(quiet), WithUI(testUI), WithVersion("1.2.3")}, opts...)
	s, err := New(cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	return s, ts
}

func do(t *testing.T, method, url string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

var bearer = map[string]string{"Authorization": "Bearer " + token}

func createSession(t *testing.T, ts *httptest.Server, n int) map[string]any {
	t.Helper()
	resp := do(t, "POST", ts.URL+"/api/session", strings.NewReader(`{"connections":`+strconv.Itoa(n)+`}`), bearer)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create session: %d %s", resp.StatusCode, b)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func TestHealthzNoAuth(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	resp := do(t, "GET", ts.URL+"/healthz", nil, nil)
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("%d %q", resp.StatusCode, b)
	}
}

func TestAuthRequired(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	for _, p := range []string{"/api/config", "/api/ip", "/", "/api/nope", "/api/down?sid=x&bytes=1"} {
		resp := do(t, "GET", ts.URL+p, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without auth: %d", p, resp.StatusCode)
		}
		var e map[string]string
		json.NewDecoder(resp.Body).Decode(&e)
		if e["error"] != "unauthorized" {
			t.Errorf("%s body %v", p, e)
		}
		if resp := do(t, "GET", ts.URL+p, nil, map[string]string{"Authorization": "Bearer wrong", "Remote-User": "x"}); resp.StatusCode != 401 {
			t.Errorf("%s with wrong token: %d", p, resp.StatusCode)
		}
	}
	if resp := do(t, "GET", ts.URL+"/api/config", nil, bearer); resp.StatusCode != 200 {
		t.Fatalf("bearer: %d", resp.StatusCode)
	}
	if resp := do(t, "GET", ts.URL+"/api/config", nil, map[string]string{"Remote-User": "alice"}); resp.StatusCode != 200 {
		t.Fatalf("remote-user: %d", resp.StatusCode)
	}
}

func TestConfigEndpoint(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	resp := do(t, "GET", ts.URL+"/api/config", nil, bearer)
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", resp.Header)
	}
	var c struct {
		ServerName   string
		Version      string
		TestHosts    []string
		Limits       map[string]int64
		RTC          struct{ Enabled bool }
		SingleOrigin bool
		Dev          *bool
	}
	json.NewDecoder(resp.Body).Decode(&c)
	if c.Version != "1.2.3" || len(c.TestHosts) != 4 || c.SingleOrigin || c.Limits["maxConnections"] != 16 ||
		c.Limits["maxDownloadBytes"] != 10<<20 || c.RTC.Enabled || c.Dev != nil {
		t.Fatalf("config %+v", c)
	}
}

func TestConfigDevAndSingleOrigin(t *testing.T) {
	cfg := config.Default()
	cfg.Dev = true
	_, ts := newTestServer(t, cfg)
	resp := do(t, "GET", ts.URL+"/api/config", nil, nil)
	var c map[string]any
	json.NewDecoder(resp.Body).Decode(&c)
	if c["dev"] != true || c["singleOrigin"] != true {
		t.Fatalf("config %v", c)
	}
	hosts := c["testHosts"].([]any)
	if len(hosts) != 1 || hosts[0] != "http://localhost:8080" {
		t.Fatalf("hosts %v", hosts)
	}
}

func TestIPEndpoint(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	resp := do(t, "GET", ts.URL+"/api/ip", nil, map[string]string{"Remote-User": "a", "X-Forwarded-For": "2001:db8::7"})
	var ip map[string]string
	json.NewDecoder(resp.Body).Decode(&ip)
	if ip["ip"] != "2001:db8::7" || ip["family"] != "ipv6" {
		t.Fatalf("%v", ip)
	}
	resp = do(t, "GET", ts.URL+"/api/ip", nil, map[string]string{"Remote-User": "a", "X-Forwarded-For": "::ffff:198.51.100.1"})
	json.NewDecoder(resp.Body).Decode(&ip)
	if ip["ip"] != "198.51.100.1" || ip["family"] != "ipv4" {
		t.Fatalf("%v", ip)
	}
}

func TestSessionEndpoint(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	out := createSession(t, ts, 5)
	if sid := out["sid"].(string); len(sid) != 32 {
		t.Fatalf("sid %q", sid)
	}
	if hosts := out["testHosts"].([]any); len(hosts) != 2 {
		t.Fatalf("5 connections need 2 hosts, got %v", hosts)
	}
	if out["expiresInSec"].(float64) != 120 || out["connections"].(float64) != 5 {
		t.Fatalf("%v", out)
	}
	for _, body := range []string{`{"connections":0}`, `{"connections":17}`, `{}`, `nope`, `{"connections":"8"}`} {
		resp := do(t, "POST", ts.URL+"/api/session", strings.NewReader(body), bearer)
		if resp.StatusCode != 400 {
			t.Errorf("%s: %d", body, resp.StatusCode)
		}
	}
}

func TestSessionHostsAndClamp(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConnections = 32
	cfg.TestHosts = cfg.TestHosts[:2]
	_, ts := newTestServer(t, cfg)
	out := createSession(t, ts, 9)
	if len(out["testHosts"].([]any)) != 2 { // needs 3, only 2 configured
		t.Fatalf("%v", out)
	}

	cfg = testConfig()
	cfg.TestHosts = nil
	_, ts = newTestServer(t, cfg)
	resp := do(t, "POST", ts.URL+"/api/session", strings.NewReader(`{"connections":12}`), bearer)
	var o map[string]any
	json.NewDecoder(resp.Body).Decode(&o)
	hosts := o["testHosts"].([]any)
	if o["connections"].(float64) != 6 || len(hosts) != 1 || hosts[0] != publicURL {
		t.Fatalf("single origin: %v", o)
	}
}

func TestNewestWinsViaAPI(t *testing.T) {
	s, ts := newTestServer(t, testConfig())
	a := createSession(t, ts, 1)["sid"].(string)
	b := createSession(t, ts, 1)["sid"].(string)
	if _, ok := s.Sessions().Get(a); ok {
		t.Fatal("old session should be preempted")
	}
	if _, ok := s.Sessions().Get(b); !ok {
		t.Fatal("new session missing")
	}
	resp := do(t, "GET", ts.URL+"/api/down?bytes=10&sid="+a, nil, bearer)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("preempted sid: %d", resp.StatusCode)
	}
}

func TestTransferCORS(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	sid := createSession(t, ts, 4)["sid"].(string)

	// Allowed origin: credentialed CORS headers, exact origin.
	resp := do(t, "GET", ts.URL+"/api/down?bytes=1000&sid="+sid, nil,
		map[string]string{"Origin": publicURL, "Remote-User": "a", "Accept-Encoding": "gzip"})
	n, _ := io.Copy(io.Discard, resp.Body)
	h := resp.Header
	if resp.StatusCode != 200 || n != 1000 || h.Get("Access-Control-Allow-Origin") != publicURL ||
		h.Get("Access-Control-Allow-Credentials") != "true" || !strings.Contains(h.Get("Vary"), "Origin") ||
		h.Get("Content-Encoding") != "" || h.Get("Content-Length") != "1000" {
		t.Fatalf("status=%d n=%d headers=%v", resp.StatusCode, n, h)
	}

	// Foreign origin: no CORS headers.
	resp = do(t, "POST", ts.URL+"/api/up?sid="+sid, strings.NewReader("hello"),
		map[string]string{"Origin": "https://evil.example", "Remote-User": "a"})
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "" ||
		resp.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("foreign origin: %d %v", resp.StatusCode, resp.Header)
	}
	var up map[string]int64
	json.NewDecoder(resp.Body).Decode(&up)
	if up["bytes"] != 5 {
		t.Fatalf("up %v", up)
	}

	// Error responses still carry CORS headers so the browser can read them.
	resp = do(t, "GET", ts.URL+"/api/down?bytes=1&sid=bad", nil, map[string]string{"Origin": publicURL, "Remote-User": "a"})
	if resp.StatusCode != http.StatusGone || resp.Header.Get("Access-Control-Allow-Origin") != publicURL {
		t.Fatalf("410 cors: %d %v", resp.StatusCode, resp.Header)
	}
	resp = do(t, "GET", ts.URL+"/api/down?bytes=99999999999&sid="+sid, nil, map[string]string{"Remote-User": "a"})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("413: %d", resp.StatusCode)
	}

	// Same-origin endpoints never get CORS headers.
	resp = do(t, "GET", ts.URL+"/api/config", nil, map[string]string{"Origin": publicURL, "Remote-User": "a"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS on /api/config")
	}
}

func TestOptionsPreflight(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	for _, p := range []string{"/api/down", "/api/up"} {
		resp := do(t, "OPTIONS", ts.URL+p+"?sid=x", nil, map[string]string{
			"Origin": publicURL, "Access-Control-Request-Method": "POST",
		})
		h := resp.Header
		if resp.StatusCode != http.StatusNoContent || h.Get("Access-Control-Allow-Methods") != "GET, POST" ||
			h.Get("Access-Control-Max-Age") != "7200" || h.Get("Access-Control-Allow-Origin") != publicURL ||
			h.Get("Access-Control-Allow-Credentials") != "true" {
			t.Fatalf("%s: %d %v", p, resp.StatusCode, h)
		}
		resp = do(t, "OPTIONS", ts.URL+p, nil, map[string]string{"Origin": "https://evil.example"})
		if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s foreign: %d %v", p, resp.StatusCode, resp.Header)
		}
	}
}

func TestStreamCap(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConnections = 1
	s, ts := newTestServer(t, cfg)
	sid := createSession(t, ts, 1)["sid"].(string)
	sess, _ := s.Sessions().Get(sid)
	sess.AcquireDown(3)
	sess.AcquireDown(3)
	sess.AcquireDown(3)
	resp := do(t, "GET", ts.URL+"/api/down?bytes=1&sid="+sid, nil, bearer)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestStaticHeaders(t *testing.T) {
	_, ts := newTestServer(t, testConfig())
	auth := map[string]string{"Remote-User": "a"}
	resp := do(t, "GET", ts.URL+"/", nil, auth)
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-cache" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("index: %d %v", resp.StatusCode, resp.Header)
	}
	for _, want := range []string{"default-src 'self'", "https://s1.speed.example.com", "https://s4.speed.example.com",
		"wss://speed.example.com", "frame-ancestors 'none'", "script-src 'self'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	resp = do(t, "GET", ts.URL+"/assets/app-abc.js", nil, auth)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset: %d %v", resp.StatusCode, resp.Header)
	}
	resp = do(t, "GET", ts.URL+"/assets/missing.js", nil, auth)
	if resp.StatusCode != 404 || strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("missing asset: %d %v", resp.StatusCode, resp.Header)
	}
	resp = do(t, "GET", ts.URL+"/api/unknown", nil, auth)
	if resp.StatusCode != 404 || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("api 404: %d", resp.StatusCode)
	}
}

func TestDevCSPUsesWS(t *testing.T) {
	cfg := config.Default()
	cfg.Dev = true
	cfg.TestHosts = []string{"http://127.0.0.1:8080"}
	s, err := New(cfg, WithLogger(quiet), WithUI(testUI))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.Contains(s.csp, "connect-src 'self' http://127.0.0.1:8080 ws://localhost:8080;") {
		t.Fatal(s.csp)
	}
}

type fakeRTC struct{ closed bool }

func (f *fakeRTC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"sdp":"answer"}`)
}
func (f *fakeRTC) Close() error { f.closed = true; return nil }

func TestRTCHook(t *testing.T) {
	rtc := &fakeRTC{}
	var gotStore *session.Store
	s, ts := newTestServer(t, testConfig(), WithRTC(func(cfg config.Config, st *session.Store, l *slog.Logger) (http.Handler, error) {
		gotStore = st
		return rtc, nil
	}))
	if gotStore != s.Sessions() {
		t.Fatal("factory got wrong store")
	}
	if resp := do(t, "POST", ts.URL+"/api/rtc/offer?sid=x", strings.NewReader(`{}`), nil); resp.StatusCode != 401 {
		t.Fatalf("rtc without auth: %d", resp.StatusCode)
	}
	if resp := do(t, "POST", ts.URL+"/api/rtc/offer?sid=x", strings.NewReader(`{}`), bearer); resp.StatusCode != 200 {
		t.Fatalf("rtc: %d", resp.StatusCode)
	}
	resp := do(t, "GET", ts.URL+"/api/config", nil, bearer)
	var c map[string]map[string]any
	json.NewDecoder(resp.Body).Decode(&c)
	if c["rtc"]["enabled"] != true {
		t.Fatalf("rtc not enabled in config: %v", c["rtc"])
	}
	s.Close()
	if !rtc.closed {
		t.Fatal("rtc handler not closed")
	}
}

func TestMetrics(t *testing.T) {
	s, ts := newTestServer(t, testConfig())
	sid := createSession(t, ts, 1)["sid"].(string)
	resp := do(t, "GET", ts.URL+"/api/down?bytes=4096&sid="+sid, nil, bearer)
	io.Copy(io.Discard, resp.Body)
	createSession(t, ts, 1)
	rec := httptest.NewRecorder()
	s.MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"speedtest_sessions_active 1", "speedtest_sessions_created_total 2",
		"speedtest_sessions_preempted_total 1", "speedtest_download_bytes_total 4096",
		"# TYPE speedtest_upload_bytes_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	cfg := testConfig()
	cfg.MaxDownloadBytes = 1 << 40
	cfg.MetricsListen = "127.0.0.1:0"
	s, err := New(cfg, WithLogger(quiet), WithUI(testUI))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()

	resp := do(t, "POST", base+"/api/session", strings.NewReader(`{"connections":1}`), bearer)
	var o map[string]any
	json.NewDecoder(resp.Body).Decode(&o)
	sid := o["sid"].(string)

	// A long download in flight must be stopped by shutdown (session cancel).
	dl := do(t, "GET", base+"/api/down?bytes=1099511627776&sid="+sid, nil, bearer)
	buf := make([]byte, 1<<16)
	if _, err := io.ReadFull(dl.Body, buf); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(ShutdownTimeout + 2*time.Second):
		t.Fatal("shutdown hung")
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("shutdown took %v; transfers should be cancelled", el)
	}
	if s.Sessions().Len() != 0 {
		t.Fatal("sessions remain after shutdown")
	}
}
