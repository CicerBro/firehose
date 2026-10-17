package rtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	"github.com/CicerBro/speedtest/internal/stats"
)

// DefaultConnectTimeout is how long the client waits for ICE + DTLS + SCTP
// (both data channels open) before reporting the test unavailable (§8.3).
const DefaultConnectTimeout = 5 * time.Second

// ClientOptions configure RunClient. Zero values get defaults.
type ClientOptions struct {
	// OfferURL is the full signaling URL, e.g.
	// https://speed.example.com/api/rtc/offer?sid=<sid>.
	OfferURL string
	// HTTPClient performs the offer POST (default http.DefaultClient).
	HTTPClient *http.Client
	// Header is added to the offer request (e.g. Authorization: Bearer …).
	Header http.Header

	PPS        int // default 100
	DurationMs int // default 6000
	Size       int // default 200

	// Network restricts ICE to "udp4" (-4) or "udp6" (-6); "" uses both.
	Network string
	// ConnectTimeout bounds connection setup after the answer (default 5 s).
	ConnectTimeout time.Duration
	// Grace is the straggler wait after the last probe (default 1 s).
	Grace time.Duration
}

// ClientResult is the §7.5 "loss" object.
type ClientResult struct {
	Available     bool    `json:"available"`
	DownPct       float64 `json:"downPct"`
	UpPct         float64 `json:"upPct"`
	JitterDownMs  float64 `json:"jitterDownMs"`
	JitterUpMs    float64 `json:"jitterUpMs"`
	SentDown      int     `json:"sentDown"`
	SentUp        int     `json:"sentUp"`
	ReorderedDown int     `json:"reorderedDown"`
}

func errorf(format string, a ...any) error { return fmt.Errorf("rtc: "+format, a...) }

// RunClient runs one packet-loss test against the server. It blocks for
// about DurationMs + Grace (+ setup). If ICE does not connect within
// ConnectTimeout (UDP blocked), it returns Available=false and a nil error.
// Other failures (HTTP errors such as *HTTPError, a bad answer, protocol
// errors, ctx cancellation) return a non-nil error with Available=false.
func RunClient(ctx context.Context, o ClientOptions) (ClientResult, error) {
	if o.PPS == 0 {
		o.PPS = DefaultPPS
	}
	if o.DurationMs == 0 {
		o.DurationMs = DefaultDurationMs
	}
	if o.Size == 0 {
		o.Size = DefaultSize
	}
	if !validParams(o.PPS, o.DurationMs, o.Size) {
		return ClientResult{}, errorf("invalid parameters pps=%d durationMs=%d size=%d", o.PPS, o.DurationMs, o.Size)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = DefaultConnectTimeout
	}
	if o.Grace <= 0 {
		o.Grace = DefaultGrace
	}
	var nets []webrtc.NetworkType
	switch o.Network {
	case "":
		nets = []webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6}
	case "udp4":
		nets = []webrtc.NetworkType{webrtc.NetworkTypeUDP4}
	case "udp6":
		nets = []webrtc.NetworkType{webrtc.NetworkTypeUDP6}
	default:
		return ClientResult{}, errorf("invalid network %q", o.Network)
	}

	var se webrtc.SettingEngine
	se.SetNetworkTypes(nets)
	se.SetIncludeLoopbackCandidate(true) // a server on this host
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return ClientResult{}, errorf("new peer connection: %w", err)
	}
	defer pc.Close()

	c := &client{
		o:      o,
		base:   time.Now(),
		total:  TotalProbes(o.PPS, o.DurationMs),
		report: make(chan reportMsg, 1),
		failed: make(chan struct{}),
	}
	c.down = newReceiver(c.total)

	ctl, err := pc.CreateDataChannel(LabelCtl, nil)
	if err != nil {
		return ClientResult{}, errorf("create ctl channel: %w", err)
	}
	ordered, retransmits := false, uint16(0)
	probe, err := pc.CreateDataChannel(LabelProbe, &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retransmits})
	if err != nil {
		return ClientResult{}, errorf("create probe channel: %w", err)
	}
	ctlOpen, probeOpen := make(chan struct{}), make(chan struct{})
	ctl.OnOpen(func() { close(ctlOpen) })
	probe.OnOpen(func() { close(probeOpen) })
	ctl.OnMessage(c.onCtl)
	probe.OnMessage(c.onProbe)
	var failOnce sync.Once
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			failOnce.Do(func() { close(c.failed) })
		}
	})

	// Non-trickle: gather everything, then POST the complete offer.
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return ClientResult{}, errorf("create offer: %w", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return ClientResult{}, errorf("set local description: %w", err)
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return ClientResult{}, ctx.Err()
	}
	answer, err := c.exchange(ctx, pc.LocalDescription().SDP)
	if err != nil {
		return ClientResult{}, err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		return ClientResult{}, errorf("set remote description: %w", err)
	}

	connect := time.NewTimer(o.ConnectTimeout)
	defer connect.Stop()
	for _, ch := range []chan struct{}{ctlOpen, probeOpen} {
		select {
		case <-ch:
		case <-connect.C:
			return ClientResult{}, nil // UDP blocked: unavailable, not an error
		case <-c.failed:
			return ClientResult{}, nil
		case <-ctx.Done():
			return ClientResult{}, ctx.Err()
		}
	}

	start, _ := json.Marshal(ctlMsg{Type: "start", PPS: o.PPS, DurationMs: o.DurationMs, Size: o.Size})
	if err := ctl.SendText(string(start)); err != nil {
		return ClientResult{}, errorf("send start: %w", err)
	}
	sentUp, err := c.sendLoop(ctx, probe)
	if err != nil {
		return ClientResult{}, err
	}
	sentUpCopy := sentUp
	end, _ := json.Marshal(ctlMsg{Type: "end", Sent: &sentUpCopy})
	if err := ctl.SendText(string(end)); err != nil {
		return ClientResult{}, errorf("send end: %w", err)
	}

	// Wait for the server report and at least the straggler grace.
	graceDone := time.After(o.Grace)
	reportWait := time.NewTimer(o.Grace + 5*time.Second)
	defer reportWait.Stop()
	var rep reportMsg
	select {
	case rep = <-c.report:
	case <-reportWait.C:
		return ClientResult{}, errorf("no report from server")
	case <-c.failed:
		return ClientResult{}, errorf("connection closed before report")
	case <-ctx.Done():
		return ClientResult{}, ctx.Err()
	}
	select {
	case <-graceDone:
	case <-ctx.Done():
		return ClientResult{}, ctx.Err()
	}

	c.mu.Lock()
	receivedDown, jitterDown := c.down.summary(rep.SentDown)
	reordered := c.down.reordered
	c.mu.Unlock()
	_ = ctl.Close()
	return ClientResult{
		Available:     true,
		DownPct:       stats.LossPct(rep.SentDown, receivedDown),
		UpPct:         stats.LossPct(sentUp, rep.ReceivedUp),
		JitterDownMs:  round2(jitterDown),
		JitterUpMs:    round2(rep.JitterUpMs),
		SentDown:      rep.SentDown,
		SentUp:        sentUp,
		ReorderedDown: reordered,
	}, nil
}

type client struct {
	o      ClientOptions
	base   time.Time
	total  int
	report chan reportMsg
	failed chan struct{}

	mu   sync.Mutex
	down *receiver
}

func (c *client) nowUs() uint64 { return uint64(time.Since(c.base).Microseconds()) }

func (c *client) exchange(ctx context.Context, offer string) (string, error) {
	body, _ := json.Marshal(sdpBody{SDP: offer})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.o.OfferURL, bytes.NewReader(body))
	if err != nil {
		return "", errorf("offer request: %w", err)
	}
	for k, vs := range c.o.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.o.HTTPClient.Do(req)
	if err != nil {
		return "", errorf("offer: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxOfferBytes))
	if err != nil {
		return "", errorf("offer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{Status: resp.StatusCode, Body: string(bytes.TrimSpace(b))}
	}
	var ans sdpBody
	if err := json.Unmarshal(b, &ans); err != nil || ans.SDP == "" {
		return "", errorf("offer: invalid answer")
	}
	return ans.SDP, nil
}

// HTTPError is returned when the offer endpoint answers with a non-200
// status (e.g. 401 without a valid token, 410 for an expired session).
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("rtc: offer: HTTP %d: %s", e.Status, e.Body)
}

func (c *client) onCtl(msg webrtc.DataChannelMessage) {
	if !msg.IsString {
		return
	}
	var r reportMsg
	if err := json.Unmarshal(msg.Data, &r); err != nil || r.Type != "report" {
		return
	}
	select {
	case c.report <- r:
	default:
	}
}

func (c *client) onProbe(msg webrtc.DataChannelMessage) {
	recv := c.nowUs()
	kind, seq, sendUs, ok := DecodeProbe(msg.Data)
	if !ok || kind != KindDown {
		return
	}
	c.mu.Lock()
	c.down.add(seq, float64(sendUs), float64(recv))
	c.mu.Unlock()
}

// sendLoop sends up probes paced by a 10 ms ticker (cumulative target).
func (c *client) sendLoop(ctx context.Context, dc *webrtc.DataChannel) (int, error) {
	buf := make([]byte, c.o.Size)
	start := time.Now()
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	seq := 0
	for {
		target := min(c.total, int(time.Since(start).Microseconds()*int64(c.o.PPS)/1e6)+1)
		for ; seq < target; seq++ {
			// A failed local send counts as sent: it is loss either way.
			EncodeProbe(buf, KindUp, uint32(seq), c.nowUs())
			_ = dc.Send(buf)
		}
		if seq >= c.total {
			return seq, nil
		}
		select {
		case <-t.C:
		case <-c.failed:
			return 0, errors.New("rtc: connection closed during test")
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}
