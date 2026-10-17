// Package server wires the HTTP server: routes, auth, CORS, static UI with
// security headers, graceful shutdown and the optional /metrics listener.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/CicerBro/firehose/internal/config"
	"github.com/CicerBro/firehose/internal/control"
	"github.com/CicerBro/firehose/internal/session"
	"github.com/CicerBro/firehose/internal/transfer"
	"github.com/CicerBro/firehose/web"
)

// ShutdownTimeout bounds graceful shutdown (§5.4).
const ShutdownTimeout = 10 * time.Second

// RTCFactory builds the POST /api/rtc/offer handler once the session store
// exists. If the returned handler implements io.Closer it is closed when the
// server closes.
type RTCFactory func(cfg config.Config, store *session.Store, log *slog.Logger) (http.Handler, error)

// Option customizes New.
type Option func(*options)

type options struct {
	logger  *slog.Logger
	version string
	rtc     RTCFactory
	ui      fs.FS
	now     func() time.Time
}

// WithLogger sets the logger (default slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithVersion sets the version reported by /api/config.
func WithVersion(v string) Option { return func(o *options) { o.version = v } }

// WithRTC mounts the WebRTC signaling handler built by f at
// POST /api/rtc/offer (behind auth). Without it, /api/config reports
// rtc.enabled=false.
func WithRTC(f RTCFactory) Option { return func(o *options) { o.rtc = f } }

// WithUI replaces the embedded UI filesystem (tests).
func WithUI(fsys fs.FS) Option { return func(o *options) { o.ui = fsys } }

// WithClock injects the session-store clock (tests).
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// Server is the speed test HTTP server.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	version  string
	store    *session.Store
	transfer *transfer.Handler
	control  *control.Handler
	rtc      http.Handler
	ui       fs.FS
	csp      string
	handler  http.Handler

	closeOnce sync.Once
}

// New validates cfg (applying dev defaults), creates the session store and
// starts its janitor. Call Close (or Run, which closes on exit) to release it.
func New(cfg config.Config, opts ...Option) (*Server, error) {
	o := options{logger: slog.Default(), version: "dev", ui: web.FS}
	for _, opt := range opts {
		opt(&o)
	}
	cfg.TestHosts = append([]string(nil), cfg.TestHosts...)
	cfg.ApplyDevDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	s := &Server{cfg: cfg, log: o.logger, version: o.version, ui: o.ui}
	s.store = session.NewStore(session.Options{
		MaxActive: cfg.MaxActiveSessions,
		TTL:       cfg.SessionTTL,
		MaxAge:    cfg.SessionMaxAge,
		Now:       o.now,
		Logger:    o.logger,
	})
	th, err := transfer.New(s.store, transfer.Options{
		MaxDownloadBytes:   cfg.MaxDownloadBytes,
		MaxUploadBytes:     cfg.MaxUploadBytes,
		MaxRequestDuration: cfg.MaxRequestDuration,
		MaxStreams:         cfg.MaxConnections + 2,
		Logger:             o.logger,
	})
	if err != nil {
		return nil, err
	}
	s.transfer = th
	s.control = control.New(s.store, control.Options{
		OriginPatterns: []string{hostOf(cfg.PublicURL)},
		Logger:         o.logger,
	})
	if o.rtc != nil && cfg.RTCEnabled {
		h, err := o.rtc(cfg, s.store, o.logger)
		if err != nil {
			return nil, fmt.Errorf("rtc: %w", err)
		}
		s.rtc = h
	}
	s.csp = buildCSP(cfg)
	s.handler = s.routes()
	if cfg.Dev {
		s.log.Warn("!!! DEV MODE: authentication check DISABLED, debug overrides enabled. Never use --dev in production !!!")
	}
	if !cfg.SingleOrigin() && len(cfg.TestHosts)*config.ConnectionsPerHost < cfg.MaxConnections {
		s.log.Warn("not enough test hosts for max connections; some hosts will carry more than 4 connections",
			"test_hosts", len(cfg.TestHosts), "max_connections", cfg.MaxConnections)
	}
	s.store.Start()
	return s, nil
}

// Config returns the effective (validated, normalized) configuration.
func (s *Server) Config() config.Config { return s.cfg }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Sessions returns the session store.
func (s *Server) Sessions() *session.Store { return s.store }

// Close cancels all sessions, stops the janitor and closes the RTC handler
// if it is an io.Closer. Idempotent.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.store.Close()
		if c, ok := s.rtc.(io.Closer); ok {
			err = c.Close()
		}
	})
	return err
}

// Run listens on cfg.Listen (and cfg.MetricsListen if set) and serves until
// ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		s.Close()
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve is Run on an existing listener (e.g. 127.0.0.1:0 in tests). On ctx
// cancellation it stops accepting, cancels all sessions and calls Shutdown
// with a 10 s timeout. It always closes the server before returning.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	defer s.Close()
	hs := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	servers := []*http.Server{hs}
	errc := make(chan error, 2)
	s.log.Info("listening", "addr", ln.Addr().String(), "public_url", s.cfg.PublicURL,
		"single_origin", s.cfg.SingleOrigin(), "version", s.version)
	go func() { errc <- hs.Serve(ln) }()

	if s.cfg.MetricsListen != "" {
		mln, err := net.Listen("tcp", s.cfg.MetricsListen)
		if err != nil {
			hs.Close()
			return fmt.Errorf("metrics listen: %w", err)
		}
		ms := &http.Server{Handler: s.MetricsHandler(), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, ms)
		s.log.Info("metrics listening", "addr", mln.Addr().String())
		go func() { errc <- ms.Serve(mln) }()
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
	}
	s.log.Info("shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, srv := range servers {
			if err := srv.Shutdown(shCtx); err != nil {
				srv.Close()
			}
		}
	}()
	s.Close() // cancel all session contexts so transfers and WS end promptly
	<-done
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
