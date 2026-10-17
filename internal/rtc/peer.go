package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/CicerBro/speedtest/internal/session"
)

// peer is one server-side peer connection running the §8.2 protocol.
type peer struct {
	h      *Handler
	pc     *webrtc.PeerConnection
	sess   *session.Session
	ctx    context.Context // ends the peer: timeout, session end, Close, ctl closed, ICE failed, done
	cancel context.CancelFunc
	base   time.Time // probe clock origin

	mu        sync.Mutex
	ctl       *webrtc.DataChannel
	probe     *webrtc.DataChannel
	probeOpen chan struct{} // closed when the probe channel is known
	started   bool
	ended     bool
	pps       int
	duration  int
	size      int
	sentDown  int
	sendDone  chan struct{} // closed when the down sender finished
	up        *receiver
}

// startPeer wires callbacks and starts the lifecycle goroutine, which
// closes the PC and frees its slot when p.ctx ends.
func (h *Handler) startPeer(pc *webrtc.PeerConnection, sess *session.Session) *peer {
	ctx, cancel := context.WithTimeout(h.ctx, h.opts.PeerTimeout)
	p := &peer{
		h:         h,
		pc:        pc,
		sess:      sess,
		ctx:       ctx,
		cancel:    cancel,
		base:      time.Now(),
		probeOpen: make(chan struct{}),
		sendDone:  make(chan struct{}),
		up:        newReceiver(MaxProbes),
	}
	stopSess := context.AfterFunc(sess.Context(), cancel)

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			cancel()
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case LabelCtl:
			p.mu.Lock()
			if p.ctl != nil {
				p.mu.Unlock()
				return
			}
			p.ctl = dc
			p.mu.Unlock()
			dc.OnMessage(p.onCtl)
			dc.OnClose(cancel)
		case LabelProbe:
			p.mu.Lock()
			if p.probe != nil {
				p.mu.Unlock()
				return
			}
			p.probe = dc
			close(p.probeOpen)
			p.mu.Unlock()
			dc.OnMessage(p.onProbe)
		}
	})

	h.active.Add(1)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		<-ctx.Done()
		stopSess()
		if err := pc.Close(); err != nil {
			h.log.Debug("rtc: close peer connection", "err", err)
		}
		h.active.Add(-1)
		<-h.slots
		h.log.Debug("rtc: peer closed", "sid", sess.ID, "reason", context.Cause(ctx))
	}()
	return p
}

// negotiate applies the offer and returns the answer once ICE gathering is
// complete (non-trickle). status is the HTTP status to use on error.
func (p *peer) negotiate(reqCtx context.Context, offer string) (string, int, error) {
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", http.StatusBadRequest, errors.New("invalid offer")
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", http.StatusBadRequest, errors.New("cannot answer offer")
	}
	gathered := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return "", http.StatusInternalServerError, errors.New("cannot set local description")
	}
	select {
	case <-gathered:
	case <-reqCtx.Done():
		return "", http.StatusBadRequest, reqCtx.Err()
	case <-p.ctx.Done():
		return "", http.StatusServiceUnavailable, errors.New("peer closed")
	}
	ld := p.pc.LocalDescription()
	if ld == nil {
		return "", http.StatusInternalServerError, errors.New("no local description")
	}
	return ld.SDP, http.StatusOK, nil
}

func (p *peer) nowUs() uint64 { return uint64(time.Since(p.base).Microseconds()) }

func (p *peer) onProbe(msg webrtc.DataChannelMessage) {
	recv := p.nowUs()
	kind, seq, sendUs, ok := DecodeProbe(msg.Data)
	if !ok || kind != KindUp {
		return
	}
	if drop := p.h.opts.DropProbe; drop != nil && drop(KindUp, seq) {
		return
	}
	p.mu.Lock()
	p.up.add(seq, float64(sendUs), float64(recv))
	p.mu.Unlock()
}

func (p *peer) onCtl(msg webrtc.DataChannelMessage) {
	if !msg.IsString {
		return
	}
	var m ctlMsg
	if err := json.Unmarshal(msg.Data, &m); err != nil {
		return
	}
	switch m.Type {
	case "start":
		p.mu.Lock()
		if p.started || !validParams(m.PPS, m.DurationMs, m.Size) {
			p.mu.Unlock()
			return
		}
		p.started = true
		p.pps, p.duration, p.size = m.PPS, m.DurationMs, m.Size
		p.mu.Unlock()
		p.sess.Touch()
		go p.sendLoop()
	case "end":
		if m.Sent == nil {
			return
		}
		p.mu.Lock()
		if !p.started || p.ended {
			p.mu.Unlock()
			return
		}
		p.ended = true
		p.mu.Unlock()
		go p.finish(min(max(*m.Sent, 0), MaxProbes))
	}
}

// sendLoop paces down probes with a 10 ms ticker, sending the cumulative
// target pps × elapsed so the total is exact regardless of tick jitter.
func (p *peer) sendLoop() {
	defer close(p.sendDone)
	// The probe channel can open a moment after ctl delivered "start".
	wait := time.NewTimer(2 * time.Second)
	select {
	case <-p.probeOpen:
		wait.Stop()
	case <-wait.C:
		return
	case <-p.ctx.Done():
		wait.Stop()
		return
	}
	p.mu.Lock()
	dc, pps, size := p.probe, p.pps, p.size
	total := TotalProbes(p.pps, p.duration)
	p.mu.Unlock()

	buf := make([]byte, size)
	start := time.Now()
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	seq := 0
	for {
		target := min(total, int(time.Since(start).Microseconds()*int64(pps)/1e6)+1)
		for ; seq < target; seq++ {
			// A failed local send counts as sent: it is loss either way.
			if drop := p.h.opts.DropProbe; drop == nil || !drop(KindDown, uint32(seq)) {
				EncodeProbe(buf, KindDown, uint32(seq), p.nowUs())
				_ = dc.Send(buf)
			}
		}
		p.mu.Lock()
		p.sentDown = seq
		p.mu.Unlock()
		if seq >= total {
			return
		}
		select {
		case <-t.C:
		case <-p.ctx.Done():
			return
		}
	}
}

// finish waits for the down sender and the straggler grace, then reports.
// The PC is closed shortly after (the client normally closes it first).
func (p *peer) finish(sentUp int) {
	select {
	case <-p.sendDone:
	case <-p.ctx.Done():
		return
	}
	grace := time.NewTimer(p.h.opts.Grace)
	defer grace.Stop()
	select {
	case <-grace.C:
	case <-p.ctx.Done():
		return
	}
	p.mu.Lock()
	received, jitter := p.up.summary(sentUp)
	rep := reportMsg{
		Type:        "report",
		SentDown:    p.sentDown,
		ReceivedUp:  received,
		JitterUpMs:  round2(jitter),
		ReorderedUp: p.up.reordered,
	}
	ctl := p.ctl
	p.mu.Unlock()
	p.sess.Touch()
	b, _ := json.Marshal(rep)
	if err := ctl.SendText(string(b)); err != nil {
		p.cancel()
		return
	}
	linger := time.NewTimer(postReportLinger)
	defer linger.Stop()
	select {
	case <-linger.C:
		p.cancel()
	case <-p.ctx.Done():
	}
}
