package client

import (
	"context"
	"net/http"
	"time"
)

// Packet loss test (§8) — integration seam.
//
// The WebRTC client lives in internal/rtc (M6). This package only defines
// the seam it plugs into, so internal/client does not depend on pion:
//
//   - LossFunc is the signature of a loss test implementation.
//   - Options.Loss sets it per run (cmd/firehose wires the rtc
//     implementation there); when nil, the package-level runLoss is used.
//   - runLoss is nil until the rtc client is wired in (either set it here,
//     e.g. `var runLoss LossFunc = rtc.RunClientLoss` if rtc does not import
//     this package, or pass Options.Loss from cmd/firehose).
//
// Semantics expected from Run:
//   - With no implementation, the loss phase is skipped as if --no-loss was
//     given (§10.3: "until M6 --no-loss is implied"): config.loss=false and
//     loss.available=false.
//   - If the server reports rtc.enabled=false, the implementation is not
//     called and loss.available=false (not an error, exit 0).
//   - The implementation runs in parallel with the idle-latency phase, which
//     then lasts LossParams.Duration (6 s) instead of 2 s (§6.1).
//   - An error from the implementation (e.g. ICE did not connect within 5 s
//     because UDP is blocked) is not a test failure: Run records
//     loss.available=false, reports the error through Options.Warn and
//     exits 0 (§8.3, §10.5).
//   - --no-loss, or --only without "loss", never calls it.

// LossParams is everything a loss implementation needs.
type LossParams struct {
	Server     string       // server origin (https://speed.example.com), no trailing slash
	SID        string       // session ID; POST {Server}/api/rtc/offer?sid=SID
	Header     http.Header  // Authorization (bearer) and User-Agent, for /api/rtc/offer
	HTTPClient *http.Client // honors -4/-6, --insecure, --ca-file; never follows redirects
	IPFamily   int          // 0 = any, 4 = udp4 only, 6 = udp6 only
	Duration   time.Duration
	PPS        int // probes per second per direction (§8.2 default 100)
	Size       int // probe size in bytes (§8.2 default 200)
}

// LossFunc runs the WebRTC loss/jitter test and returns its result with
// Available=true, or an error if the test could not run.
type LossFunc func(ctx context.Context, p LossParams) (Loss, error)

// Loss test defaults (§8.2).
const (
	LossDuration = 6 * time.Second
	LossPPS      = 100
	LossSize     = 200
)

// runLoss is the default loss implementation, nil until M6 wiring.
var runLoss LossFunc

// lossFunc returns the implementation to use for o, or nil.
func lossFunc(o *Options) LossFunc {
	if o.Loss != nil {
		return o.Loss
	}
	return runLoss
}
