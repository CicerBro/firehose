package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultDownloadBytes is the size of one download request (§7.3); the
	// request restarts automatically if it finishes early.
	DefaultDownloadBytes = 2 << 30
	// downloadBufferSize is each stream's read buffer.
	downloadBufferSize = 1 << 20
	// maxStreamFailures is how many consecutive failed requests (without
	// any bytes transferred in between) make a stream give up.
	maxStreamFailures = 3
	retryDelay        = 100 * time.Millisecond
)

var downloadBufs = sync.Pool{New: func() any {
	b := make([]byte, downloadBufferSize)
	return &b
}}

// stream is one test connection: its own transport, so its own TCP
// connection (MaxConnsPerHost 1).
type stream struct {
	base string // test host origin
	tr   *http.Transport
	hc   *http.Client
}

// streamGroup tracks the outcome of the streams of one phase.
type streamGroup struct {
	cancel  context.CancelFunc
	mu      sync.Mutex
	fatal   error // first error that ends the whole phase (auth, session gone)
	lastErr error // last per-stream error, for the "all streams failed" message
	gaveUp  int   // streams that stopped after maxStreamFailures
}

func (g *streamGroup) fail(err error) {
	g.mu.Lock()
	if g.fatal == nil {
		g.fatal = err
	}
	g.mu.Unlock()
	g.cancel()
}

func (g *streamGroup) giveUp(err error) {
	g.mu.Lock()
	g.gaveUp++
	g.lastErr = err
	g.mu.Unlock()
}

func (g *streamGroup) note(err error) {
	g.mu.Lock()
	g.lastErr = err
	g.mu.Unlock()
}

// result returns the phase error, if any: a fatal error, or every stream
// having given up.
func (g *streamGroup) result(n int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fatal != nil {
		return g.fatal
	}
	if g.gaveUp >= n {
		return fmt.Errorf("all %d streams failed: %w", n, g.lastErr)
	}
	return nil
}

// fatalTransferError returns the error if a transfer response ends the phase:
// authentication problems and an expired session cannot be retried.
func fatalTransferError(resp *http.Response) error {
	err := statusError(resp)
	var ae *AuthError
	if errors.As(err, &ae) || resp.StatusCode == http.StatusGone {
		return err
	}
	return nil
}

// runStreams runs fn on every stream until ctx is done or fn returns, with
// retry bookkeeping handled by fn through g.
func runStreams(ctx context.Context, streams []*stream, fn func(ctx context.Context, i int, s *stream, g *streamGroup)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g := &streamGroup{cancel: cancel}
	var wg sync.WaitGroup
	for i, s := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx, i, s, g)
		}()
	}
	wg.Wait()
	return g.result(len(streams))
}

// sleepCtx waits d or until ctx is done; false if ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// downloadStream loops GET /api/down until ctx is done (§10.3), adding
// every received byte to total.
func downloadStream(ctx context.Context, s *stream, sid string, size int64, header http.Header, total *atomic.Int64, g *streamGroup) {
	bp := downloadBufs.Get().(*[]byte)
	defer downloadBufs.Put(bp)
	buf := *bp
	u := s.base + "/api/down?sid=" + url.QueryEscape(sid) + "&bytes=" + strconv.FormatInt(size, 10)
	failures := 0
	for ctx.Err() == nil {
		err := downloadOnce(ctx, s, u, header, buf, total, &failures, g)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			continue
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

func downloadOnce(ctx context.Context, s *stream, u string, header http.Header, buf []byte, total *atomic.Int64, failures *int, g *streamGroup) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
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
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			total.Add(int64(n))
			*failures = 0
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
