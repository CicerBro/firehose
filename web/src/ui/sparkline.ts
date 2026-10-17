// Canvas area chart (§11.3): last `spanMs` of the phase, y auto-scaled to the max seen (linear),
// gradient fill at 25 % opacity plus a 1.5 px line. No axes or labels. Redraws coalesced per frame.
import { withAlpha } from './theme';

export interface Point {
  t: number;
  v: number;
}

export class Sparkline {
  private points: Point[] = [];
  private max = 0;
  private color = '#22d3ee';
  private spanMs = 20_000;
  private raf = 0;
  private ctx: CanvasRenderingContext2D | null;

  constructor(private readonly canvas: HTMLCanvasElement) {
    this.ctx = canvas.getContext('2d');
    new ResizeObserver(() => this.schedule()).observe(canvas);
  }

  reset(color: string, spanMs: number): void {
    this.points = [];
    this.max = 0;
    this.color = color;
    this.spanMs = Math.min(20_000, Math.max(1000, spanMs));
    this.schedule();
  }

  setColor(color: string): void {
    this.color = color;
    this.schedule();
  }

  push(t: number, v: number): void {
    this.points.push({ t, v });
    if (v > this.max) this.max = v;
    this.schedule();
  }

  setData(points: Point[], color: string, spanMs: number): void {
    this.reset(color, spanMs);
    for (const p of points) this.push(p.t, p.v);
  }

  clear(): void {
    this.points = [];
    this.max = 0;
    this.schedule();
  }

  private schedule(): void {
    if (this.raf) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      this.draw();
    });
  }

  private draw(): void {
    const ctx = this.ctx;
    if (!ctx) return;
    const r = this.canvas.getBoundingClientRect();
    const dpr = Math.min(window.devicePixelRatio || 1, 3);
    const w = r.width;
    const h = r.height;
    const pw = Math.max(1, Math.round(w * dpr));
    const ph = Math.max(1, Math.round(h * dpr));
    if (this.canvas.width !== pw) this.canvas.width = pw;
    if (this.canvas.height !== ph) this.canvas.height = ph;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    const pts = this.points;
    if (pts.length < 2 || this.max <= 0 || w === 0) return;
    const tEnd = Math.max(this.spanMs, pts[pts.length - 1]!.t);
    const t0 = tEnd - this.spanMs;
    const pad = 2;
    const x = (t: number): number => ((t - t0) / this.spanMs) * w;
    const y = (v: number): number => h - pad - (v / this.max) * (h - pad * 2);

    ctx.beginPath();
    let first = true;
    let firstX = 0;
    let lastX = 0;
    for (const p of pts) {
      if (p.t < t0) continue;
      const px = x(p.t);
      if (first) {
        ctx.moveTo(px, y(p.v));
        firstX = px;
        first = false;
      } else ctx.lineTo(px, y(p.v));
      lastX = px;
    }
    ctx.lineWidth = 1.5;
    ctx.lineJoin = 'round';
    ctx.strokeStyle = this.color;
    ctx.stroke();
    ctx.lineTo(lastX, h);
    ctx.lineTo(firstX, h);
    ctx.closePath();
    const g = ctx.createLinearGradient(0, 0, 0, h);
    g.addColorStop(0, withAlpha(this.color, 0.25));
    g.addColorStop(1, withAlpha(this.color, 0));
    ctx.fillStyle = g;
    ctx.fill();
  }
}
