// Canvas semicircle gauge (§11.2): 240° arc opening at the bottom, log scale in Mbps,
// eased in gauge-position space, rAF loop only while animating, paused when hidden.
import { formatMsValue, formatRate } from './format';
import { cssVar, mixColor, withAlpha } from './theme';

export const ARC_SWEEP = (240 * Math.PI) / 180;
/** Arc start angle (canvas angles grow clockwise from +x): lower-left, so the gap is centered at the bottom. */
export const ARC_START = Math.PI / 2 + (2 * Math.PI - ARC_SWEEP) / 2;
export const VMAX_DEFAULT = 10_000; // Mbps (10 Gbps)
export const VMAX_HIGH = 100_000; // Mbps (100 Gbps)
export const EASE_TAU_MS = 150;
const SCALE_TWEEN_MS = 600;
const COLOR_FADE_MS = 300;
/** Minimum time between frames: caps the gauge at ~60 fps on 120 Hz displays (the glow is costly to draw). */
export const MIN_FRAME_MS = 1000 / 60;

/** Gauge position 0…1 for a value in Mbps: log10(1 + v) / log10(1 + vMax). Clamped. */
export function pos(vMbps: number, vMax: number = VMAX_DEFAULT): number {
  if (!(vMbps > 0)) return 0;
  return Math.min(1, Math.log10(1 + vMbps) / Math.log10(1 + vMax));
}

export interface Tick {
  value: number; // Mbps
  label: string;
  pos: number;
}

const TICK_LABELS: [number, string][] = [
  [0, '0'],
  [1, '1'],
  [10, '10'],
  [100, '100'],
  [1_000, '1G'],
  [10_000, '10G'],
  [100_000, '100G'],
];

export function ticks(vMax: number = VMAX_DEFAULT): Tick[] {
  return TICK_LABELS.filter(([v]) => v <= vMax).map(([value, label]) => ({ value, label, pos: pos(value, vMax) }));
}

/** One easing step: pos += (target − pos) × (1 − exp(−dt / τ)) (§6.4). */
export function ease(current: number, target: number, dtMs: number, tauMs: number = EASE_TAU_MS): number {
  return current + (target - current) * (1 - Math.exp(-Math.max(0, dtMs) / tauMs));
}

/** Angle on the canvas for a gauge position. */
export const angleAt = (p: number): number => ARC_START + ARC_SWEEP * p;

export type GaugeMode = 'idle' | 'latency' | 'download' | 'upload' | 'done';

interface Elements {
  canvas: HTMLCanvasElement;
  value: HTMLElement;
  unit: HTMLElement;
  label: HTMLElement;
}

const LABELS: Record<GaugeMode, string> = {
  idle: 'Ready',
  latency: 'Latency',
  download: '↓ Download',
  upload: '↑ Upload',
  done: 'Done',
};

export class Gauge {
  private ctx: CanvasRenderingContext2D;
  private mode: GaugeMode = 'idle';
  private target = 0; // position space
  private current = 0;
  private valueMbps = 0;
  private vMax = VMAX_DEFAULT;
  private vMaxFrom = VMAX_DEFAULT;
  private vMaxTo = VMAX_DEFAULT;
  private vMaxT0 = 0;
  private faint = false;
  private color = '#22d3ee';
  private colorFrom = '#22d3ee';
  private colorTo = '#22d3ee';
  private colorT0 = -Infinity;
  private raf = 0;
  private lastFrame = 0;
  private lastText = 0;
  private pendingText: (() => void) | null = null;
  private textTimer: ReturnType<typeof setTimeout> | null = null;
  private size = { w: 0, h: 0, dpr: 1 };
  private reducedMotion: MediaQueryList;
  private palette = { track: '#1f2937', muted: '#94a3b8', text: '#e5e7eb', down: '#22d3ee', up: '#a78bfa' };

  constructor(private readonly el: Elements) {
    const ctx = el.canvas.getContext('2d');
    if (!ctx) throw new Error('canvas 2d unavailable');
    this.ctx = ctx;
    this.reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
    this.refreshColors();
    this.color = this.colorFrom = this.colorTo = this.palette.down;
    new ResizeObserver(() => this.resize()).observe(el.canvas);
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') this.kick();
    });
    this.resize();
    this.renderText();
  }

  get currentPhase(): GaugeMode {
    return this.mode;
  }

  private get reduced(): boolean {
    return this.reducedMotion.matches;
  }

  /** Re-read theme tokens (call after a theme change). */
  refreshColors(): void {
    this.palette = {
      track: cssVar('--border', '#1f2937'),
      muted: cssVar('--text-muted', '#94a3b8'),
      text: cssVar('--text', '#e5e7eb'),
      down: cssVar('--accent-down', '#22d3ee'),
      up: cssVar('--accent-up', '#a78bfa'),
    };
    const c = this.mode === 'upload' ? this.palette.up : this.palette.down;
    this.color = this.colorFrom = this.colorTo = c;
    this.kick(true);
  }

  private resize(): void {
    const r = this.el.canvas.getBoundingClientRect();
    const dpr = Math.min(window.devicePixelRatio || 1, 3);
    this.size = { w: r.width, h: r.height, dpr };
    this.el.canvas.width = Math.max(1, Math.round(r.width * dpr));
    this.el.canvas.height = Math.max(1, Math.round(r.height * dpr));
    this.kick(true);
  }

  /** Switch mode: crossfades the arc color and animates back to 0 between phases. */
  setMode(mode: GaugeMode, faintMbps?: number): void {
    const prev = this.mode;
    this.mode = mode;
    this.faint = mode === 'done';
    const next = mode === 'upload' ? this.palette.up : this.palette.down;
    if (next !== this.colorTo) {
      this.colorFrom = this.color;
      this.colorTo = next;
      this.colorT0 = performance.now();
    }
    if (mode === 'done') {
      this.valueMbps = faintMbps ?? 0;
      this.target = pos(this.valueMbps, this.vMax);
    } else if (mode !== prev) {
      this.valueMbps = 0;
      this.target = 0;
      if (mode === 'idle' || mode === 'latency') this.setScale(VMAX_DEFAULT, true);
    }
    this.queueText(true);
    this.kick();
  }

  /** Live throughput in bit/s. */
  setRate(bps: number): void {
    this.valueMbps = bps / 1e6;
    if (this.valueMbps > this.vMaxTo && this.vMaxTo < VMAX_HIGH) this.setScale(VMAX_HIGH);
    this.target = pos(this.valueMbps, this.vMax);
    this.queueText();
    this.kick();
  }

  /** Live ping in ms (latency phase readout). */
  setPing(ms: number): void {
    this.pingMs = ms;
    this.queueText();
  }
  private pingMs = NaN;

  private setScale(vMax: number, instant = false): void {
    this.vMaxFrom = this.vMax;
    this.vMaxTo = vMax;
    this.vMaxT0 = performance.now();
    if (instant || this.reduced) this.vMax = this.vMaxFrom = vMax;
  }

  // ---- readout (DOM, throttled: ≤ 10 Hz, 4 Hz with reduced motion) ----

  private queueText(now = false): void {
    this.pendingText = () => this.renderText();
    const minGap = this.reduced ? 250 : 100;
    const wait = now ? 0 : Math.max(0, this.lastText + minGap - performance.now());
    if (wait === 0) {
      if (this.textTimer) clearTimeout(this.textTimer);
      this.textTimer = null;
      this.flushText();
    } else if (!this.textTimer) {
      this.textTimer = setTimeout(() => {
        this.textTimer = null;
        this.flushText();
      }, wait);
    }
  }

  private flushText(): void {
    const f = this.pendingText;
    this.pendingText = null;
    this.lastText = performance.now();
    f?.();
  }

  private renderText(): void {
    const { value, unit, label } = this.el;
    label.textContent = LABELS[this.mode];
    if (this.mode === 'latency') {
      value.textContent = Number.isFinite(this.pingMs) ? formatMsValue(this.pingMs) : '—';
      unit.textContent = 'ms';
    } else if (this.mode === 'idle') {
      value.textContent = '—';
      unit.textContent = 'Mbps';
    } else {
      const r = formatRate(this.valueMbps * 1e6);
      value.textContent = r.value;
      unit.textContent = r.unit;
    }
    this.el.value.parentElement?.classList.toggle('faint', this.faint);
  }

  // ---- animation ----

  private kick(redraw = false): void {
    if (redraw && !this.raf) this.draw(performance.now());
    if (this.raf || document.visibilityState === 'hidden') return;
    this.lastFrame = performance.now();
    this.raf = requestAnimationFrame(this.frame);
  }

  private frame = (now: number): void => {
    // rAF timestamps jitter by a ms or two: the slack keeps a 60 Hz display at 60 fps.
    if (now - this.lastFrame < MIN_FRAME_MS - 2) {
      this.raf = requestAnimationFrame(this.frame);
      return;
    }
    this.raf = 0;
    const dt = Math.min(100, now - this.lastFrame);
    this.lastFrame = now;
    let animating = false;

    // scale tween (600 ms)
    if (this.vMax !== this.vMaxTo) {
      const k = Math.min(1, (now - this.vMaxT0) / SCALE_TWEEN_MS);
      this.vMax = this.vMaxFrom + (this.vMaxTo - this.vMaxFrom) * k;
      if (k >= 1) this.vMax = this.vMaxTo;
      this.target = pos(this.valueMbps, this.vMax);
      animating = true;
    }
    // color crossfade (300 ms)
    const ck = Math.min(1, (now - this.colorT0) / COLOR_FADE_MS);
    this.color = ck >= 1 || this.reduced ? this.colorTo : mixColor(this.colorFrom, this.colorTo, ck);
    if (ck < 1 && !this.reduced) animating = true;

    if (this.reduced) this.current = this.target;
    else {
      this.current = ease(this.current, this.target, dt);
      if (Math.abs(this.target - this.current) < 1e-4) this.current = this.target;
      else animating = true;
    }
    if (this.mode === 'latency' && !this.reduced) animating = true; // pulse

    this.draw(now);
    if (animating && document.visibilityState !== 'hidden') this.raf = requestAnimationFrame(this.frame);
  };

  private draw(now: number): void {
    const { ctx } = this;
    const { w, h, dpr } = this.size;
    if (w === 0 || h === 0) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    // Geometry: the 240° arc spans 1 + sin(30°) = 1.5 radii vertically.
    const lw = Math.max(10, Math.min(w, h) * 0.055);
    const m = lw * 1.3; // room for the glow (shadowBlur) around the arc
    const r = Math.max(10, Math.min((w - 2 * m - lw) / 2, (h - m - lw) / 1.5));
    const cx = w / 2;
    const cy = m + lw / 2 + r;
    ctx.lineCap = 'round';

    // track
    ctx.beginPath();
    ctx.arc(cx, cy, r, ARC_START, ARC_START + ARC_SWEEP);
    ctx.strokeStyle = this.palette.track;
    ctx.lineWidth = Math.max(4, lw * 0.4);
    ctx.stroke();

    // ticks
    ctx.fillStyle = this.palette.muted;
    ctx.font = `${Math.max(10, Math.round(r * 0.075))}px system-ui, -apple-system, "Segoe UI", sans-serif`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    for (const t of ticks(this.vMax)) {
      if (t.pos > 1.0001) continue;
      const a = angleAt(t.pos);
      const tr = r - lw * 1.6;
      ctx.fillText(t.label, cx + Math.cos(a) * tr, cy + Math.sin(a) * tr);
    }

    const alpha = this.faint ? 0.35 : 1;
    if (this.mode === 'latency') {
      // indeterminate gentle pulse
      const k = this.reduced ? 0.5 : 0.5 + 0.5 * Math.sin(now / 450);
      ctx.beginPath();
      ctx.arc(cx, cy, r, ARC_START, ARC_START + ARC_SWEEP);
      ctx.strokeStyle = withAlpha(this.color, 0.12 + 0.18 * k);
      ctx.lineWidth = lw;
      ctx.stroke();
      return;
    }
    if (this.current <= 0.0005) return;

    const end = angleAt(this.current);
    ctx.save();
    ctx.globalAlpha = alpha;
    let stroke: string | CanvasGradient = this.color;
    if (typeof ctx.createConicGradient === 'function') {
      const g = ctx.createConicGradient(ARC_START, cx, cy);
      const span = ARC_SWEEP / (2 * Math.PI);
      g.addColorStop(0, withAlpha(this.color, 0.35));
      g.addColorStop(Math.min(1, span * this.current), this.color);
      g.addColorStop(Math.min(1, span * this.current + 0.001), withAlpha(this.color, 0.35));
      stroke = g;
    }
    ctx.beginPath();
    ctx.arc(cx, cy, r, ARC_START, end);
    ctx.strokeStyle = stroke;
    ctx.lineWidth = lw;
    ctx.shadowColor = withAlpha(this.color, 0.6);
    ctx.shadowBlur = lw * 1.2;
    ctx.stroke();

    // bright leading dot
    ctx.beginPath();
    ctx.arc(cx + Math.cos(end) * r, cy + Math.sin(end) * r, lw * 0.42, 0, 2 * Math.PI);
    ctx.fillStyle = mixColor(this.color, '#ffffff', 0.7);
    ctx.shadowBlur = lw;
    ctx.fill();
    ctx.restore();
  }
}
