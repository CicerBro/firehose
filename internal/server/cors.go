package server

import "net/http"

// cors adds credentialed CORS headers for the exact PUBLIC_URL origin on the
// transfer endpoints (§5.5) and answers OPTIONS itself (204). Preflights carry
// no cookies, so OPTIONS is answered before the auth check.
func (s *Server) cors(next http.Handler) http.Handler {
	allowed := s.cfg.PublicURL
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		match := origin != "" && origin == allowed
		if match {
			h.Set("Access-Control-Allow-Origin", allowed)
			h.Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == http.MethodOptions {
			if match {
				h.Set("Access-Control-Allow-Methods", "GET, POST")
				if reqHdr := r.Header.Get("Access-Control-Request-Headers"); reqHdr != "" {
					h.Set("Access-Control-Allow-Headers", reqHdr)
				}
				h.Set("Access-Control-Max-Age", "7200")
			}
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
