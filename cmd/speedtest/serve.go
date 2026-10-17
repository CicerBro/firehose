package main

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/CicerBro/speedtest/web"
)

// runServe is the M0 placeholder: embedded UI plus /healthz.
func runServe(args []string) int {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /", http.FileServerFS(web.FS))
	addr := "127.0.0.1:8080"
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "err", err)
		return 1
	}
	return 0
}
