package transfer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/CicerBro/firehose/internal/session"
)

func setup(t *testing.T, opts Options) (*httptest.Server, *session.Store, *session.Session) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := session.NewStore(session.Options{MaxActive: 4, Logger: log})
	t.Cleanup(st.Close)
	if opts.MaxDownloadBytes == 0 {
		opts.MaxDownloadBytes = 64 << 20
	}
	if opts.MaxUploadBytes == 0 {
		opts.MaxUploadBytes = 64 << 20
	}
	if opts.MaxRequestDuration == 0 {
		opts.MaxRequestDuration = 10 * time.Second
	}
	if opts.MaxStreams == 0 {
		opts.MaxStreams = 4
	}
	opts.Logger = log
	h, err := New(st, opts)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/down", h.Down)
	mux.HandleFunc("POST /api/up", h.Up)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	sess, err := st.Create(netip.MustParseAddr("127.0.0.1"), 4, "test")
	if err != nil {
		t.Fatal(err)
	}
	return ts, st, sess
}

func TestDownloadExactLength(t *testing.T) {
	ts, _, sess := setup(t, Options{})
	for _, n := range []int64{1, 1000, 1000000, RandomBufferSize + 12345, 3*MaxWriteChunk + 7} {
		resp, err := http.Get(fmt.Sprintf("%s/api/down?sid=%s&bytes=%d", ts.URL, sess.ID, n))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || int64(len(body)) != n || resp.ContentLength != n {
			t.Fatalf("n=%d: status=%d len=%d contentLength=%d", n, resp.StatusCode, len(body), resp.ContentLength)
		}
		if resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Content-Type") != "application/octet-stream" ||
			resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("headers: %v", resp.Header)
		}
	}
	if sess.DownBytes.Load() == 0 {
		t.Fatal("down bytes not counted")
	}
	if sess.ActiveDown.Load() != 0 {
		t.Fatal("stream slot leaked")
	}
}

func TestDownloadRequestsNoCompressionWithAcceptEncoding(t *testing.T) {
	ts, _, sess := setup(t, Options{})
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/down?sid=%s&bytes=100000", ts.URL, sess.ID), nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	if resp.Header.Get("Content-Encoding") != "" || n != 100000 {
		t.Fatalf("encoding=%q n=%d", resp.Header.Get("Content-Encoding"), n)
	}
}

func TestDownloadErrors(t *testing.T) {
	ts, _, sess := setup(t, Options{MaxDownloadBytes: 1000})
	cases := []struct {
		q    string
		code int
	}{
		{"sid=bogus&bytes=10", http.StatusGone},
		{"bytes=10", http.StatusGone},
		{"sid=" + sess.ID + "&bytes=1001", http.StatusRequestEntityTooLarge},
		{"sid=" + sess.ID + "&bytes=0", http.StatusRequestEntityTooLarge},
		{"sid=" + sess.ID + "&bytes=-5", http.StatusRequestEntityTooLarge},
		{"sid=" + sess.ID + "&bytes=abc", http.StatusBadRequest},
	}
	for _, c := range cases {
		resp, err := http.Get(ts.URL + "/api/down?" + c.q)
		if err != nil {
			t.Fatal(err)
		}
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if resp.StatusCode != c.code || e.Error == "" || resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: status=%d error=%q", c.q, resp.StatusCode, e.Error)
		}
	}
}

func TestStreamCap429(t *testing.T) {
	ts, _, sess := setup(t, Options{MaxStreams: 2})
	sess.AcquireDown(2)
	sess.AcquireDown(2)
	sess.AcquireUp(2)
	sess.AcquireUp(2)
	resp, _ := http.Get(fmt.Sprintf("%s/api/down?sid=%s&bytes=10", ts.URL, sess.ID))
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("down status %d", resp.StatusCode)
	}
	resp, _ = http.Post(fmt.Sprintf("%s/api/up?sid=%s", ts.URL, sess.ID), "", strings.NewReader("x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("up status %d", resp.StatusCode)
	}
}

func TestUploadCounts(t *testing.T) {
	ts, _, sess := setup(t, Options{})
	payload := bytes.Repeat([]byte{0xAB}, 5_000_123)
	resp, err := http.Post(fmt.Sprintf("%s/api/up?sid=%s", ts.URL, sess.ID), "", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Bytes      int64 `json:"bytes"`
		DurationMs int64 `json:"durationMs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || out.Bytes != int64(len(payload)) || sess.UpBytes.Load() != int64(len(payload)) {
		t.Fatalf("status=%d bytes=%d sessionUp=%d", resp.StatusCode, out.Bytes, sess.UpBytes.Load())
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("headers %v", resp.Header)
	}
}

func TestUploadChunkedCounts(t *testing.T) {
	ts, _, sess := setup(t, Options{})
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 10; i++ {
			pw.Write(make([]byte, 100_000))
		}
		pw.Close()
	}()
	resp, err := http.Post(fmt.Sprintf("%s/api/up?sid=%s", ts.URL, sess.ID), "", pr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sess.UpBytes.Load() != 1_000_000 {
		t.Fatalf("up=%d", sess.UpBytes.Load())
	}
}

func TestUploadTooLarge(t *testing.T) {
	ts, _, sess := setup(t, Options{MaxUploadBytes: 1000})
	resp, err := http.Post(fmt.Sprintf("%s/api/up?sid=%s", ts.URL, sess.ID), "", bytes.NewReader(make([]byte, 1001)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", resp.StatusCode)
	}
	// Chunked body (no Content-Length) hits MaxBytesReader instead.
	pr, pw := io.Pipe()
	go func() { pw.Write(make([]byte, 5000)); pw.Close() }()
	resp, err = http.Post(fmt.Sprintf("%s/api/up?sid=%s", ts.URL, sess.ID), "", pr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked status %d", resp.StatusCode)
	}
	resp, _ = http.Post(ts.URL+"/api/up?sid=nope", "", strings.NewReader("x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("bad sid status %d", resp.StatusCode)
	}
}

func TestSessionCancelStopsDownload(t *testing.T) {
	ts, st, sess := setup(t, Options{MaxDownloadBytes: 1 << 40})
	resp, err := http.Get(fmt.Sprintf("%s/api/down?sid=%s&bytes=%d", ts.URL, sess.ID, int64(1)<<40))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<20)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	st.Expire(sess.ID, session.ReasonPreempted)
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, resp.Body); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected truncated body")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download did not stop after session cancel")
	}
}

func TestRequestDeadline(t *testing.T) {
	ts, _, sess := setup(t, Options{MaxDownloadBytes: 1 << 40, MaxRequestDuration: 300 * time.Millisecond})
	resp, err := http.Get(fmt.Sprintf("%s/api/down?sid=%s&bytes=%d", ts.URL, sess.ID, int64(1)<<40))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := make(chan struct{})
	go func() {
		// Read slowly so the server blocks in Write.
		buf := make([]byte, 1024)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(done)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadline not enforced")
	}
}

func TestKeepAliveReuseAfterDeadline(t *testing.T) {
	// Deadlines must be cleared after each request so a reused keep-alive
	// connection does not inherit an expired write deadline.
	ts, _, sess := setup(t, Options{MaxRequestDuration: 200 * time.Millisecond})
	client := &http.Client{Transport: &http.Transport{MaxConnsPerHost: 1}}
	for i := 0; i < 2; i++ {
		resp, err := client.Get(fmt.Sprintf("%s/api/down?sid=%s&bytes=1000", ts.URL, sess.ID))
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil || n != 1000 {
			t.Fatalf("iteration %d: n=%d err=%v", i, n, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
