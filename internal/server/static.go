package server

import (
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/CicerBro/firehose/internal/config"
)

// buildCSP builds the Content-Security-Policy from config (§5.10).
func buildCSP(cfg config.Config) string {
	connect := []string{"'self'"}
	seen := map[string]bool{"'self'": true}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			connect = append(connect, v)
		}
	}
	for _, h := range cfg.EffectiveTestHosts() {
		add(h)
	}
	if u, err := url.Parse(cfg.PublicURL); err == nil {
		scheme := "wss"
		if u.Scheme == "http" {
			scheme = "ws"
		}
		add(scheme + "://" + u.Host)
	}
	return "default-src 'self'; connect-src " + strings.Join(connect, " ") +
		"; worker-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'" +
		"; base-uri 'none'; frame-ancestors 'none'"
}

func hostOf(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Host
}

// staticHandler serves the embedded UI with cache and security headers.
func (s *Server) staticHandler() http.Handler {
	files := http.FileServerFS(s.ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "interest-cohort=()")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/assets/") && s.exists(r.URL.Path) {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) exists(urlPath string) bool {
	p := strings.TrimPrefix(urlPath, "/")
	if !fs.ValidPath(p) {
		return false
	}
	st, err := fs.Stat(s.ui, p)
	return err == nil && !st.IsDir()
}
