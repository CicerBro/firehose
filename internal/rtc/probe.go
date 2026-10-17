// Package rtc implements the WebRTC packet-loss and UDP-jitter test (§8):
// the pion server behind POST /api/rtc/offer and a Go client for the CLI.
//
// Protocol (§8.2): the client creates a reliable, ordered "ctl" data channel
// (JSON text) and an unordered "probe" channel with maxRetransmits 0
// (binary). On ctl the client sends {"type":"start",...}, both sides send
// probes at pps for durationMs, the client sends {"type":"end","sent":N} and
// the server answers {"type":"report",...} after a 1 s straggler grace.
package rtc

import (
	"encoding/binary"
	"math"

	"github.com/CicerBro/speedtest/internal/stats"
)

// Probe kinds (byte 0 of a probe packet).
const (
	KindDown byte = 1 // server → client
	KindUp   byte = 2 // client → server
)

// Probe packet layout and limits.
const (
	ProbeHeaderSize = 13 // u8 kind, u32 seq, u64 sendUs (big-endian)

	DefaultPPS        = 100
	DefaultDurationMs = 6000
	DefaultSize       = 200

	MaxPPS        = 1000
	MaxDurationMs = 10000
	MaxSize       = 1200
	// MaxProbes bounds per-direction receiver state (MaxPPS × MaxDurationMs).
	MaxProbes = MaxPPS * MaxDurationMs / 1000
)

// Channel labels.
const (
	LabelCtl   = "ctl"
	LabelProbe = "probe"
)

// EncodeProbe writes a probe into buf (len(buf) is the packet size, at least
// ProbeHeaderSize). The padding must already be zero.
func EncodeProbe(buf []byte, kind byte, seq uint32, sendUs uint64) {
	buf[0] = kind
	binary.BigEndian.PutUint32(buf[1:5], seq)
	binary.BigEndian.PutUint64(buf[5:13], sendUs)
}

// DecodeProbe parses a probe header; ok is false for short packets.
func DecodeProbe(b []byte) (kind byte, seq uint32, sendUs uint64, ok bool) {
	if len(b) < ProbeHeaderSize {
		return 0, 0, 0, false
	}
	return b[0], binary.BigEndian.Uint32(b[1:5]), binary.BigEndian.Uint64(b[5:13]), true
}

// TotalProbes is the number of probes per direction for pps × durationMs.
func TotalProbes(pps, durationMs int) int {
	n := pps * durationMs / 1000
	if n < 1 {
		n = 1
	}
	return n
}

// validParams reports whether start parameters are within limits.
func validParams(pps, durationMs, size int) bool {
	return pps >= 1 && pps <= MaxPPS && durationMs >= 1 && durationMs <= MaxDurationMs &&
		size >= ProbeHeaderSize && size <= MaxSize
}

// receiver accumulates probes for one direction: unique arrivals (in
// arrival order, for RFC 3550 jitter), duplicates ignored, and reordering
// (a probe whose seq is lower than the highest seq seen so far).
// Not safe for concurrent use.
type receiver struct {
	seen      []bool
	maxSeq    int64
	reordered int
	arrivals  []stats.Probe
	arrSeq    []uint32
}

func newReceiver(limit int) *receiver {
	return &receiver{seen: make([]bool, limit), maxSeq: -1}
}

// add records probe seq; probes beyond the limit and duplicates are ignored.
func (r *receiver) add(seq uint32, sendUs, recvUs float64) {
	if int64(seq) >= int64(len(r.seen)) || r.seen[seq] {
		return
	}
	r.seen[seq] = true
	if int64(seq) < r.maxSeq {
		r.reordered++
	} else {
		r.maxSeq = int64(seq)
	}
	r.arrivals = append(r.arrivals, stats.Probe{SendUs: sendUs, RecvUs: recvUs})
	r.arrSeq = append(r.arrSeq, seq)
}

// summary returns the unique probes received with seq < sent and the
// RFC 3550 jitter over them (ms, arrival order).
func (r *receiver) summary(sent int) (received int, jitterMs float64) {
	probes := make([]stats.Probe, 0, len(r.arrivals))
	for i, p := range r.arrivals {
		if int(r.arrSeq[i]) < sent {
			probes = append(probes, p)
		}
	}
	return len(probes), stats.RFC3550JitterMs(probes)
}

// round2 rounds to 2 decimals (jitter values in results).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// ctlMsg is any message on the ctl channel (§8.2); unused fields are omitted.
type ctlMsg struct {
	Type string `json:"type"`

	// start
	PPS        int `json:"pps,omitempty"`
	DurationMs int `json:"durationMs,omitempty"`
	Size       int `json:"size,omitempty"`

	// end
	Sent *int `json:"sent,omitempty"`
}

// reportMsg is the server's final message.
type reportMsg struct {
	Type        string  `json:"type"` // "report"
	SentDown    int     `json:"sentDown"`
	ReceivedUp  int     `json:"receivedUp"`
	JitterUpMs  float64 `json:"jitterUpMs"`
	ReorderedUp int     `json:"reorderedUp"`
}
