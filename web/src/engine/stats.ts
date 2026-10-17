// Measurement methodology (BUILD-PLAN §6). Mirror of internal/stats (Go);
// both are verified against testdata/stats-vectors.json, so keep them in
// lockstep. Pure functions, no dependencies.
//
// Timelines are arrays of Sample ordered by t (ms since phase start) with
// cumulative byte counts. Rates are bits per second (SI).

/** Width of the rolling window used by rollingP90. */
export const ROLLING_WINDOW_MS = 1000;
/** Spacing between rolling-window samples in rollingP90. */
export const ROLLING_STEP_MS = 100;
/** Shortest steady window; shorter ones fall back to [0, end]. */
export const MIN_STEADY_MS = 1000;

const EPS = 1e-9;

export interface Sample {
  t: number;
  bytes: number;
}

export interface Throughput {
  bps: number;
  /** Bytes inside the window used. */
  bytes: number;
  /** Length of the window used. */
  steadyMs: number;
  /** Window fell back to [0, end]. */
  short: boolean;
}

export interface Probe {
  sendUs: number;
  recvUs: number;
}

function sorted(xs: readonly number[]): number[] {
  return [...xs].sort((a, b) => a - b);
}

/** Middle element (mean of the two middles for even n); 0 if empty. Does not mutate. */
export function median(xs: readonly number[]): number {
  const n = xs.length;
  if (n === 0) return 0;
  const s = sorted(xs);
  const hi = s[n >> 1]!;
  return n % 2 === 1 ? hi : (s[(n >> 1) - 1]! + hi) / 2;
}

/** Nearest-rank p90: the ceil(0.9n)-th smallest; 0 if empty. Does not mutate. */
export function p90(xs: readonly number[]): number {
  const n = xs.length;
  if (n === 0) return 0;
  // ceil(9n/10) in integer arithmetic avoids 0.9*n rounding noise.
  const rank = Math.floor((9 * n + 9) / 10);
  return sorted(xs)[rank - 1]!;
}

/** Smallest element; 0 if empty. */
export function min(xs: readonly number[]): number {
  if (xs.length === 0) return 0;
  let m = xs[0]!;
  for (const x of xs) if (x < m) m = x;
  return m;
}

/** Mean |rtt[i] - rtt[i-1]|; 0 for fewer than 2 samples. */
export function jitter(rtts: readonly number[]): number {
  if (rtts.length < 2) return 0;
  let sum = 0;
  for (let i = 1; i < rtts.length; i++) sum += Math.abs(rtts[i]! - rtts[i - 1]!);
  return sum / (rtts.length - 1);
}

/**
 * Cumulative bytes at time t, linearly interpolated and clamped to the
 * first/last sample outside the timeline. 0 for an empty timeline.
 */
export function interpolate(tl: readonly Sample[], t: number): number {
  const n = tl.length;
  if (n === 0) return 0;
  const first = tl[0]!;
  const last = tl[n - 1]!;
  if (t <= first.t) return first.bytes;
  if (t >= last.t) return last.bytes;
  // First index with tl[i].t >= t; 1 <= i <= n-1 here.
  let lo = 0;
  let hi = n;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (tl[mid]!.t >= t) hi = mid;
    else lo = mid + 1;
  }
  const a = tl[lo - 1]!;
  const b = tl[lo]!;
  if (b.t === t || b.t <= a.t) return b.bytes;
  return a.bytes + ((b.bytes - a.bytes) * (t - a.t)) / (b.t - a.t);
}

function rate(tl: readonly Sample[], a: number, b: number): number {
  return ((interpolate(tl, b) - interpolate(tl, a)) * 8) / ((b - a) / 1000);
}

/** Gauge rate: (bytes(now) - bytes(now-window)) * 8 / (window/1000); 0 if windowMs <= 0. */
export function liveRate(tl: readonly Sample[], now: number, windowMs: number): number {
  if (windowMs <= 0) return 0;
  return rate(tl, now - windowMs, now);
}

function steadyStart(graceMs: number, endMs: number): { start: number; short: boolean } {
  return endMs - graceMs < MIN_STEADY_MS ? { start: 0, short: true } : { start: graceMs, short: false };
}

/**
 * Final throughput over the steady window [grace, end]. If that is shorter
 * than 1 s, [0, end] is used and short is set. bps is 0 when the window used
 * has non-positive length.
 */
export function steady(tl: readonly Sample[], graceMs: number, endMs: number): Throughput {
  const { start, short } = steadyStart(graceMs, endMs);
  const d = endMs - start;
  const bytes = interpolate(tl, endMs) - interpolate(tl, start);
  return { bps: d > 0 ? (bytes * 8) / (d / 1000) : 0, bytes, steadyMs: d, short };
}

/**
 * Peak rate: within the steady window (as chosen by steady(), including the
 * short fallback) evaluate 1 s rolling-window rates ending at start+1000,
 * start+1100, ... up to end, and return their nearest-rank p90 (bit/s).
 *
 * If the window used is shorter than 1 s (only possible in the short case
 * with end < 1000 ms) no full rolling window fits; the result is then the
 * average rate over that window, i.e. steady(...).bps.
 */
export function rollingP90(tl: readonly Sample[], graceMs: number, endMs: number): number {
  const { start } = steadyStart(graceMs, endMs);
  if (endMs - start < ROLLING_WINDOW_MS - EPS) return steady(tl, graceMs, endMs).bps;
  const rates: number[] = [];
  for (let k = 0; ; k++) {
    const s = start + ROLLING_WINDOW_MS + ROLLING_STEP_MS * k;
    if (s > endMs + EPS) break;
    rates.push(rate(tl, s - ROLLING_WINDOW_MS, s));
  }
  return p90(rates);
}

/**
 * RFC 3550 interarrival jitter over probes in arrival order:
 * D = (Rj-Ri) - (Sj-Si), J += (|D|-J)/16. Result in ms; 0 for < 2 probes.
 */
export function rfc3550JitterMs(probes: readonly Probe[]): number {
  let j = 0;
  for (let i = 1; i < probes.length; i++) {
    const p = probes[i]!;
    const q = probes[i - 1]!;
    const d = p.recvUs - q.recvUs - (p.sendUs - q.sendUs);
    j += (Math.abs(d) - j) / 16;
  }
  return j / 1000;
}

/** 100*(1-received/sent), rounded to 2 decimals, clamped to [0,100]; 0 if sent <= 0. */
export function lossPct(sent: number, received: number): number {
  if (sent <= 0) return 0;
  const v = Math.max(0, Math.min(100, (100 * (sent - received)) / sent));
  // v >= 0, so Math.round (half-up) matches Go's math.Round (half away from zero).
  return Math.round(v * 100) / 100;
}
