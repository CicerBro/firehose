package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"

	"github.com/CicerBro/firehose/internal/config"
	"github.com/CicerBro/firehose/internal/server"
	"github.com/CicerBro/firehose/internal/session"
)

// Server-side defaults.
const (
	DefaultPeerTimeout = 20 * time.Second // hard cap per peer connection (§8.1)
	DefaultGrace       = time.Second      // straggler grace before "report" (§8.2)
	slotWait           = 2 * time.Second  // wait for a free slot (a preempted session's PC is closing)
	postReportLinger   = 3 * time.Second  // keep the PC after "report" so it is delivered
	maxOfferBytes      = 64 << 10
)

// Options configure a Handler. Zero values get defaults.
type Options struct {
	// ListenAddr is the UDP address for the single ICE UDP mux. Default
	// "[::]:<UDPPort>" (dual-stack), falling back to "0.0.0.0:<UDPPort>"
	// when IPv6 is unavailable.
	ListenAddr string
	UDPPort    int
	// PublicIPs are advertised instead of the local addresses as ICE host
	// candidates (server behind 1:1 NAT / Docker bridge).
	PublicIPs []string
	// IncludeLoopback also advertises loopback host candidates (dev, tests).
	IncludeLoopback bool
	// MaxPeers is the maximum number of concurrent peer connections
	// (MAX_ACTIVE_SESSIONS). Default 1.
	MaxPeers int
	// PeerTimeout is the hard lifetime of a peer connection. Default 20 s.
	PeerTimeout time.Duration
	// Grace is the straggler wait before "report". Default 1 s.
	Grace  time.Duration
	Logger *slog.Logger
	// DropProbe, if set, simulates loss (tests): a down probe for which it
	// returns true is counted as sent but not sent; an up probe is ignored.
	DropProbe func(kind byte, seq uint32) bool

	// wrapConn wraps the UDP socket (tests: simulate blocked UDP).
	wrapConn func(net.PacketConn) net.PacketConn
}

// Handler serves POST /api/rtc/offer?sid=… and owns the UDP socket and all
// peer connections. Close releases them.
type Handler struct {
	opts  Options
	store *session.Store
	log   *slog.Logger
	api   *webrtc.API
	conn  net.PacketConn
	mux   ice.UDPMux
	slots chan struct{}

	ctx    context.Context // cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup
	active atomic.Int32
	once   sync.Once
}

// NewFactory returns a server.RTCFactory that builds a Handler from the
// server config (RTC_UDP_PORT, RTC_PUBLIC_IPS, MAX_ACTIVE_SESSIONS; loopback
// candidates in --dev). Non-zero fields of o override the config.
func NewFactory(o Options) server.RTCFactory {
	return func(cfg config.Config, store *session.Store, log *slog.Logger) (http.Handler, error) {
		if o.UDPPort == 0 {
			o.UDPPort = cfg.RTCUDPPort
		}
		if o.PublicIPs == nil {
			o.PublicIPs = cfg.RTCPublicIPs
		}
		if o.MaxPeers == 0 {
			o.MaxPeers = cfg.MaxActiveSessions
		}
		if cfg.Dev {
			o.IncludeLoopback = true
		}
		if o.Logger == nil {
			o.Logger = log
		}
		return NewHandler(store, o)
	}
}

// NewHandler opens the UDP socket and builds the pion API.
func NewHandler(store *session.Store, o Options) (*Handler, error) {
	if o.MaxPeers <= 0 {
		o.MaxPeers = 1
	}
	if o.PeerTimeout <= 0 {
		o.PeerTimeout = DefaultPeerTimeout
	}
	if o.Grace <= 0 {
		o.Grace = DefaultGrace
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	conn, err := listenUDP(o)
	if err != nil {
		return nil, err
	}
	if o.wrapConn != nil {
		conn = o.wrapConn(conn)
	}
	mux := webrtc.NewICEUDPMux(nil, conn)

	var se webrtc.SettingEngine
	se.SetICEUDPMux(mux)
	se.SetLite(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIncludeLoopbackCandidate(o.IncludeLoopback)
	if len(o.PublicIPs) > 0 {
		err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        o.PublicIPs,
			AsCandidateType: webrtc.ICECandidateTypeHost,
			Mode:            webrtc.ICEAddressRewriteReplace,
		})
		if err != nil {
			_ = mux.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("rtc public IPs: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Handler{
		opts:   o,
		store:  store,
		log:    o.Logger,
		api:    webrtc.NewAPI(webrtc.WithSettingEngine(se)),
		conn:   conn,
		mux:    mux,
		slots:  make(chan struct{}, o.MaxPeers),
		ctx:    ctx,
		cancel: cancel,
	}
	h.log.Info("rtc listening", "udp", conn.LocalAddr().String(), "public_ips", o.PublicIPs, "max_peers", o.MaxPeers)
	return h, nil
}

func listenUDP(o Options) (net.PacketConn, error) {
	if o.ListenAddr != "" {
		return net.ListenPacket("udp", o.ListenAddr)
	}
	port := strconv.Itoa(o.UDPPort)
	conn, err := net.ListenPacket("udp", net.JoinHostPort("::", port))
	if err == nil {
		return conn, nil
	}
	conn, err4 := net.ListenPacket("udp4", net.JoinHostPort("0.0.0.0", port))
	if err4 != nil {
		return nil, fmt.Errorf("rtc listen udp port %s: %w", port, errors.Join(err, err4))
	}
	return conn, nil
}

// LocalAddr is the UDP socket address (tests use an ephemeral port).
func (h *Handler) LocalAddr() net.Addr { return h.conn.LocalAddr() }

// ActivePeers is the number of open peer connections.
func (h *Handler) ActivePeers() int { return int(h.active.Load()) }

// Close closes every peer connection, waits for them and closes the UDP
// socket. Idempotent.
func (h *Handler) Close() error {
	var err error
	h.once.Do(func() {
		h.cancel()
		h.wg.Wait()
		err = h.mux.Close() // also closes the UDP socket
		if cerr := h.conn.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			err = errors.Join(err, cerr)
		}
	})
	return err
}

type sdpBody struct {
	SDP string `json:"sdp"`
}

// ServeHTTP handles the non-trickle SDP exchange.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	sess, ok := h.store.Get(r.URL.Query().Get("sid"))
	if !ok {
		writeError(w, http.StatusGone, "session_expired", "unknown or expired session")
		return
	}
	sess.Touch()
	var body sdpBody
	if err := json.NewDecoder(io.LimitReader(r.Body, maxOfferBytes)).Decode(&body); err != nil || body.SDP == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "expected {\"sdp\": \"<offer>\"}")
		return
	}
	if h.ctx.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "server is shutting down")
		return
	}

	// Acquire a peer slot; a preempted session's PC may still be closing.
	wait := time.NewTimer(slotWait)
	select {
	case h.slots <- struct{}{}:
		wait.Stop()
	case <-wait.C:
		writeError(w, http.StatusServiceUnavailable, "rtc_busy", "too many concurrent packet-loss tests")
		return
	case <-r.Context().Done():
		wait.Stop()
		return
	case <-h.ctx.Done():
		wait.Stop()
		writeError(w, http.StatusServiceUnavailable, "shutting_down", "server is shutting down")
		return
	}

	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		<-h.slots
		h.log.Error("rtc: new peer connection", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "cannot create peer connection")
		return
	}
	p := h.startPeer(pc, sess)

	answer, status, err := p.negotiate(r.Context(), body.SDP)
	if err != nil {
		p.cancel()
		code := "bad_request"
		if status != http.StatusBadRequest {
			code = "internal"
		}
		h.log.Debug("rtc: negotiation failed", "sid", sess.ID, "err", err)
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sdpBody{SDP: answer})
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, struct {
		Error   string `json:"error"`
		Message string `json:"message,omitempty"`
	}{code, msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
