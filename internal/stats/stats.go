// Package stats implements the measurement methodology of BUILD-PLAN §6.
//
// It is mirrored one-to-one by web/src/engine/stats.ts; both are verified
// against testdata/stats-vectors.json, so any change here must be made there
// too (and covered by a vector).
//
// Timelines are slices of Sample ordered by T (ms since phase start) with
// cumulative byte counts. Rates are bits per second (SI).
package stats

import (
	"math"
	"sort"
)

// RollingWindowMs is the width of the rolling window used by RollingP90.
const RollingWindowMs = 1000

// RollingStepMs is the spacing between rolling-window samples in RollingP90.
const RollingStepMs = 100

// MinSteadyMs is the shortest steady window; shorter ones fall back to [0, end].
const MinSteadyMs = 1000

// eps absorbs floating-point noise when stepping through the rolling window.
const eps = 1e-9

// Sample is one timeline point: T ms since phase start, Bytes cumulative.
type Sample struct {
	T     float64 `json:"t"`
	Bytes float64 `json:"bytes"`
}

// Median returns the middle element (mean of the two middles for an even
// count). 0 for empty input. The input is not modified.
func Median(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	s := sorted(xs)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// P90 returns the nearest-rank 90th percentile: the ceil(0.9*n)-th smallest
// element. 0 for empty input. The input is not modified.
func P90(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	// ceil(9n/10) in integer arithmetic avoids 0.9*n rounding noise.
	rank := (9*n + 9) / 10
	return sorted(xs)[rank-1]
}

// Min returns the smallest element, 0 for empty input.
func Min(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

// Jitter returns the mean absolute difference between consecutive RTTs.
// 0 for fewer than 2 samples.
func Jitter(rtts []float64) float64 {
	if len(rtts) < 2 {
		return 0
	}
	sum := 0.0
	for i := 1; i < len(rtts); i++ {
		sum += math.Abs(rtts[i] - rtts[i-1])
	}
	return sum / float64(len(rtts)-1)
}

// Interpolate returns the cumulative bytes at time t, linearly interpolated
// between samples and clamped to the first/last sample outside the timeline.
// 0 for an empty timeline.
func Interpolate(tl []Sample, t float64) float64 {
	n := len(tl)
	if n == 0 {
		return 0
	}
	if t <= tl[0].T {
		return tl[0].Bytes
	}
	if t >= tl[n-1].T {
		return tl[n-1].Bytes
	}
	// First index with T >= t; 1 <= i <= n-1 here.
	i := sort.Search(n, func(k int) bool { return tl[k].T >= t })
	a, b := tl[i-1], tl[i]
	if b.T == t || b.T <= a.T {
		return b.Bytes
	}
	return a.Bytes + (b.Bytes-a.Bytes)*(t-a.T)/(b.T-a.T)
}

// LiveRate is the gauge rate over the trailing window:
// (bytes(now) - bytes(now-window)) * 8 / (window/1000). 0 if windowMs <= 0.
func LiveRate(tl []Sample, now, windowMs float64) float64 {
	if windowMs <= 0 {
		return 0
	}
	return rate(tl, now-windowMs, now)
}

// Throughput is a final per-direction result (§6.3, §7.5).
type Throughput struct {
	Bps      float64 `json:"bps"`
	Bytes    float64 `json:"bytes"`    // bytes inside the window used
	SteadyMs float64 `json:"steadyMs"` // length of the window used
	Short    bool    `json:"short"`    // window fell back to [0, end]
}

// steadyWindow returns the window [start, end] used for final results:
// [grace, end], or [0, end] with short=true if that is shorter than 1 s.
func steadyWindow(graceMs, endMs float64) (start float64, short bool) {
	if endMs-graceMs < MinSteadyMs {
		return 0, true
	}
	return graceMs, false
}

// Steady computes the final throughput over the steady window [grace, end].
// If that window is shorter than 1 s, [0, end] is used and Short is set.
// Bps is 0 when the window used has non-positive length.
func Steady(tl []Sample, graceMs, endMs float64) Throughput {
	start, short := steadyWindow(graceMs, endMs)
	d := endMs - start
	bytes := Interpolate(tl, endMs) - Interpolate(tl, start)
	out := Throughput{Bytes: bytes, SteadyMs: d, Short: short}
	if d > 0 {
		out.Bps = bytes * 8 / (d / 1000)
	}
	return out
}

// RollingP90 computes the peak rate: within the steady window (as chosen by
// Steady, including the short fallback) it evaluates 1 s rolling-window rates
// whose window ends at start+1000, start+1100, ... up to end, and returns
// their nearest-rank p90 in bit/s.
//
// If the window used is shorter than 1 s (only possible in the short case
// with end < 1000 ms) no full rolling window fits; the result is then the
// average rate over that window, i.e. Steady(...).Bps.
func RollingP90(tl []Sample, graceMs, endMs float64) float64 {
	start, _ := steadyWindow(graceMs, endMs)
	if endMs-start < RollingWindowMs-eps {
		return Steady(tl, graceMs, endMs).Bps
	}
	var rates []float64
	for k := 0; ; k++ {
		s := start + RollingWindowMs + RollingStepMs*float64(k)
		if s > endMs+eps {
			break
		}
		rates = append(rates, rate(tl, s-RollingWindowMs, s))
	}
	return P90(rates)
}

// rate is the average bit rate between times a and b (b > a).
func rate(tl []Sample, a, b float64) float64 {
	return (Interpolate(tl, b) - Interpolate(tl, a)) * 8 / ((b - a) / 1000)
}

// Probe is one received WebRTC probe: sender and receiver clocks in µs.
type Probe struct {
	SendUs float64 `json:"sendUs"`
	RecvUs float64 `json:"recvUs"`
}

// RFC3550JitterMs computes RFC 3550 interarrival jitter over probes in
// arrival order: D = (Rj-Ri) - (Sj-Si), J += (|D|-J)/16. Result in ms.
// 0 for fewer than 2 probes.
func RFC3550JitterMs(probes []Probe) float64 {
	j := 0.0
	for i := 1; i < len(probes); i++ {
		d := (probes[i].RecvUs - probes[i-1].RecvUs) - (probes[i].SendUs - probes[i-1].SendUs)
		j += (math.Abs(d) - j) / 16
	}
	return j / 1000
}

// LossPct returns 100*(1-received/sent) rounded to 2 decimals and clamped to
// [0, 100]. 0 if sent <= 0.
func LossPct(sent, received int) float64 {
	if sent <= 0 {
		return 0
	}
	v := 100 * float64(sent-received) / float64(sent)
	v = math.Max(0, math.Min(100, v))
	return math.Round(v*100) / 100
}

func sorted(xs []float64) []float64 {
	s := make([]float64, len(xs))
	copy(s, xs)
	sort.Float64s(s)
	return s
}
