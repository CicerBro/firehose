// Shareable results image: the Download / Upload tiles (plus ping, jitter, loss) drawn on a
// canvas in the current theme, exported as PNG. Pure canvas, no dependencies.
import type { Result } from '../engine/protocol';
import { formatMsValue, formatPct, formatRate, rateText } from './format';
import type { Point } from './sparkline';
import { bloatGrade } from './tiles';
import { cssVar, withAlpha } from './theme';

export interface ShareSeries {
  down: Point[];
  up: Point[];
}

const W = 1200;
const H = 630;
const PAD = 48;
const GAP = 24;
const FONT = 'system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif';

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number): void {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
}

function spark(ctx: CanvasRenderingContext2D, pts: Point[], color: string, x: number, y: number, w: number, h: number): void {
  if (pts.length < 2) return;
  const t0 = pts[0]!.t;
  const span = Math.max(1, pts[pts.length - 1]!.t - t0);
  const max = Math.max(1, ...pts.map((p) => p.v));
  const px = (p: Point): number => x + ((p.t - t0) / span) * w;
  const py = (p: Point): number => y + h - (p.v / max) * h * 0.92;
  ctx.beginPath();
  ctx.moveTo(px(pts[0]!), y + h);
  for (const p of pts) ctx.lineTo(px(p), py(p));
  ctx.lineTo(px(pts[pts.length - 1]!), y + h);
  ctx.closePath();
  const g = ctx.createLinearGradient(0, y, 0, y + h);
  g.addColorStop(0, withAlpha(color, 0.35));
  g.addColorStop(1, withAlpha(color, 0.02));
  ctx.fillStyle = g;
  ctx.fill();
  ctx.beginPath();
  pts.forEach((p, i) => (i ? ctx.lineTo(px(p), py(p)) : ctx.moveTo(px(p), py(p))));
  ctx.strokeStyle = color;
  ctx.lineWidth = 2.5;
  ctx.lineJoin = 'round';
  ctx.stroke();
}

interface Palette {
  bg: string;
  surface: string;
  border: string;
  text: string;
  muted: string;
  down: string;
  up: string;
  ok: string;
  warn: string;
  bad: string;
}

function palette(): Palette {
  return {
    bg: cssVar('--bg', '#0b0f17'),
    surface: cssVar('--surface', '#121826'),
    border: cssVar('--border', '#1f2937'),
    text: cssVar('--text', '#e5e7eb'),
    muted: cssVar('--text-muted', '#94a3b8'),
    down: cssVar('--accent-down', '#22d3ee'),
    up: cssVar('--accent-up', '#a78bfa'),
    ok: cssVar('--ok', '#34d399'),
    warn: cssVar('--warn', '#fbbf24'),
    bad: cssVar('--bad', '#f87171'),
  };
}

function rateCard(
  ctx: CanvasRenderingContext2D,
  c: Palette,
  x: number,
  y: number,
  w: number,
  h: number,
  label: string,
  color: string,
  bps: number,
  p90bps: number,
  loaded: { medianMs: number; samples: number },
  idleMs: number | null,
  series: Point[],
): void {
  roundRect(ctx, x, y, w, h, 20);
  ctx.fillStyle = c.surface;
  ctx.fill();
  ctx.strokeStyle = c.border;
  ctx.lineWidth = 2;
  ctx.stroke();

  const ix = x + 32;
  ctx.textBaseline = 'alphabetic';
  ctx.fillStyle = color;
  ctx.font = `600 22px ${FONT}`;
  ctx.fillText(label, ix, y + 52);

  const r = formatRate(bps);
  ctx.fillStyle = c.text;
  ctx.font = `700 88px ${FONT}`;
  ctx.fillText(r.value, ix, y + 148);
  const vw = ctx.measureText(r.value).width;
  ctx.fillStyle = c.muted;
  ctx.font = `500 32px ${FONT}`;
  ctx.fillText(r.unit, ix + vw + 14, y + 148);

  const parts: string[] = [];
  if (loaded.samples > 0) parts.push(`loaded latency ${formatMsValue(loaded.medianMs)} ms`);
  parts.push(`peak ${rateText(p90bps)}`);
  ctx.font = `400 21px ${FONT}`;
  ctx.fillText(parts.join(' · '), ix, y + 190);

  if (loaded.samples > 0 && idleMs !== null) {
    const b = bloatGrade(loaded.medianMs, idleMs);
    const pc = b.grade === 'ok' ? c.ok : b.grade === 'warn' ? c.warn : c.bad;
    ctx.font = `600 18px ${FONT}`;
    const pw = ctx.measureText(b.label).width + 28;
    const px = x + w - 32 - pw;
    roundRect(ctx, px, y + 30, pw, 32, 16);
    ctx.fillStyle = withAlpha(pc, 0.16);
    ctx.fill();
    ctx.fillStyle = pc;
    ctx.fillText(b.label, px + 14, y + 52);
  }

  spark(ctx, series, color, x + 2, y + h - 120, w - 4, 100);
}

function statCard(ctx: CanvasRenderingContext2D, c: Palette, x: number, y: number, w: number, h: number, label: string, value: string): void {
  roundRect(ctx, x, y, w, h, 20);
  ctx.fillStyle = c.surface;
  ctx.fill();
  ctx.strokeStyle = c.border;
  ctx.lineWidth = 2;
  ctx.stroke();
  ctx.fillStyle = c.muted;
  ctx.font = `600 18px ${FONT}`;
  ctx.fillText(label, x + 28, y + 40);
  ctx.fillStyle = c.text;
  ctx.font = `700 34px ${FONT}`;
  ctx.fillText(value, x + 28, y + 86);
}

/** Draws the results card. Returns the canvas (W×H CSS px at `scale`). */
export function drawShareImage(r: Result, series: ShareSeries, scale = 2): HTMLCanvasElement {
  const canvas = document.createElement('canvas');
  canvas.width = W * scale;
  canvas.height = H * scale;
  const ctx = canvas.getContext('2d')!;
  ctx.scale(scale, scale);
  const c = palette();

  ctx.fillStyle = c.bg;
  ctx.fillRect(0, 0, W, H);

  // Header: brand left, server · date right.
  ctx.textBaseline = 'alphabetic';
  ctx.fillStyle = c.text;
  ctx.font = `700 30px ${FONT}`;
  ctx.fillText('◉ Firehose', PAD, PAD + 22);
  const when = new Date(r.timestamp);
  const meta = [r.server.name, Number.isNaN(when.getTime()) ? '' : when.toLocaleString(), `${r.config.connections} ${r.config.connections === 1 ? 'connection' : 'connections'}`]
    .filter(Boolean)
    .join(' · ');
  ctx.fillStyle = c.muted;
  ctx.font = `400 20px ${FONT}`;
  ctx.fillText(meta, W - PAD - ctx.measureText(meta).width, PAD + 20);

  const idle = r.latency.idle.samples > 0 ? r.latency.idle.medianMs : null;
  const top = PAD + 56;
  const cardW = (W - 2 * PAD - GAP) / 2;
  const cardH = 330;
  rateCard(ctx, c, PAD, top, cardW, cardH, '↓ DOWNLOAD', c.down, r.download.bps, r.download.p90bps, r.latency.download, idle, series.down);
  rateCard(ctx, c, PAD + cardW + GAP, top, cardW, cardH, '↑ UPLOAD', c.up, r.upload.bps, r.upload.p90bps, r.latency.upload, idle, series.up);

  const sy = top + cardH + GAP;
  const sh = H - PAD - sy;
  const sw = (W - 2 * PAD - 2 * GAP) / 3;
  const dash = '—';
  statCard(ctx, c, PAD, sy, sw, sh, 'PING', idle !== null ? `${formatMsValue(idle)} ms` : dash);
  statCard(ctx, c, PAD + sw + GAP, sy, sw, sh, 'JITTER', r.latency.idle.samples > 1 ? `${formatMsValue(r.latency.idle.jitterMs)} ms` : dash);
  statCard(
    ctx,
    c,
    PAD + 2 * (sw + GAP),
    sy,
    sw,
    sh,
    'PACKET LOSS',
    r.loss.available ? `↓ ${formatPct(r.loss.downPct)}  ↑ ${formatPct(r.loss.upPct)}` : r.config.loss ? 'Unavailable' : dash,
  );
  return canvas;
}

export function toPngBlob(canvas: HTMLCanvasElement): Promise<Blob> {
  return new Promise((resolve, reject) =>
    canvas.toBlob((b) => (b ? resolve(b) : reject(new Error('PNG encoding failed'))), 'image/png'),
  );
}

/**
 * Copies the PNG to the clipboard where image clipboard writes are supported. Otherwise it
 * offers the system share sheet (Web Share with a file, mostly mobile), and as a last resort
 * downloads the PNG. Returns which one happened.
 */
export async function shareImage(r: Result, series: ShareSeries): Promise<'copied' | 'shared' | 'downloaded'> {
  const canvas = drawShareImage(r, series);
  // Safari requires the ClipboardItem to be created synchronously within the user gesture,
  // so hand it a promise of the blob rather than awaiting first.
  const blobP = toPngBlob(canvas);
  if (typeof ClipboardItem !== 'undefined' && navigator.clipboard?.write) {
    try {
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': blobP })]);
      return 'copied';
    } catch {
      // fall through
    }
  }
  const blob = await blobP;
  const name = `firehose-${r.timestamp.replace(/[:.]/g, '-')}.png`;
  const file = new File([blob], name, { type: 'image/png' });
  if (navigator.canShare?.({ files: [file] })) {
    try {
      await navigator.share({ files: [file], title: 'Firehose results' });
      return 'shared';
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return 'shared';
    }
  }
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
  return 'downloaded';
}
