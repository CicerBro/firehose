// Builds the §7.5 results object (shared schema with `firehose client --json`) and its text summary.
import { formatMs, formatPct, rateText, round } from '../ui/format';
import type { ConfigResponse, IdleLatency, IpResponse, LoadedLatency, LossResult, Result, ThroughputResult, UploadResult } from './protocol';
import { jitter, median, min, p90, type Throughput } from './stats';

/** Idle latency ignores the first 3 RTTs (warm-up, §6.3). */
export const IDLE_WARMUP = 3;

export interface PhaseThroughput {
  tp: Throughput;
  p90bps: number;
}

export interface ResultInput {
  timestamp: Date;
  config: ConfigResponse;
  serverUrl: string;
  ip: IpResponse | null;
  userAgent: string;
  connections: number;
  durationSec: number;
  graceSec: number;
  lossEnabled: boolean;
  /** All idle RTTs in arrival order (warm-up included; dropped here). */
  idleRtts: number[];
  /** Loaded RTTs after the grace period. */
  downRtts: number[];
  upRtts: number[];
  download: PhaseThroughput | null;
  upload: (PhaseThroughput & { approx: boolean }) | null;
  loss: LossResult;
  tabHidden: boolean;
  aborted: boolean;
}

const ms = (v: number): number => round(v, 2);

export function idleLatency(rtts: number[]): IdleLatency {
  const xs = rtts.length > IDLE_WARMUP ? rtts.slice(IDLE_WARMUP) : rtts.slice();
  if (xs.length === 0) return { medianMs: 0, minMs: 0, jitterMs: 0, samples: 0 };
  return { medianMs: ms(median(xs)), minMs: ms(min(xs)), jitterMs: ms(xs.length > 1 ? jitter(xs) : 0), samples: xs.length };
}

export function loadedLatency(rtts: number[]): LoadedLatency {
  if (rtts.length === 0) return { medianMs: 0, p90Ms: 0, samples: 0 };
  return { medianMs: ms(median(rtts)), p90Ms: ms(p90(rtts)), samples: rtts.length };
}

function throughput(p: PhaseThroughput | null): ThroughputResult {
  if (!p) return { bps: 0, p90bps: 0, bytes: 0, steadyMs: 0, short: true };
  return {
    bps: Math.round(p.tp.bps),
    p90bps: Math.round(p.p90bps),
    bytes: Math.round(p.tp.bytes),
    steadyMs: Math.round(p.tp.steadyMs),
    short: p.tp.short,
  };
}

export function buildResult(i: ResultInput): Result {
  const up: UploadResult = { ...throughput(i.upload), approx: i.upload?.approx ?? false };
  const loss: LossResult = {
    ...i.loss,
    downPct: round(i.loss.downPct, 2),
    upPct: round(i.loss.upPct, 2),
    jitterDownMs: ms(i.loss.jitterDownMs),
    jitterUpMs: ms(i.loss.jitterUpMs),
  };
  return {
    version: 1,
    timestamp: i.timestamp.toISOString().replace(/\.\d{3}Z$/, 'Z'),
    server: { name: i.config.serverName, url: i.serverUrl, version: i.config.version },
    client: { ip: i.ip?.ip ?? '', family: i.ip?.family ?? '', userAgent: i.userAgent },
    config: { connections: i.connections, durationSec: i.durationSec, graceSec: i.graceSec, loss: i.lossEnabled },
    latency: { idle: idleLatency(i.idleRtts), download: loadedLatency(i.downRtts), upload: loadedLatency(i.upRtts) },
    download: throughput(i.download),
    upload: up,
    loss,
    flags: { tabHidden: i.tabHidden, singleOrigin: i.config.singleOrigin, aborted: i.aborted },
  };
}

/** Plain-text summary (the "Copy results → Text" format, mirrors the CLI's human output). */
export function resultText(r: Result): string {
  const fam = r.client.family === 'ipv6' ? 'IPv6' : r.client.family === 'ipv4' ? 'IPv4' : '?';
  const lines = [
    `Firehose ${r.timestamp}`,
    `Server      ${r.server.name} (${r.server.url}) · v${r.server.version}`,
    `Client      ${r.client.ip || 'unknown'} (${fam})`,
    `Mode        ${r.config.connections} connection${r.config.connections === 1 ? '' : 's'} · ${r.config.durationSec} s per direction`,
    '',
    `Latency     ${formatMs(r.latency.idle.medianMs)}   (min ${formatMs(r.latency.idle.minMs)}, jitter ${formatMs(r.latency.idle.jitterMs)}, ${r.latency.idle.samples} samples)`,
    `Download    ${rateText(r.download.bps)}   (p90 ${rateText(r.download.p90bps)}, loaded latency ${formatMs(r.latency.download.medianMs)})`,
    `Upload      ${rateText(r.upload.bps)}${r.upload.approx ? ' (approx.)' : ''}   (p90 ${rateText(r.upload.p90bps)}, loaded latency ${formatMs(r.latency.upload.medianMs)})`,
    r.loss.available
      ? `Packet loss ↓ ${formatPct(r.loss.downPct)}  ↑ ${formatPct(r.loss.upPct)}   (UDP jitter ↓ ${formatMs(r.loss.jitterDownMs)} ↑ ${formatMs(r.loss.jitterUpMs)})`
      : `Packet loss ${r.config.loss ? 'unavailable (UDP blocked?)' : 'not tested'}`,
  ];
  const notes: string[] = [];
  if (r.flags.tabHidden) notes.push('Tab was in background; results may be inaccurate.');
  if (r.flags.singleOrigin) notes.push('Single-origin mode (max 6 connections).');
  if (r.download.short || r.upload.short) notes.push('Short test: steady window under 1 s.');
  if (notes.length) lines.push('', ...notes);
  return lines.join('\n');
}
