package client

import (
	"context"
	"crypto/rand"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
)

const (
	// PayloadSize is the shared random upload buffer (§5.6, §10.3).
	PayloadSize = 4 << 20
	// DefaultUploadBytes is used when the server reports no upload limit.
	DefaultUploadBytes = 512 << 20
)

// payload is filled once from crypto/rand and only read afterwards.
var payload = sync.OnceValue(func() []byte {
	b := make([]byte, PayloadSize)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return b
})

// uploadReader serves exactly size bytes from the shared read-only random
// buffer, wrapping around, without allocating. Every Read adds its count to
// sent (the client-side byte count, used only when server stats are
// unavailable).
type uploadReader struct {
	buf       []byte
	off       int
	remaining int64
	sent      *atomic.Int64
}

// reset prepares r for a new request of size bytes starting at a random
// offset.
func (r *uploadReader) reset(buf []byte, size int64, sent *atomic.Int64) {
	r.buf = buf
	r.off = mrand.IntN(len(buf))
	r.remaining = size
	r.sent = sent
}

func (r *uploadReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n := 0
	for n < len(p) {
		c := copy(p[n:], r.buf[r.off:])
		n += c
		r.off += c
		if r.off >= len(r.buf) {
			r.off = 0
		}
	}
	r.remaining -= int64(n)
	if r.sent != nil {
		r.sent.Add(int64(n))
	}
	return n, nil
}

// Close makes uploadReader an io.ReadCloser so net/http does not wrap it.
func (r *uploadReader) Close() error { return nil }

// uploadStream loops POST /api/up with size-byte bodies until ctx is done.
func uploadStream(ctx context.Context, s *stream, sid string, size int64, header http.Header, sent *atomic.Int64, g *streamGroup) {
	u := s.base + "/api/up?sid=" + url.QueryEscape(sid)
	body := &uploadReader{}
	buf := payload()
	failures := 0
	for ctx.Err() == nil {
		before := sent.Load()
		body.reset(buf, size, sent)
		err := uploadOnce(ctx, s, u, header, body, size, g)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			failures = 0
			continue
		}
		if sent.Load() > before {
			failures = 0
		}
		failures++
		if failures >= maxStreamFailures {
			g.giveUp(err)
			return
		}
		g.note(err)
		if !sleepCtx(ctx, retryDelay) {
			return
		}
	}
}

func uploadOnce(ctx context.Context, s *stream, u string, header http.Header, body *uploadReader, size int64, g *streamGroup) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	setHeaders(req, header)
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if ferr := fatalTransferError(resp); ferr != nil {
			g.fail(ferr)
			return ferr
		}
		return statusError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
