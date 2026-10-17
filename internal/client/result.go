package client

import (
	"math"
)

// ResultVersion is the schema version of Result (§7.5).
const ResultVersion = 1

// Result is the result object shared with the browser's "Copy JSON"
// (BUILD-PLAN §7.5). Field names and nesting are normative.
//
// All objects are always present so the schema is stable for scripts:
// phases that did not run report zeros (samples 0, bps 0) and loss reports
// available=false.
type Result struct {
	Version   int        `json:"version"`
	Timestamp string     `json:"timestamp"` // RFC 3339, UTC, start of the run
	Server    ServerInfo `json:"server"`
	Client    ClientInfo `json:"client"`
	Config    RunConfig  `json:"config"`
	Latency   Latency    `json:"latency"`
	Download  Throughput `json:"download"`
	Upload    Upload     `json:"upload"`
	Loss      Loss       `json:"loss"`
	Flags     Flags      `json:"flags"`

	// Phases records which phases ran (not part of the JSON schema; used by
	// the human and CSV renderers to leave out skipped phases).
	Phases Phases `json:"-"`
	// Stats are client-side counters (not part of the JSON schema).
	Stats RunStats `json:"-"`
}

// ServerInfo describes the server (from /api/config).
type ServerInfo struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Version string `json:"version"`
}

// ClientInfo describes the client as the server sees it (from /api/ip).
type ClientInfo struct {
	IP        string `json:"ip"`
	Family    string `json:"family"` // "ipv4" or "ipv6"
	UserAgent string `json:"userAgent"`
}

// RunConfig is the test configuration that was used.
type RunConfig struct {
	Connections int     `json:"connections"`
	DurationSec float64 `json:"durationSec"`
	GraceSec    float64 `json:"graceSec"`
	Loss        bool    `json:"loss"`
}

// Latency holds idle and loaded WebSocket latency.
type Latency struct {
	Idle     IdleLatency   `json:"idle"`
	Download LoadedLatency `json:"download"`
	Upload   LoadedLatency `json:"upload"`
}

// IdleLatency is phase-1 latency (first 3 pings excluded as warm-up).
type IdleLatency struct {
	MedianMs float64 `json:"medianMs"`
	MinMs    float64 `json:"minMs"`
	JitterMs float64 `json:"jitterMs"`
	Samples  int     `json:"samples"`
}

// LoadedLatency is latency during a transfer phase, after the grace period.
type LoadedLatency struct {
	MedianMs float64 `json:"medianMs"`
	P90Ms    float64 `json:"p90Ms"`
	Samples  int     `json:"samples"`
}

// Throughput is a download result.
type Throughput struct {
	Bps      float64 `json:"bps"`
	P90Bps   float64 `json:"p90bps"`
	Bytes    float64 `json:"bytes"`
	SteadyMs float64 `json:"steadyMs"`
	Short    bool    `json:"short"`
}

// Upload is an upload result. Approx is set when server stats were
// unavailable and the client-side byte count was used instead.
type Upload struct {
	Bps      float64 `json:"bps"`
	P90Bps   float64 `json:"p90bps"`
	Bytes    float64 `json:"bytes"`
	SteadyMs float64 `json:"steadyMs"`
	Short    bool    `json:"short"`
	Approx   bool    `json:"approx"`
}

// Loss is the WebRTC packet loss / UDP jitter result (§8).
type Loss struct {
	Available     bool    `json:"available"`
	DownPct       float64 `json:"downPct"`
	UpPct         float64 `json:"upPct"`
	JitterDownMs  float64 `json:"jitterDownMs"`
	JitterUpMs    float64 `json:"jitterUpMs"`
	SentDown      int     `json:"sentDown"`
	SentUp        int     `json:"sentUp"`
	ReorderedDown int     `json:"reorderedDown"`
}

// Flags are result qualifiers.
type Flags struct {
	TabHidden    bool `json:"tabHidden"` // always false for the CLI
	SingleOrigin bool `json:"singleOrigin"`
	Aborted      bool `json:"aborted"`
}

// Phases selects (in Options) or records (in Result) the test phases.
type Phases struct {
	Latency  bool
	Download bool
	Upload   bool
	Loss     bool
}

// AllPhases enables every phase.
func AllPhases() Phases { return Phases{Latency: true, Download: true, Upload: true, Loss: true} }

// round rounds x to d decimals.
func round(x float64, d int) float64 {
	p := math.Pow10(d)
	return math.Round(x*p) / p
}
