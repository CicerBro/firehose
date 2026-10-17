// Packet loss + UDP jitter over a WebRTC data channel (§8). Runs on the main thread
// (RTCPeerConnection is not available in workers).
//
// CONTRACT (used by orchestrator.ts; keep stable):
//
//   runLossTest(opts: LossTestOptions): Promise<LossResult>
//
//   - opts.offerUrl   URL for the SDP exchange: `/api/rtc/offer?sid=<sid>` on the main host
//                     (POST {"sdp": offer} → {"sdp": answer}, non-trickle ICE, §5.5).
//   - opts.durationMs probe sending duration per direction (default 6000).
//   - opts.pps        probes per second per direction (default 100).
//   - opts.size       probe packet size in bytes (default 200, §8.2 layout).
//   - opts.signal     aborts the test (user Stop): closes the peer connection and resolves
//                     with `unavailableLoss()` (the orchestrator ignores the outcome after abort).
//
//   Resolves with the §7.5 `loss` object. It NEVER rejects: if WebRTC is unsupported, ICE does
//   not connect within 5 s (UDP blocked, §8.3), or anything else fails, it resolves with
//   `unavailableLoss()` (available:false).
//
// Protocol (§8.2): channel `ctl` (reliable, ordered, JSON text) and `probe` (unordered,
// maxRetransmits 0, binary). Client sends {"type":"start",pps,durationMs,size}, both sides send
// probes for durationMs, client sends {"type":"end","sent":N}, server answers
// {"type":"report",sentDown,receivedUp,jitterUpMs,reorderedUp} after a 1 s straggler grace.
// Down loss, down jitter and reordering are computed here from the probes received.
import type { LossResult } from './protocol';
import { lossPct, rfc3550JitterMs, type Probe } from './stats';

export interface LossTestOptions {
  offerUrl: string;
  durationMs: number;
  pps: number;
  size: number;
  signal: AbortSignal;
}

export const LOSS_DEFAULTS = { durationMs: 6000, pps: 100, size: 200 } as const;

export function unavailableLoss(): LossResult {
  return {
    available: false,
    downPct: 0,
    upPct: 0,
    jitterDownMs: 0,
    jitterUpMs: 0,
    sentDown: 0,
    sentUp: 0,
    reorderedDown: 0,
  };
}

// ---------- Probe packets (pure; unit-tested) ----------

/** u8 kind, u32 seq, u64 sendUs — big-endian. */
export const PROBE_HEADER_SIZE = 13;
/** server → client */
export const KIND_DOWN = 1;
/** client → server */
export const KIND_UP = 2;

/** ICE + DTLS + SCTP must be up (both channels open) within this time (§8.3). */
export const CONNECT_TIMEOUT_MS = 5000;
/** Straggler grace after the last probe (§8.2). */
export const GRACE_MS = 1000;
/** Upper bound for ICE gathering before the offer is sent anyway. */
export const GATHER_TIMEOUT_MS = 2000;
/** Extra wait for the server report beyond the grace period. */
export const REPORT_TIMEOUT_MS = 3000;
/** Pacing tick (§8.2: setInterval 10 ms). */
export const TICK_MS = 10;

export interface DecodedProbe {
  kind: number;
  seq: number;
  sendUs: number;
}

/** Number of probes per direction for pps × durationMs (same as the Go side). */
export function totalProbes(pps: number, durationMs: number): number {
  return Math.max(1, Math.floor((pps * durationMs) / 1000));
}

/** Builds a zero-padded probe packet of `size` bytes (at least PROBE_HEADER_SIZE). */
export function encodeProbe(kind: number, seq: number, sendUs: number, size: number): ArrayBuffer {
  const buf = new ArrayBuffer(Math.max(size, PROBE_HEADER_SIZE));
  const v = new DataView(buf);
  v.setUint8(0, kind);
  v.setUint32(1, seq >>> 0);
  v.setBigUint64(5, BigInt(Math.max(0, Math.round(sendUs))));
  return buf;
}

/** Parses a probe header; null if the packet is too short. */
export function decodeProbe(data: ArrayBuffer): DecodedProbe | null {
  if (data.byteLength < PROBE_HEADER_SIZE) return null;
  const v = new DataView(data);
  return { kind: v.getUint8(0), seq: v.getUint32(1), sendUs: Number(v.getBigUint64(5)) };
}

/**
 * Accumulates the probes of one direction: unique arrivals in arrival order (for RFC 3550
 * jitter), duplicates and seq ≥ limit ignored, and reordering (a probe whose seq is lower than
 * the highest seq seen so far). Mirrors `receiver` in internal/rtc.
 */
export class ProbeReceiver {
  reordered = 0;
  private readonly seen: Uint8Array;
  private maxSeq = -1;
  private readonly arrivals: Probe[] = [];
  private readonly seqs: number[] = [];

  constructor(limit: number) {
    this.seen = new Uint8Array(limit);
  }

  add(seq: number, sendUs: number, recvUs: number): void {
    if (seq >= this.seen.length || this.seen[seq]) return;
    this.seen[seq] = 1;
    if (seq < this.maxSeq) this.reordered++;
    else this.maxSeq = seq;
    this.arrivals.push({ sendUs, recvUs });
    this.seqs.push(seq);
  }

  /** Unique probes received with seq < sent, and their RFC 3550 jitter (ms). */
  summary(sent: number): { received: number; jitterMs: number } {
    const probes = this.arrivals.filter((_, i) => this.seqs[i]! < sent);
    return { received: probes.length, jitterMs: rfc3550JitterMs(probes) };
  }
}

/** Server report on `ctl`. */
export interface LossReport {
  type: 'report';
  sentDown: number;
  receivedUp: number;
  jitterUpMs: number;
  reorderedUp: number;
}

export function parseReport(text: unknown): LossReport | null {
  if (typeof text !== 'string') return null;
  try {
    const m = JSON.parse(text) as Partial<LossReport>;
    if (
      m.type === 'report' &&
      typeof m.sentDown === 'number' &&
      typeof m.receivedUp === 'number' &&
      typeof m.jitterUpMs === 'number'
    ) {
      return { type: 'report', sentDown: m.sentDown, receivedUp: m.receivedUp, jitterUpMs: m.jitterUpMs, reorderedUp: m.reorderedUp ?? 0 };
    }
  } catch {
    // not JSON
  }
  return null;
}

/** Combines the local down-direction receiver with the server report into the §7.5 object. */
export function lossResult(rx: ProbeReceiver, report: LossReport, sentUp: number): LossResult {
  const down = rx.summary(report.sentDown);
  return {
    available: true,
    downPct: lossPct(report.sentDown, down.received),
    upPct: lossPct(sentUp, report.receivedUp),
    jitterDownMs: round2(down.jitterMs),
    jitterUpMs: round2(report.jitterUpMs),
    sentDown: report.sentDown,
    sentUp,
    reorderedDown: rx.reordered,
  };
}

const round2 = (v: number): number => Math.round(v * 100) / 100;

// ---------- Test run (browser) ----------

const nowUs = (): number => performance.now() * 1000;

class Aborted extends Error {}

export async function runLossTest(opts: LossTestOptions): Promise<LossResult> {
  if (typeof RTCPeerConnection === 'undefined' || opts.signal.aborted) return unavailableLoss();

  const timers: ReturnType<typeof setTimeout>[] = [];
  let interval: ReturnType<typeof setInterval> | undefined;
  const sleep = (ms: number): Promise<void> =>
    new Promise((resolve) => {
      timers.push(setTimeout(resolve, ms));
    });

  let pc: RTCPeerConnection | undefined;
  let rejectStop: (e: Error) => void = () => {};
  // Rejects on user abort or when the connection fails/closes; raced against every wait.
  const stopped = new Promise<never>((_, reject) => {
    rejectStop = reject;
  });
  stopped.catch(() => {});
  const onAbort = (): void => rejectStop(new Aborted('aborted'));
  opts.signal.addEventListener('abort', onAbort, { once: true });
  const guard = <T>(p: Promise<T>): Promise<T> => Promise.race([p, stopped]);

  try {
    pc = new RTCPeerConnection({ iceServers: [] });
    const conn = pc;
    conn.addEventListener('connectionstatechange', () => {
      if (conn.connectionState === 'failed' || conn.connectionState === 'closed') {
        rejectStop(new Error('connection ' + conn.connectionState));
      }
    });
    const ctl = conn.createDataChannel('ctl');
    const probe = conn.createDataChannel('probe', { ordered: false, maxRetransmits: 0 });
    probe.binaryType = 'arraybuffer';

    const total = totalProbes(opts.pps, opts.durationMs);
    const rx = new ProbeReceiver(total);
    probe.onmessage = (ev: MessageEvent) => {
      const recv = nowUs();
      if (!(ev.data instanceof ArrayBuffer)) return;
      const p = decodeProbe(ev.data);
      if (p && p.kind === KIND_DOWN) rx.add(p.seq, p.sendUs, recv);
    };
    let resolveReport: (r: LossReport) => void = () => {};
    const reportP = new Promise<LossReport>((resolve) => {
      resolveReport = resolve;
    });
    ctl.onmessage = (ev: MessageEvent) => {
      const r = parseReport(ev.data);
      if (r) resolveReport(r);
    };
    const opened = (dc: RTCDataChannel): Promise<void> =>
      dc.readyState === 'open'
        ? Promise.resolve()
        : new Promise((resolve) => dc.addEventListener('open', () => resolve(), { once: true }));
    const bothOpen = Promise.all([opened(ctl), opened(probe)]).then(() => true);

    // Non-trickle: wait for gathering to complete, then send the full offer.
    await guard(conn.setLocalDescription());
    await guard(
      Promise.race([
        new Promise<void>((resolve) => {
          if (conn.iceGatheringState === 'complete') return resolve();
          conn.addEventListener('icegatheringstatechange', () => {
            if (conn.iceGatheringState === 'complete') resolve();
          });
        }),
        sleep(GATHER_TIMEOUT_MS),
      ]),
    );
    const resp = await guard(
      fetch(opts.offerUrl, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sdp: conn.localDescription?.sdp ?? '' }),
        signal: opts.signal,
      }),
    );
    if (!resp.ok) return unavailableLoss();
    const answer = (await guard(resp.json())) as { sdp?: unknown };
    if (typeof answer.sdp !== 'string') return unavailableLoss();
    await guard(conn.setRemoteDescription({ type: 'answer', sdp: answer.sdp }));

    // §8.3: UDP blocked → unavailable after 5 s.
    const ok = await guard(Promise.race([bothOpen, sleep(CONNECT_TIMEOUT_MS).then(() => false)]));
    if (!ok) return unavailableLoss();

    ctl.send(JSON.stringify({ type: 'start', pps: opts.pps, durationMs: opts.durationMs, size: opts.size }));

    // Pacing: every 10 ms send up to the cumulative target pps × elapsed (pps/100 per tick).
    let seq = 0;
    const start = performance.now();
    const sendDue = (): boolean => {
      const target = Math.min(total, Math.floor(((performance.now() - start) * opts.pps) / 1000) + 1);
      for (; seq < target; seq++) {
        try {
          probe.send(encodeProbe(KIND_UP, seq, nowUs(), opts.size));
        } catch {
          // a failed local send counts as sent: it is loss either way
        }
      }
      return seq >= total;
    };
    await guard(
      new Promise<void>((resolve) => {
        if (sendDue()) return resolve();
        interval = setInterval(() => {
          if (sendDue()) {
            clearInterval(interval);
            interval = undefined;
            resolve();
          }
        }, TICK_MS);
      }),
    );
    const sentUp = seq;
    ctl.send(JSON.stringify({ type: 'end', sent: sentUp }));

    // Wait for the report and at least the straggler grace.
    const [report] = await guard(
      Promise.all([Promise.race([reportP, sleep(GRACE_MS + REPORT_TIMEOUT_MS).then(() => null)]), sleep(GRACE_MS)]),
    );
    if (!report) return unavailableLoss();
    return lossResult(rx, report, sentUp);
  } catch {
    return unavailableLoss();
  } finally {
    opts.signal.removeEventListener('abort', onAbort);
    if (interval !== undefined) clearInterval(interval);
    for (const t of timers) clearTimeout(t);
    pc?.close();
  }
}
