// Result tiles (§11.4).
import type { LossResult } from '../engine/protocol';
import { formatMsValue, formatPct, formatRate, rateText } from './format';
import { Sparkline, type Point } from './sparkline';

export type Bloat = 'ok' | 'warn' | 'bad';

/** Bufferbloat hint from loaded − idle latency: < 30 ms good, < 100 ms fair, else poor (§11.4). */
export function bloatGrade(loadedMs: number, idleMs: number): { grade: Bloat; label: string } {
  const d = loadedMs - idleMs;
  if (d < 30) return { grade: 'ok', label: 'Good' };
  if (d < 100) return { grade: 'warn', label: 'Fair' };
  return { grade: 'bad', label: 'Poor' };
}

class Tile {
  readonly el: HTMLElement;
  private v: HTMLElement;
  private u: HTMLElement;
  private box: HTMLElement;
  constructor(id: string) {
    this.el = document.getElementById(id)!;
    this.box = this.el.querySelector('.tile-value')!;
    this.v = this.el.querySelector('.v')!;
    this.u = this.el.querySelector('.u')!;
  }
  set(value: string, unit: string, title?: string): void {
    this.v.textContent = value;
    this.u.textContent = unit;
    this.box.classList.remove('pending');
    if (title) this.el.title = title;
    else this.el.removeAttribute('title');
  }
  pending(): void {
    this.v.textContent = '—';
    this.u.textContent = '';
    this.box.classList.add('pending');
    this.el.removeAttribute('title');
  }
}

class RateTile extends Tile {
  readonly spark: Sparkline;
  private sub: HTMLElement;
  private bloat: HTMLElement;
  constructor(id: string) {
    super(id);
    this.spark = new Sparkline(this.el.querySelector('canvas')!);
    this.sub = this.el.querySelector('.sub')!;
    this.bloat = this.el.querySelector('.bloat')!;
  }
  override pending(): void {
    super.pending();
    this.sub.textContent = '';
    this.bloat.hidden = true;
    this.spark.clear();
  }
  result(
    bps: number,
    p90bps: number,
    loadedMs: number,
    loadedSamples: number,
    idleMs: number | null,
    approx: boolean,
    series: Point[],
    color: string,
    spanMs: number,
  ): void {
    const r = formatRate(bps);
    this.set(r.value, r.unit + (approx ? ' ≈' : ''), approx ? 'Approximate: server upload stats were unavailable' : undefined);
    const parts: string[] = [];
    if (loadedSamples > 0) parts.push(`loaded latency ${formatMsValue(loadedMs)} ms`);
    parts.push(`peak ${rateText(p90bps)}`);
    this.sub.textContent = parts.join(' · ');
    if (loadedSamples > 0 && idleMs !== null) {
      const b = bloatGrade(loadedMs, idleMs);
      this.bloat.textContent = b.label;
      this.bloat.className = `bloat ${b.grade}`;
      this.bloat.title = `Latency under load is ${formatMsValue(Math.max(0, loadedMs - idleMs))} ms above idle (bufferbloat)`;
      this.bloat.hidden = false;
    } else this.bloat.hidden = true;
    this.spark.setData(series, color, spanMs);
  }
}

export class Tiles {
  readonly down = new RateTile('tile-down');
  readonly up = new RateTile('tile-up');
  readonly ping = new Tile('tile-ping');
  readonly jitter = new Tile('tile-jitter');
  readonly loss = new Tile('tile-loss');

  reset(): void {
    for (const t of [this.down, this.up, this.ping, this.jitter, this.loss]) t.pending();
  }

  idle(medianMs: number, jitterMs: number, samples: number): void {
    if (samples === 0) {
      this.ping.set('—', '', 'No latency samples');
      this.jitter.set('—', '');
      return;
    }
    this.ping.set(formatMsValue(medianMs), 'ms');
    this.jitter.set(formatMsValue(jitterMs), 'ms');
  }

  lossResult(l: LossResult, enabled: boolean): void {
    if (!enabled) this.loss.set('Off', '', 'Packet-loss test disabled');
    else if (!l.available) this.loss.set('Unavailable', '', 'UDP appears to be blocked (WebRTC could not connect), so packet loss could not be measured');
    else this.loss.set(`↓ ${formatPct(l.downPct)} ↑ ${formatPct(l.upPct)}`, '', `UDP jitter ↓ ${formatMsValue(l.jitterDownMs)} ms ↑ ${formatMsValue(l.jitterUpMs)} ms`);
  }
}
