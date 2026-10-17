package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

type userKey struct{}

// UserFromContext returns the authenticated identity set by the auth
// middleware: the auth-header value, "token" for bearer auth, or "dev".
func UserFromContext(ctx context.Context) string {
	u, _ := ctx.Value(userKey{}).(string)
	return u
}

// authenticate implements §5.9. It returns the identity and whether the
// request is allowed.
func authenticate(r *http.Request, apiToken, authHeader string, dev bool) (string, bool) {
	if authz := r.Header.Get("Authorization"); authz != "" {
		if tok, ok := bearerToken(authz); ok {
			if apiToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(apiToken)) == 1 {
				return "token", true
			}
			if dev {
				return "dev", true
			}
			return "", false
		}
	}
	if dev {
		if u := r.Header.Get(authHeader); u != "" {
			return u, true
		}
		return "dev", true
	}
	if u := r.Header.Get(authHeader); u != "" {
		return u, true
	}
	return "", false
}

func bearerToken(h string) (string, bool) {
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// requireAuth wraps next with the §5.9 check.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := authenticate(r, s.cfg.APIToken, s.cfg.AuthHeader, s.cfg.Dev)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}
