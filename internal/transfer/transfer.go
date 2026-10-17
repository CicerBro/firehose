// Package transfer implements the download (/api/down) and upload (/api/up)
// stream handlers (§5.5) and the shared random payload (§5.6).
package transfer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/CicerBro/speedtest/internal/session"
)

const (
	// RandomBufferSize is the size of the shared random payload.
	RandomBufferSize = 4 << 20
	// MaxWriteChunk is the largest single Write (bypasses net/http's bufio).
	MaxWriteChunk = 1 << 20
	// ReadBufferSize is the pooled upload read buffer size.
	ReadBufferSize = 256 << 10
)

// Options configure the handlers.
type Options struct {
	MaxDownloadBytes   int64
	MaxUploadBytes     int64
	MaxRequestDuration time.Duration
	// MaxStreams is the per-session, per-direction active stream cap
	// (§5.5: maxConnections + 2).
	MaxStreams int
	Logger     *slog.Logger
}

// Handler serves /api/down and /api/up.
type Handler struct {
	store   *session.Store
	opts    Options
	log     *slog.Logger
	payload []byte
	bufs    sync.Pool
}

// New creates the handlers and fills the 4 MiB random payload from crypto/rand.
func New(store *session.Store, opts Options) (*Handler, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxStreams <= 0 {
		return nil, errors.New("transfer: MaxStreams must be > 0")
	}
	if opts.MaxDownloadBytes <= 0 || opts.MaxUploadBytes <= 0 || opts.MaxRequestDuration <= 0 {
		return nil, errors.New("transfer: byte and duration limits must be > 0")
	}
	payload := make([]byte, RandomBufferSize)
	if _, err := rand.Read(payload); err != nil {
		return nil, err
	}
	h := &Handler{store: store, opts: opts, log: opts.Logger, payload: payload}
	h.bufs.New = func() any {
		b := make([]byte, ReadBufferSize)
		return &b
	}
	return h, nil
}

// Down handles GET /api/down?sid=…&bytes=N.
func (h *Handler) Down(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.lookup(w, r)
	if !ok {
		return
	}
	n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bytes must be an integer")
		return
	}
	if n < 1 || n > h.opts.MaxDownloadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large",
			"bytes must be between 1 and "+strconv.FormatInt(h.opts.MaxDownloadBytes, 10))
		return
	}
	if !sess.AcquireDown(h.opts.MaxStreams) {
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "session stream limit reached")
		return
	}
	defer sess.ReleaseDown()
	sess.Touch()
	defer sess.Touch()

	ctx, cleanup := h.prepare(w, r, sess, false)
	defer cleanup()

	hdr := w.Header()
	hdr.Set("Content-Type", "application/octet-stream")
	hdr.Set("Content-Length", strconv.FormatInt(n, 10))
	hdr.Set("Cache-Control", "no-store")
	hdr.Del("Content-Encoding")
	w.WriteHeader(http.StatusOK)

	off := mrand.IntN(len(h.payload))
	remaining := n
	for remaining > 0 {
		if ctx.Err() != nil {
			return
		}
		chunk := h.payload[off:min(len(h.payload), off+MaxWriteChunk)]
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		wn, err := w.Write(chunk)
		if wn > 0 {
			sess.AddDown(int64(wn))
			remaining -= int64(wn)
			off += wn
			if off >= len(h.payload) {
				off = 0
			}
		}
		if err != nil {
			return
		}
	}
}

type upResponse struct {
	Bytes      int64 `json:"bytes"`
	DurationMs int64 `json:"durationMs"`
}

// Up handles POST /api/up?sid=….
func (h *Handler) Up(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.lookup(w, r)
	if !ok {
		return
	}
	if r.ContentLength > h.opts.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "upload body too large")
		return
	}
	if !sess.AcquireUp(h.opts.MaxStreams) {
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "session stream limit reached")
		return
	}
	defer sess.ReleaseUp()
	sess.Touch()
	defer sess.Touch()

	_, cleanup := h.prepare(w, r, sess, true)
	defer cleanup()

	body := http.MaxBytesReader(w, r.Body, h.opts.MaxUploadBytes)
	bp := h.bufs.Get().(*[]byte)
	defer h.bufs.Put(bp)
	buf := *bp

	start := time.Now()
	var total int64
	for {
		n, err := body.Read(buf)
		if n > 0 {
			total += int64(n)
			sess.AddUp(int64(n)) // per Read: live upload stats
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			switch {
			case errors.As(err, &mbe):
				writeError(w, http.StatusRequestEntityTooLarge, "too_large", "upload body too large")
			case !sess.Alive():
				writeError(w, http.StatusGone, "session_expired", "session expired")
			}
			// Otherwise the client went away or the deadline passed.
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, upResponse{Bytes: total, DurationMs: time.Since(start).Milliseconds()})
}

// lookup validates the sid query parameter (410 if unknown or expired).
func (h *Handler) lookup(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	sess, ok := h.store.Get(r.URL.Query().Get("sid"))
	if !ok {
		writeError(w, http.StatusGone, "session_expired", "unknown or expired session")
		return nil, false
	}
	return sess, true
}

// prepare sets the per-request deadline and returns a context cancelled by
// either the request or the session. When the session ends, the connection
// deadline is moved to now so a blocked Read/Write returns immediately.
// cleanup must be deferred: it clears the deadlines so a keep-alive
// connection is reusable (net/http does not reset write deadlines).
func (h *Handler) prepare(w http.ResponseWriter, r *http.Request, sess *session.Session, read bool) (context.Context, func()) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(h.opts.MaxRequestDuration)
	_ = rc.SetWriteDeadline(deadline)
	if read {
		_ = rc.SetReadDeadline(deadline)
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(sess.Context(), func() {
		cancel()
		now := time.Now()
		_ = rc.SetWriteDeadline(now)
		if read {
			_ = rc.SetReadDeadline(now)
		}
	})
	return ctx, func() {
		fired := !stop()
		cancel()
		if fired {
			return // connection deadline was forced; let net/http close it
		}
		_ = rc.SetWriteDeadline(time.Time{})
		if read {
			_ = rc.SetReadDeadline(time.Time{})
		}
	}
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
