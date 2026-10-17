package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CicerBro/firehose/internal/stats"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixture is the §7.5 example result.
func fixture() Result {
	return Result{
		Version:   1,
		Timestamp: "2026-10-17T12:00:00Z",
		Server:    ServerInfo{Name: "ams-1", URL: "https://speed.example.com", Version: "0.1.0"},
		Client:    ClientInfo{IP: "2001:db8::7", Family: "ipv6", UserAgent: "firehose-cli/0.1.0"},
		Config:    RunConfig{Connections: 8, DurationSec: 12, GraceSec: 2, Loss: true},
		Latency: Latency{
			Idle:     IdleLatency{MedianMs: 4.1, MinMs: 3.8, JitterMs: 0.3, Samples: 17},
			Download: LoadedLatency{MedianMs: 18.2, P90Ms: 25.0, Samples: 40},
			Upload:   LoadedLatency{MedianMs: 9.0, P90Ms: 12.1, Samples: 40},
		},
		Download: Throughput{Bps: 9410000000, P90Bps: 9480000000, Bytes: 11760000000, SteadyMs: 10000},
		Upload:   Upload{Bps: 9120000000, P90Bps: 9200000000, Bytes: 11400000000, SteadyMs: 10000},
		Loss: Loss{Available: true, DownPct: 0.00, UpPct: 0.17, JitterDownMs: 0.4, JitterUpMs: 0.5,
			SentDown: 600, SentUp: 600, ReorderedDown: 0},
		Flags:  Flags{},
		Phases: AllPhases(),
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenHuman(t *testing.T) {
	var b bytes.Buffer
	if err := WriteHuman(&b, fixture(), Style{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "human.golden", b.Bytes())

	// Partial result: aborted during upload, loss unavailable, single conn.
	r := fixture()
	r.Config.Connections = 1
	r.Flags.Aborted = true
	r.Upload = Upload{Bps: 123456789, Bytes: 1e8, SteadyMs: 600, Short: true, Approx: true}
	r.Latency.Upload = LoadedLatency{}
	r.Loss = Loss{}
	b.Reset()
	if err := WriteHuman(&b, r, Style{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "human_partial.golden", b.Bytes())
}

func TestHumanColor(t *testing.T) {
	var plain, color bytes.Buffer
	_ = WriteHuman(&plain, fixture(), Style{})
	_ = WriteHuman(&color, fixture(), Style{Color: true})
	if strings.Contains(plain.String(), "\x1b[") {
		t.Error("escape codes without color")
	}
	if !strings.Contains(color.String(), "\x1b[") {
		t.Error("no escape codes with color")
	}
}

func TestGoldenJSON(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, fixture()); err != nil {
		t.Fatal(err)
	}
	golden(t, "result.golden.json", b.Bytes())
	if bytes.Count(b.Bytes(), []byte("\n")) != 1 {
		t.Error("JSON must be exactly one line (NDJSON)")
	}
}

// TestJSONSchema checks every §7.5 field name and JSON type.
func TestJSONSchema(t *testing.T) {
	var b bytes.Buffer
	_ = WriteJSON(&b, Result{})
	var m map[string]any
	if err := json.Unmarshal(b.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if err := validateSchema(m); err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	b.Reset()
	_ = WriteJSON(&b, fixture())
	_ = json.Unmarshal(b.Bytes(), &full)
	if err := validateSchema(full); err != nil {
		t.Fatal(err)
	}
	// Must equal the documented example exactly (modulo formatting).
	example := `{
  "version": 1,
  "timestamp": "2026-10-17T12:00:00Z",
  "server": { "name": "ams-1", "url": "https://speed.example.com", "version": "0.1.0" },
  "client": { "ip": "2001:db8::7", "family": "ipv6", "userAgent": "firehose-cli/0.1.0" },
  "config": { "connections": 8, "durationSec": 12, "graceSec": 2, "loss": true },
  "latency": {
    "idle":     { "medianMs": 4.1, "minMs": 3.8, "jitterMs": 0.3, "samples": 17 },
    "download": { "medianMs": 18.2, "p90Ms": 25.0, "samples": 40 },
    "upload":   { "medianMs": 9.0,  "p90Ms": 12.1, "samples": 40 }
  },
  "download": { "bps": 9410000000, "p90bps": 9480000000, "bytes": 11760000000, "steadyMs": 10000, "short": false },
  "upload":   { "bps": 9120000000, "p90bps": 9200000000, "bytes": 11400000000, "steadyMs": 10000, "short": false, "approx": false },
  "loss":     { "available": true, "downPct": 0.00, "upPct": 0.17, "jitterDownMs": 0.4, "jitterUpMs": 0.5, "sentDown": 600, "sentUp": 600, "reorderedDown": 0 },
  "flags":    { "tabHidden": false, "singleOrigin": false, "aborted": false }
}`
	var want map[string]any
	if err := json.Unmarshal([]byte(example), &want); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(full)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("result differs from §7.5 example\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestGoldenCSV(t *testing.T) {
	var b bytes.Buffer
	if err := WriteCSVHeader(&b); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSV(&b, fixture()); err != nil {
		t.Fatal(err)
	}
	golden(t, "result.golden.csv", b.Bytes())
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || strings.Count(lines[0], ",") != strings.Count(lines[1], ",") {
		t.Errorf("header/row column mismatch:\n%s", b.String())
	}
}

func TestFormatBps(t *testing.T) {
	cases := map[float64]string{
		0: "0 bps", 999: "999 bps", 1000: "1.00 kbps", 9410000000: "9.41 Gbps", 94100000: "94.1 Mbps",
		941000000: "941 Mbps", 999600000: "1.00 Gbps", 30.5e9: "30.5 Gbps", 123456789: "123 Mbps",
	}
	for in, want := range cases {
		if got := FormatBps(in); got != want {
			t.Errorf("FormatBps(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBar(t *testing.T) {
	if got := bar(9.41, 9.41); got != "▕████████████████████▏" {
		t.Errorf("full bar %q", got)
	}
	if got := bar(9.12, 9.41); got != "▕███████████████████▌▏" {
		t.Errorf("9.12/9.41 bar %q", got)
	}
	if got := bar(0, 0); got != "▕"+strings.Repeat(" ", 20)+"▏" {
		t.Errorf("empty bar %q", got)
	}
}

func TestProgressLine(t *testing.T) {
	l := ProgressLine(Progress{Phase: PhaseDownload, Elapsed: 5200 * time.Millisecond, Duration: 12 * time.Second, Bps: 9.41e9, LatencyMs: 18.2})
	if !strings.Contains(l, "Download") || !strings.Contains(l, "9.41 Gbps") || !strings.Contains(l, "5.2 / 12 s") || !strings.Contains(l, "18.2 ms") {
		t.Errorf("progress line %q", l)
	}
	if strings.Contains(l, "\n") {
		t.Error("progress line contains a newline")
	}
}

func TestUploadReaderExactCount(t *testing.T) {
	buf := make([]byte, 1000) // small buffer to exercise wrap-around
	for i := range buf {
		buf[i] = byte(i)
	}
	for _, size := range []int64{0, 1, 999, 1000, 1001, 123457, 5 << 20} {
		for _, chunk := range []int{1, 7, 1000, 4096, 1 << 20} {
			var sent atomic.Int64
			r := &uploadReader{}
			r.reset(buf, size, &sent)
			start := r.off
			p := make([]byte, chunk)
			var total int64
			for {
				n, err := r.Read(p)
				for i := 0; i < n; i++ {
					if p[i] != buf[(start+int(total)+i)%len(buf)] {
						t.Fatalf("size %d chunk %d: wrong byte at %d", size, chunk, total+int64(i))
					}
				}
				total += int64(n)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					t.Fatal("zero read without EOF")
				}
			}
			if total != size || sent.Load() != size {
				t.Errorf("size %d chunk %d: read %d, counted %d", size, chunk, total, sent.Load())
			}
		}
	}
}

func TestUploadReaderNoAlloc(t *testing.T) {
	var sent atomic.Int64
	r := &uploadReader{}
	p := make([]byte, 256<<10)
	buf := payload()
	allocs := testing.AllocsPerRun(100, func() {
		r.reset(buf, 1<<20, &sent)
		for {
			if _, err := r.Read(p); err != nil {
				break
			}
		}
	})
	if allocs != 0 {
		t.Errorf("uploadReader allocates %v per run, want 0", allocs)
	}
}

func BenchmarkUploadReader(b *testing.B) {
	var sent atomic.Int64
	r := &uploadReader{}
	p := make([]byte, 256<<10)
	buf := payload()
	b.SetBytes(int64(len(p)))
	b.ReportAllocs()
	r.reset(buf, 1<<62, &sent)
	for b.Loop() {
		_, _ = r.Read(p)
	}
}

func TestExitCode(t *testing.T) {
	opErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{&UsageError{Msg: "x"}, 1},
		{&UnreachableError{Err: errors.New("x")}, 2},
		{opErr, 2},
		{fmt.Errorf("wrapped: %w", opErr), 2},
		{tls.RecordHeaderError{Msg: "x"}, 2},
		{&AuthError{Status: 401}, 3},
		{&AuthError{Status: 302, Location: "https://auth.example.com/"}, 3},
		{fmt.Errorf("x: %w", &AuthError{Status: 401}), 3},
		{&PhaseError{Phase: "download", Err: errors.New("all streams failed")}, 4},
		{errors.Join(&PhaseError{Phase: "latency", Err: errors.New("x")}, &PhaseError{Phase: "upload", Err: errors.New("y")}), 4},
		{fmt.Errorf("%w: %w", ErrInterrupted, context.Canceled), 130},
		{context.Canceled, 130},
		{errors.New("something else"), 4},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestNormalizeServer(t *testing.T) {
	ok := map[string]string{
		"https://Speed.Example.com/":  "https://speed.example.com",
		"speed.example.com":           "https://speed.example.com",
		"http://127.0.0.1:8080":       "http://127.0.0.1:8080",
		" http://[::1]:8080/ ":        "http://[::1]:8080",
		"https://speed.example.com:8": "https://speed.example.com:8",
	}
	for in, want := range ok {
		got, err := NormalizeServer(in)
		if err != nil || got != want {
			t.Errorf("NormalizeServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://x/path", "https://x?q=1", "https://user@x", "http://"} {
		if _, err := NormalizeServer(bad); ExitCode(err) != ExitUsage {
			t.Errorf("NormalizeServer(%q): err %v, want usage error", bad, err)
		}
	}
}

func TestOptionsNormalize(t *testing.T) {
	o := Options{Server: "x.example"}
	if err := o.normalize(); err != nil {
		t.Fatal(err)
	}
	if o.Connections != 8 || o.Duration != 12*time.Second || o.Grace != 2*time.Second || o.Phases != AllPhases() {
		t.Errorf("defaults: %+v", o)
	}
	o = Options{Server: "x.example", NoGrace: true}
	_ = o.normalize()
	if o.Grace != 0 {
		t.Errorf("NoGrace: grace %v", o.Grace)
	}
	for _, bad := range []Options{
		{Server: "x", Connections: -1},
		{Server: "x", Duration: -time.Second},
		{Server: "x", IPFamily: 5},
		{Server: "x", CAFile: "/nonexistent"},
	} {
		_, err := Run(context.Background(), bad)
		if ExitCode(err) != ExitUsage {
			t.Errorf("%+v: err %v, want usage", bad, err)
		}
	}
}

func TestStreamTransport(t *testing.T) {
	nc, err := newNetConfig(&Options{IPFamily: 6})
	if err != nil {
		t.Fatal(err)
	}
	tr := nc.newStreamTransport()
	if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 || tr.ForceAttemptHTTP2 {
		t.Error("HTTP/2 not disabled")
	}
	if !tr.DisableCompression || tr.MaxConnsPerHost != 1 || tr.ReadBufferSize != 256<<10 || tr.WriteBufferSize != 256<<10 {
		t.Errorf("transport settings: %+v", tr)
	}
	if tr.Proxy != nil {
		t.Error("proxy must be off")
	}
	if nc.network() != "tcp6" {
		t.Errorf("network %q", nc.network())
	}
	if got := tr.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Errorf("ALPN %v", got)
	}
}

func TestUploadTimeline(t *testing.T) {
	t0 := time.Now()
	ms := func(d int) time.Time { return t0.Add(time.Duration(d) * time.Millisecond) }
	ss := []statsSample{
		{recv: ms(-150), t: 1_000_000_000, up: 500},
		{recv: ms(-50), t: 1_100_000_000, up: 1000}, // baseline
		{recv: ms(50), t: 1_200_000_000, up: 3000},
		{recv: ms(150), t: 1_300_000_000, up: 6000},
		{recv: ms(250), t: 1_400_000_000, up: 9000}, // after end
	}
	tl := uploadTimeline(ss, t0, ms(200))
	want := []stats.Sample{{T: -50, Bytes: 0}, {T: 50, Bytes: 2000}, {T: 150, Bytes: 5000}}
	if len(tl) != len(want) {
		t.Fatalf("timeline %v, want %v", tl, want)
	}
	for i := range want {
		if d := tl[i].T - want[i].T; d > 1e-6 || d < -1e-6 || tl[i].Bytes != want[i].Bytes {
			t.Errorf("sample %d: %+v, want %+v", i, tl[i], want[i])
		}
	}
	if uploadTimeline(nil, t0, ms(100)) != nil {
		t.Error("empty stats must give nil timeline")
	}
}

func TestWSURL(t *testing.T) {
	if got := wsURL("https://a.example", "ab"); got != "wss://a.example/api/ws?sid=ab" {
		t.Error(got)
	}
	if got := wsURL("http://127.0.0.1:1", "ab"); got != "ws://127.0.0.1:1/api/ws?sid=ab" {
		t.Error(got)
	}
}
