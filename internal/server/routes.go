package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/CicerBro/firehose/internal/config"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	auth := s.requireAuth

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ok")
	})

	mux.Handle("GET /api/config", auth(http.HandlerFunc(s.handleConfig)))
	mux.Handle("GET /api/ip", auth(http.HandlerFunc(s.handleIP)))
	mux.Handle("POST /api/session", auth(http.HandlerFunc(s.handleSession)))
	mux.Handle("GET /api/ws", auth(s.control))

	// Test-host transfer endpoints: credentialed CORS; OPTIONS answered
	// before auth (preflights never carry cookies).
	noop := http.NotFoundHandler()
	mux.Handle("GET /api/down", s.cors(auth(http.HandlerFunc(s.transfer.Down))))
	mux.Handle("POST /api/up", s.cors(auth(http.HandlerFunc(s.transfer.Up))))
	mux.Handle("OPTIONS /api/down", s.cors(noop))
	mux.Handle("OPTIONS /api/up", s.cors(noop))

	if s.rtc != nil {
		mux.Handle("POST /api/rtc/offer", auth(s.rtc))
	}

	mux.Handle("/api/", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})))
	mux.Handle("/", auth(s.staticHandler()))

	return s.logRequests(mux)
}

type configResponse struct {
	ServerName   string       `json:"serverName"`
	Version      string       `json:"version"`
	TestHosts    []string     `json:"testHosts"`
	Limits       configLimits `json:"limits"`
	RTC          configRTC    `json:"rtc"`
	SingleOrigin bool         `json:"singleOrigin"`
	Dev          bool         `json:"dev,omitempty"`
}

type configLimits struct {
	MaxConnections   int   `json:"maxConnections"`
	MaxDownloadBytes int64 `json:"maxDownloadBytes"`
	MaxUploadBytes   int64 `json:"maxUploadBytes"`
}

type configRTC struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{
		ServerName: s.cfg.ServerName,
		Version:    s.version,
		TestHosts:  s.cfg.EffectiveTestHosts(),
		Limits: configLimits{
			MaxConnections:   s.cfg.MaxConnections,
			MaxDownloadBytes: s.cfg.MaxDownloadBytes,
			MaxUploadBytes:   s.cfg.MaxUploadBytes,
		},
		RTC:          configRTC{Enabled: s.cfg.RTCEnabled && s.rtc != nil},
		SingleOrigin: s.cfg.SingleOrigin(),
		Dev:          s.cfg.Dev,
	})
}

type ipResponse struct {
	IP     string `json:"ip"`
	Family string `json:"family"`
}

func (s *Server) handleIP(w http.ResponseWriter, r *http.Request) {
	ip := ClientIP(r, s.cfg.TrustedProxies)
	if !ip.IsValid() {
		writeError(w, http.StatusInternalServerError, "no_ip", "could not determine client IP")
		return
	}
	writeJSON(w, http.StatusOK, ipResponse{IP: ip.String(), Family: ipFamily(ip)})
}

type sessionRequest struct {
	Connections *int `json:"connections"`
}

type sessionResponse struct {
	SID          string   `json:"sid"`
	ExpiresInSec int      `json:"expiresInSec"`
	TestHosts    []string `json:"testHosts"`
	Connections  int      `json:"connections"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Connections == nil || *req.Connections < 1 || *req.Connections > s.cfg.MaxConnections {
		writeError(w, http.StatusBadRequest, "bad_connections",
			"connections must be between 1 and "+strconv.Itoa(s.cfg.MaxConnections))
		return
	}
	n := *req.Connections
	hosts := s.cfg.EffectiveTestHosts()
	if s.cfg.SingleOrigin() {
		n = min(n, config.SingleOriginMaxConnections)
	} else {
		need := (n + config.ConnectionsPerHost - 1) / config.ConnectionsPerHost
		hosts = hosts[:min(need, len(hosts))]
	}
	sess, err := s.store.Create(ClientIP(r, s.cfg.TrustedProxies), n, UserFromContext(r.Context()))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "server is shutting down")
		return
	}
	s.log.Info("session created", "sid", sess.ID, "client_ip", sess.ClientIP.String(),
		"user", sess.User, "connections", n)
	writeJSON(w, http.StatusCreated, sessionResponse{
		SID:          sess.ID,
		ExpiresInSec: int(s.cfg.SessionTTL / time.Second),
		TestHosts:    hosts,
		Connections:  n,
	})
}

// statusWriter records the status for request logging. Unwrap keeps
// http.ResponseController (deadlines, hijack, flush) working.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || !s.log.Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration", time.Since(start).String(), "remote", r.RemoteAddr)
	})
}
