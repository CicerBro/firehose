package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ServerConfig is the GET /api/config response (§5.5).
type ServerConfig struct {
	ServerName string   `json:"serverName"`
	Version    string   `json:"version"`
	TestHosts  []string `json:"testHosts"`
	Limits     struct {
		MaxConnections   int   `json:"maxConnections"`
		MaxDownloadBytes int64 `json:"maxDownloadBytes"`
		MaxUploadBytes   int64 `json:"maxUploadBytes"`
	} `json:"limits"`
	RTC struct {
		Enabled bool `json:"enabled"`
	} `json:"rtc"`
	SingleOrigin bool `json:"singleOrigin"`
}

type ipInfo struct {
	IP     string `json:"ip"`
	Family string `json:"family"`
}

type sessionInfo struct {
	SID          string   `json:"sid"`
	ExpiresInSec int      `json:"expiresInSec"`
	TestHosts    []string `json:"testHosts"`
	Connections  int      `json:"connections"`
}

type apiErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// api performs the main-host JSON calls.
type api struct {
	base   string // normalized server origin, no trailing slash
	hc     *http.Client
	header http.Header
}

// setHeaders adds the auth and user-agent headers to req.
func setHeaders(req *http.Request, h http.Header) {
	for k, v := range h {
		req.Header[k] = v
	}
}

// statusError converts a non-2xx response into an error. 401 and 3xx are
// AuthErrors; others carry the server's error message.
func statusError(resp *http.Response) error {
	u := redactURL(resp.Request.URL)
	if resp.StatusCode == http.StatusUnauthorized || (resp.StatusCode >= 300 && resp.StatusCode < 400) {
		return &AuthError{Status: resp.StatusCode, Location: resp.Header.Get("Location"), URL: u}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var eb apiErrorBody
	if json.Unmarshal(body, &eb) == nil && eb.Error != "" {
		msg := eb.Error
		if eb.Message != "" {
			msg += ": " + eb.Message
		}
		return fmt.Errorf("%s: HTTP %d (%s)", u, resp.StatusCode, msg)
	}
	return fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
}

func (a *api) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return 0, err
	}
	setHeaders(req, a.header)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, statusError(resp)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return resp.StatusCode, fmt.Errorf("%s%s: unexpected content type %q (is this a speedtest server?)", a.base, path, ct)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("%s%s: invalid JSON: %w", a.base, path, err)
	}
	return resp.StatusCode, nil
}

func (a *api) config(ctx context.Context) (ServerConfig, error) {
	var c ServerConfig
	_, err := a.do(ctx, http.MethodGet, "/api/config", nil, &c)
	return c, err
}

func (a *api) ip(ctx context.Context) (ipInfo, error) {
	var ip ipInfo
	_, err := a.do(ctx, http.MethodGet, "/api/ip", nil, &ip)
	return ip, err
}

func (a *api) session(ctx context.Context, connections int) (sessionInfo, int, error) {
	var s sessionInfo
	st, err := a.do(ctx, http.MethodPost, "/api/session", map[string]int{"connections": connections}, &s)
	return s, st, err
}
