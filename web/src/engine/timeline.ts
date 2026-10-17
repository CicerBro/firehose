// Cumulative byte timeline of one throughput phase (§6.2) → live and final rates (§6.3).
// The math lives in stats.ts (shared vectors with Go); this is the stateful recorder around it.
import { interpolate, liveRate, rollingP90, steady, type Sample, type Throughput } from './stats';

export const LIVE_WINDOW_MS = 500;

export class Timeline {
  readonly samples: Sample[] = [{ t: 0, bytes: 0 }];

  /** Append a sample. Out-of-order or non-monotonic samples are clamped so the timeline stays monotonic. */
  add(t: number, bytes: number): void {
    const last = this.samples[this.samples.length - 1]!;
    if (t <= last.t) {
      // Same timestamp: keep the newest byte count (never decreasing).
      if (t === last.t) last.bytes = Math.max(last.bytes, bytes);
      return;
    }
    this.samples.push({ t, bytes: Math.max(bytes, last.bytes) });
  }

  get lastT(): number {
    return this.samples[this.samples.length - 1]!.t;
  }

  get lastBytes(): number {
    return this.samples[this.samples.length - 1]!.bytes;
  }

  bytesAt(t: number): number {
    return interpolate(this.samples, t);
  }

  /** Live rate in bit/s over the last 500 ms, ending at the newest sample (or at `now`). */
  live(now: number = this.lastT, windowMs: number = LIVE_WINDOW_MS): number {
    return liveRate(this.samples, now, windowMs);
  }

  /** Final throughput over the steady window [grace, end] plus the rolling p90 peak. */
  final(graceMs: number, endMs: number = this.lastT): { tp: Throughput; p90bps: number } {
    const tp = steady(this.samples, graceMs, endMs);
    const p90bps = rollingP90(this.samples, graceMs, endMs);
    return { tp, p90bps };
  }
}
