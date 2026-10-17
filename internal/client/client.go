// Package client is the speed test client library behind `speedtest client`
// (BUILD-PLAN §10). It follows the browser's API flow and methodology
// (§6): GET /api/config → GET /api/ip → POST /api/session → WebSocket
// /api/ws → latency (+ loss) → download → upload, with all statistics
// computed by internal/stats.
//
// Run never prints; progress is reported through Options.Progress and
// warnings through Options.Warn. Renderers for the result live in output.go.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/CicerBro/speedtest/internal/stats"
)

// Defaults (§6.1, §10.2).
const (
	DefaultConnections     = 8
	DefaultDuration        = 12 * time.Second
	DefaultGrace           = 2 * time.Second
	DefaultLatencyDuration = 2 * time.Second
	// idleWarmup is the number of initial idle pings excluded (§6.3).
	idleWarmup = 3
	// timelineStep is the throughput timeline sampling period (§6.2).
	timelineStep = 100 * time.Millisecond
	// progressStep is the progress callback period (4×/s, §10.4).
	progressStep = 250 * time.Millisecond
	// liveWindowMs is the live-rate window (§6.3).
	liveWindowMs = 500
)

// Options configure one run.
type Options struct {
	Server      string        // server origin (PUBLIC_URL); see NormalizeServer
	Token       string        // bearer token; empty sends no Authorization header
	Connections int           // streams per direction (default 8)
	Duration    time.Duration // per direction (default 12 s)
	Grace       time.Duration // warm-up excluded from final results (default 2 s; 0 allowed)
	NoGrace     bool          // use a zero grace period (Grace 0 means default otherwise)
	Phases      Phases        // phases to run; the zero value means all
	IPFamily    int           // 0 (any), 4 or 6
	Direct      bool          // send all streams to Server, ignoring testHosts
	Insecure    bool          // skip TLS verification
	CAFile      string        // extra PEM CA bundle
	Version     string        // client version, for the user agent

	// Progress, if set, is called from Run's goroutine about 4×/s during
	// phases, once with PhaseInit (Result has server, client and config
	// filled in) and once with PhaseDone. It must not block for long.
	Progress func(Progress)
	// Warn, if set, receives non-fatal problems (e.g. loss test failed).
	Warn func(string)
	// Loss overrides the packet-loss implementation (see loss.go).
	Loss LossFunc

	// Tuning, mainly for tests. Zero values use the defaults.
	LatencyDuration time.Duration // idle-latency phase without loss (default 2 s)
	DownloadBytes   int64         // bytes per download request (default 2 GiB, capped by the server limit)
	UploadBytes     int64         // bytes per upload request (default: server maxUploadBytes)
}

// Phase names a test phase in Progress.
type Phase string

// Phases reported through Progress.
const (
	PhaseInit     Phase = "init"
	PhaseLatency  Phase = "latency"
	PhaseDownload Phase = "download"
	PhaseUpload   Phase = "upload"
	PhaseDone     Phase = "done"
)

// Progress is a live update.
type Progress struct {
	Phase     Phase
	Elapsed   time.Duration // since phase start
	Duration  time.Duration // planned phase length
	Bps       float64       // live rate (transfer phases), §6.3
	Bytes     int64         // bytes so far in this phase
	LatencyMs float64       // most recent RTT
	Result    *Result       // set for PhaseInit and PhaseDone only
}

// RunStats are client-side counters of a run (not part of the JSON).
type RunStats struct {
	SID           string
	DownBytes     int64    // bytes received by the client during download
	UpBytesSent   int64    // bytes handed to the upload connections
	StreamOrigins []string // origin used by each stream
}

// UserAgent returns the client user agent for version.
func UserAgent(version string) string {
	if version == "" {
		version = "dev"
	}
	return "speedtest-cli/" + version
}

// NormalizeServer validates a server URL and returns its origin without a
// trailing slash. A bare host ("speed.example.com") gets https://.
func NormalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", &UsageError{Msg: "no server given: use --server or set SPEEDTEST_SERVER"}
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", &UsageError{Msg: fmt.Sprintf("invalid server URL %q: %v", s, err)}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", &UsageError{Msg: fmt.Sprintf("invalid server URL %q: scheme must be http or https", s)}
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", &UsageError{Msg: fmt.Sprintf("invalid server URL %q: missing host", s)}
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", &UsageError{Msg: fmt.Sprintf("invalid server URL %q: use the origin only (scheme://host[:port])", s)}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

func (o *Options) normalize() error {
	srv, err := NormalizeServer(o.Server)
	if err != nil {
		return err
	}
	o.Server = srv
	if o.Connections == 0 {
		o.Connections = DefaultConnections
	}
	if o.Connections < 1 {
		return &UsageError{Msg: "connections must be at least 1"}
	}
	if o.Duration == 0 {
		o.Duration = DefaultDuration
	}
	if o.Duration < 0 {
		return &UsageError{Msg: "duration must be positive"}
	}
	switch {
	case o.NoGrace:
		o.Grace = 0
	case o.Grace == 0:
		o.Grace = DefaultGrace
	case o.Grace < 0:
		return &UsageError{Msg: "grace must not be negative"}
	}
	if o.IPFamily != 0 && o.IPFamily != 4 && o.IPFamily != 6 {
		return &UsageError{Msg: "IP family must be 4 or 6"}
	}
	if o.Phases == (Phases{}) {
		o.Phases = AllPhases()
	}
	if o.LatencyDuration <= 0 {
		o.LatencyDuration = DefaultLatencyDuration
	}
	if o.DownloadBytes <= 0 {
		o.DownloadBytes = DefaultDownloadBytes
	}
	if o.Progress == nil {
		o.Progress = func(Progress) {}
	}
	if o.Warn == nil {
		o.Warn = func(string) {}
	}
	return nil
}

// runner holds the state of one Run.
type runner struct {
	o       Options
	header  http.Header
	api     *api
	streams []*stream
	ctl     *control
	sid     string
	res     Result
}

// Run executes one test. It returns the result (partial on error once the
// session exists) and an error classified for ExitCode:
//   - context cancellation (Ctrl-C): ErrInterrupted, flags.aborted=true;
//   - context deadline (--timeout): *PhaseError, flags.aborted=true;
//   - *UsageError, *AuthError, *UnreachableError, *PhaseError otherwise.
//
// Loss being unavailable is never an error.
func Run(ctx context.Context, o Options) (Result, error) {
	if err := o.normalize(); err != nil {
		return Result{}, err
	}
	start := time.Now()
	r := &runner{o: o}
	r.res = Result{
		Version:   ResultVersion,
		Timestamp: start.UTC().Format(time.RFC3339),
		Client:    ClientInfo{UserAgent: UserAgent(o.Version)},
	}
	err := r.run(ctx)
	if ctx.Err() != nil {
		r.res.Flags.Aborted = true
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = &PhaseError{Phase: "test", Err: errors.New("timed out")}
		} else {
			err = fmt.Errorf("%w: %w", ErrInterrupted, context.Canceled)
		}
	}
	res := r.res
	o.Progress(Progress{Phase: PhaseDone, Result: &res})
	return res, err
}

func (r *runner) run(ctx context.Context) error {
	o := &r.o
	nc, err := newNetConfig(o)
	if err != nil {
		return err
	}
	r.header = http.Header{"User-Agent": {UserAgent(o.Version)}}
	if o.Token != "" {
		r.header.Set("Authorization", "Bearer "+o.Token)
	}
	apiTr := nc.newAPITransport()
	defer apiTr.CloseIdleConnections()
	r.api = &api{base: o.Server, hc: newHTTPClient(apiTr), header: r.header}

	// Init: config and IP.
	cfg, err := r.api.config(ctx)
	if err != nil {
		return classifyInit(ctx, err)
	}
	r.res.Server = ServerInfo{Name: cfg.ServerName, URL: o.Server, Version: cfg.Version}
	r.res.Flags.SingleOrigin = cfg.SingleOrigin
	ip, err := r.api.ip(ctx)
	if err != nil {
		var ae *AuthError
		if errors.As(err, &ae) || ctx.Err() != nil {
			return classifyInit(ctx, err)
		}
		o.Warn("could not determine client IP: " + err.Error())
	}
	r.res.Client.IP, r.res.Client.Family = ip.IP, ip.Family

	n := o.Connections
	if max := cfg.Limits.MaxConnections; max > 0 && n > max {
		return &UsageError{Msg: fmt.Sprintf("the server allows at most %d connections (requested %d)", max, n)}
	}
	loss := lossFunc(o)
	lossOn := o.Phases.Loss && loss != nil
	r.res.Phases = Phases{Loss: lossOn} // others are set as phases run
	r.res.Config = RunConfig{
		Connections: n,
		DurationSec: o.Duration.Seconds(),
		GraceSec:    o.Grace.Seconds(),
		Loss:        lossOn,
	}

	sess, status, err := r.api.session(ctx, n)
	if err != nil {
		if status == http.StatusBadRequest {
			return &UsageError{Msg: "session rejected: " + err.Error()}
		}
		return classifyInit(ctx, err)
	}
	r.sid = sess.SID
	r.res.Stats.SID = sess.SID

	// Streams: one transport each. The CLI has no per-host limit, so in
	// single-origin mode (or with --direct) every stream goes to --server.
	hosts := []string{o.Server}
	if !o.Direct && !cfg.SingleOrigin && len(sess.TestHosts) > 0 {
		hosts = make([]string, 0, len(sess.TestHosts))
		for _, h := range sess.TestHosts {
			hosts = append(hosts, strings.TrimRight(h, "/"))
		}
	}
	for i := 0; i < n; i++ {
		tr := nc.newStreamTransport()
		r.streams = append(r.streams, &stream{base: hosts[i%len(hosts)], tr: tr, hc: newHTTPClient(tr)})
		r.res.Stats.StreamOrigins = append(r.res.Stats.StreamOrigins, hosts[i%len(hosts)])
	}
	defer func() {
		for _, s := range r.streams {
			s.tr.CloseIdleConnections()
		}
	}()

	info := r.res
	o.Progress(Progress{Phase: PhaseInit, Result: &info})

	var phaseErrs []error
	needCtl := o.Phases.Latency || o.Phases.Download || o.Phases.Upload
	if needCtl {
		ctl, err := dialControl(ctx, r.api.hc, o.Server, r.sid, r.header)
		if err != nil {
			var ae *AuthError
			if errors.As(err, &ae) || ctx.Err() != nil {
				return err
			}
			o.Warn("control channel unavailable (no latency, upload estimated client-side): " + err.Error())
			if o.Phases.Latency {
				phaseErrs = append(phaseErrs, &PhaseError{Phase: "latency", Err: err})
			}
		} else {
			r.ctl = ctl
			ctl.start(ctx)
			defer ctl.close()
		}
	}
	// The API calls are done (the WebSocket owns its hijacked connection):
	// drop the idle keep-alive connection so only test connections remain.
	apiTr.CloseIdleConnections()

	// Phase 1: idle latency, with the loss test in parallel.
	if o.Phases.Latency || lossOn {
		if err := r.latencyPhase(ctx, &cfg, lossOn, loss); err != nil {
			phaseErrs = append(phaseErrs, err)
		}
	}
	if ctx.Err() != nil {
		return nil
	}

	// Phase 2: download.
	if o.Phases.Download {
		if err := r.downloadPhase(ctx, &cfg); err != nil {
			var ae *AuthError
			if errors.As(err, &ae) {
				return err
			}
			phaseErrs = append(phaseErrs, &PhaseError{Phase: "download", Err: err})
		}
	}
	if ctx.Err() != nil {
		return nil
	}

	// Phase 3: upload.
	if o.Phases.Upload {
		if err := r.uploadPhase(ctx, &cfg); err != nil {
			var ae *AuthError
			if errors.As(err, &ae) {
				return err
			}
			phaseErrs = append(phaseErrs, &PhaseError{Phase: "upload", Err: err})
		}
	}
	return errors.Join(phaseErrs...)
}

// classifyInit maps an error before the test phases to an exit class.
func classifyInit(ctx context.Context, err error) error {
	var ae *AuthError
	var ue *UsageError
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &ae), errors.As(err, &ue):
		return err
	default:
		return &UnreachableError{Err: err}
	}
}

// waitPhase waits d (or until ctx is done), calling tick every progressStep.
func waitPhase(ctx context.Context, d time.Duration, tick func(elapsed time.Duration)) {
	start := time.Now()
	t := time.NewTicker(progressStep)
	defer t.Stop()
	end := time.NewTimer(d)
	defer end.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-end.C:
			return
		case <-t.C:
			tick(time.Since(start))
		}
	}
}

func (r *runner) latencyPhase(ctx context.Context, cfg *ServerConfig, lossOn bool, loss LossFunc) error {
	o := &r.o
	d := o.LatencyDuration
	type lossResult struct {
		l   Loss
		err error
	}
	var lossCh chan lossResult
	if lossOn {
		if !cfg.RTC.Enabled {
			o.Warn("packet loss test disabled on the server")
			lossOn = false
		} else {
			d = max(d, LossDuration)
			lossCh = make(chan lossResult, 1)
			p := LossParams{
				Server: o.Server, SID: r.sid, Header: r.header.Clone(), HTTPClient: r.api.hc,
				IPFamily: o.IPFamily, Duration: LossDuration, PPS: LossPPS, Size: LossSize,
			}
			go func() {
				l, err := loss(ctx, p)
				lossCh <- lossResult{l, err}
			}()
		}
	}
	var phaseErr error
	if o.Phases.Latency && r.ctl != nil {
		r.ctl.setInterval(IdlePingInterval)
		start := time.Now()
		waitPhase(ctx, d, func(el time.Duration) {
			o.Progress(Progress{Phase: PhaseLatency, Elapsed: el, Duration: d, LatencyMs: r.ctl.lastRTT()})
		})
		r.ctl.setInterval(0)
		r.res.Phases.Latency = true
		end := time.Now()
		rtts := r.ctl.rttsBetween(start, end)
		if len(rtts) > idleWarmup {
			rtts = rtts[idleWarmup:]
		} else {
			rtts = nil
		}
		r.res.Latency.Idle = IdleLatency{
			MedianMs: round(stats.Median(rtts), 3),
			MinMs:    round(stats.Min(rtts), 3),
			JitterMs: round(stats.Jitter(rtts), 3),
			Samples:  len(rtts),
		}
		if err := r.ctl.Err(); err != nil {
			phaseErr = &PhaseError{Phase: "latency", Err: err}
		} else if len(rtts) == 0 && ctx.Err() == nil {
			phaseErr = &PhaseError{Phase: "latency", Err: errors.New("no ping replies")}
		}
	} else if lossOn {
		waitPhase(ctx, d, func(el time.Duration) {
			o.Progress(Progress{Phase: PhaseLatency, Elapsed: el, Duration: d})
		})
	}
	if lossCh != nil {
		select {
		case lr := <-lossCh:
			if lr.err != nil {
				o.Warn("packet loss unavailable: " + lr.err.Error())
			} else {
				r.res.Loss = lr.l
			}
		case <-ctx.Done():
		}
	}
	return phaseErr
}

// transferOutcome is the client-side view of one transfer phase.
type transferOutcome struct {
	tl         []stats.Sample // client-side cumulative bytes every 100 ms
	start, end time.Time
	endMs      float64
	bytes      int64
	err        error
}

// transfer runs worker on every stream for o.Duration (or until ctx ends),
// recording the client-side timeline from counter and reporting progress.
// live, if set, computes the live rate shown in progress instead of the
// client timeline.
func (r *runner) transfer(ctx context.Context, phase Phase, counter *atomic.Int64,
	worker func(ctx context.Context, s *stream, g *streamGroup),
	live func(start time.Time) float64) transferOutcome {
	d := r.o.Duration
	if r.ctl != nil {
		r.ctl.setInterval(LoadedPingInterval)
		defer r.ctl.setInterval(0)
	}
	pctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	start := time.Now()
	out := transferOutcome{start: start, tl: make([]stats.Sample, 1, int(d/timelineStep)+4)}
	done := make(chan error, 1)
	go func() {
		done <- runStreams(pctx, r.streams, func(ctx context.Context, _ int, s *stream, g *streamGroup) {
			worker(ctx, s, g)
		})
	}()
	tick := time.NewTicker(timelineStep)
	defer tick.Stop()
	nextProgress := start.Add(progressStep)
	finished := false
	for !finished {
		select {
		case <-pctx.Done():
			finished = true
		case out.err = <-done:
			finished = true
			done = nil
		case now := <-tick.C:
			ms := float64(now.Sub(start)) / float64(time.Millisecond)
			b := counter.Load()
			out.tl = append(out.tl, stats.Sample{T: ms, Bytes: float64(b)})
			if !now.Before(nextProgress) {
				nextProgress = nextProgress.Add(progressStep)
				var bps float64
				if live != nil {
					bps = live(start)
				} else {
					bps = stats.LiveRate(out.tl, ms, liveWindowMs)
				}
				p := Progress{Phase: phase, Elapsed: now.Sub(start), Duration: d, Bps: bps, Bytes: b}
				if r.ctl != nil {
					p.LatencyMs = r.ctl.lastRTT()
				}
				r.o.Progress(p)
			}
		}
	}
	out.end = time.Now()
	out.endMs = float64(out.end.Sub(start)) / float64(time.Millisecond)
	out.bytes = counter.Load()
	out.tl = append(out.tl, stats.Sample{T: out.endMs, Bytes: float64(out.bytes)})
	cancel()
	if done != nil {
		out.err = <-done
	}
	return out
}

// loaded computes loaded latency for a transfer phase: RTTs after grace.
func (r *runner) loaded(t transferOutcome) LoadedLatency {
	if r.ctl == nil {
		return LoadedLatency{}
	}
	rtts := r.ctl.rttsBetween(t.start.Add(r.o.Grace), t.end)
	return LoadedLatency{
		MedianMs: round(stats.Median(rtts), 3),
		P90Ms:    round(stats.P90(rtts), 3),
		Samples:  len(rtts),
	}
}

func graceMs(o *Options) float64 { return float64(o.Grace) / float64(time.Millisecond) }

func (r *runner) downloadPhase(ctx context.Context, cfg *ServerConfig) error {
	size := r.o.DownloadBytes
	if m := cfg.Limits.MaxDownloadBytes; m > 0 && size > m {
		size = m
	}
	var total atomic.Int64
	out := r.transfer(ctx, PhaseDownload, &total, func(ctx context.Context, s *stream, g *streamGroup) {
		downloadStream(ctx, s, r.sid, size, r.header, &total, g)
	}, nil)
	g := graceMs(&r.o)
	th := stats.Steady(out.tl, g, out.endMs)
	r.res.Download = Throughput{
		Bps:      round(th.Bps, 0),
		P90Bps:   round(stats.RollingP90(out.tl, g, out.endMs), 0),
		Bytes:    round(th.Bytes, 0),
		SteadyMs: round(th.SteadyMs, 0),
		Short:    th.Short,
	}
	r.res.Latency.Download = r.loaded(out)
	r.res.Phases.Download = true
	r.res.Stats.DownBytes = total.Load()
	return out.err
}

func (r *runner) uploadPhase(ctx context.Context, cfg *ServerConfig) error {
	size := r.o.UploadBytes
	if size <= 0 {
		size = cfg.Limits.MaxUploadBytes
	}
	if size <= 0 {
		size = DefaultUploadBytes
	}
	if m := cfg.Limits.MaxUploadBytes; m > 0 && size > m {
		size = m
	}
	var sent atomic.Int64
	var live func(time.Time) float64
	if r.ctl != nil {
		live = func(start time.Time) float64 {
			tl := uploadTimeline(r.ctl.statsSnapshot(), start, time.Now())
			if len(tl) < 2 {
				return 0
			}
			return stats.LiveRate(tl, tl[len(tl)-1].T, liveWindowMs)
		}
	}
	out := r.transfer(ctx, PhaseUpload, &sent, func(ctx context.Context, s *stream, g *streamGroup) {
		uploadStream(ctx, s, r.sid, size, r.header, &sent, g)
	}, live)

	g := graceMs(&r.o)
	tl, endMs, approx := out.tl, out.endMs, true
	if r.ctl != nil {
		if stl := uploadTimeline(r.ctl.statsSnapshot(), out.start, out.end); len(stl) >= 2 {
			tl, endMs, approx = stl, stl[len(stl)-1].T, false
		}
	}
	if approx {
		r.o.Warn("no server upload stats; upload estimated from client-side byte count")
	}
	th := stats.Steady(tl, g, endMs)
	r.res.Upload = Upload{
		Bps:      round(th.Bps, 0),
		P90Bps:   round(stats.RollingP90(tl, g, endMs), 0),
		Bytes:    round(th.Bytes, 0),
		SteadyMs: round(th.SteadyMs, 0),
		Short:    th.Short,
		Approx:   approx,
	}
	r.res.Latency.Upload = r.loaded(out)
	r.res.Phases.Upload = true
	r.res.Stats.UpBytesSent = sent.Load() // after all streams stopped
	return out.err
}

// uploadTimeline builds the upload timeline from server stats (§6.2):
// bytes = stats.up minus the baseline at phase start, time deltas from the
// server clock t, and client receipt time only to select samples inside
// [start, end]. The baseline is the last sample received at or before
// start (or the first one after it); its receipt time anchors the server
// clock to the phase start.
func uploadTimeline(ss []statsSample, start, end time.Time) []stats.Sample {
	bi := -1
	for i, s := range ss {
		if s.recv.After(start) {
			if bi < 0 {
				bi = i
			}
			break
		}
		bi = i
	}
	if bi < 0 {
		return nil
	}
	base := ss[bi]
	offMs := float64(base.recv.Sub(start)) / float64(time.Millisecond)
	tl := []stats.Sample{{T: offMs, Bytes: 0}}
	for _, s := range ss[bi+1:] {
		if s.recv.After(end) {
			break
		}
		tl = append(tl, stats.Sample{
			T:     offMs + float64(s.t-base.t)/1e6,
			Bytes: float64(s.up - base.up),
		})
	}
	return tl
}
