package server

import (
	"fmt"
	"net/http"
	"strings"
)

// MetricsHandler serves Prometheus text-format metrics (hand-written, no
// dependency). Mounted at /metrics on METRICS_LISTEN by Run/Serve.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		st := s.store.Stats()
		var b strings.Builder
		metric := func(name, typ, help string, v any) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, v)
		}
		metric("firehose_sessions_active", "gauge", "Active test sessions.", st.Active)
		metric("firehose_sessions_created_total", "counter", "Sessions created.", st.Created)
		metric("firehose_sessions_preempted_total", "counter", "Sessions cancelled by a newer session (newest wins).", st.Preempted)
		metric("firehose_sessions_ended_total", "counter", "Sessions ended for any reason.", st.Expired)
		metric("firehose_download_bytes_total", "counter", "Bytes sent by /api/down.", st.DownBytes)
		metric("firehose_upload_bytes_total", "counter", "Bytes received by /api/up.", st.UpBytes)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(b.String()))
	})
	return mux
}
