// Phase state machine (§7.1):
//   idle → starting → latency → download → upload → done
//               ↘ error (any phase) → idle
//   any running state —[Stop]→ aborting → idle (partial results discarded)
import { createSession, SignedOutError, wsUrl } from './api';
import { LOSS_DEFAULTS, runLossTest, unavailableLoss } from './loss';
import { DOWN_BYTES, planStreams, UP_MAX_SIZE, UP_START_SIZE, workerCount } from './plan';
import type {
  ConfigResponse,
  Direction,
  IdleLatency,
  IpResponse,
  LatencyEvent,
  LossResult,
  Result,
  SessionResponse,
  TransferCommand,
  TransferEvent,
} from './protocol';
import { buildResult, idleLatency, loadedLatency, type PhaseThroughput } from './results';
import { median } from './stats';
import { Timeline } from './timeline';

export type State = 'idle' | 'starting' | 'latency' | 'download' | 'upload' | 'done' | 'error' | 'aborting';
export type TransferPhase = 'download' | 'upload';

export interface RunOptions {
  connections: number;
  durationSec: number;
  graceSec: number;
  loss: boolean;
}

export type EngineEvent =
  | { type: 'state'; state: State }
  | { type: 'session'; session: SessionResponse }
  /** Live idle ping median (latency phase). */
  | { type: 'ping'; ms: number }
  /** Live throughput sample (every ~100 ms). t = ms since phase start. */
  | { type: 'rate'; phase: TransferPhase; t: number; bps: number }
  | { type: 'idle-result'; idle: IdleLatency }
  | { type: 'loss-result'; loss: LossResult }
  | {
      type: 'phase-result';
      phase: TransferPhase;
      bps: number;
      p90bps: number;
      loadedMs: number;
      loadedSamples: number;
      approx: boolean;
      /** Live rate series of the finished phase (for the tile sparkline). */
      series: { t: number; bps: number }[];
    }
  | { type: 'done'; result: Result }
  | { type: 'error'; message: string; signedOut: boolean };

export const IDLE_PING_MS = 100;
export const LOADED_PING_MS = 250;
export const SAMPLE_MS = 100;
export const latencyPhaseMs = (loss: boolean): number => (loss ? 6000 : 2000);

class AbortedError extends Error {
  constructor() {
    super('aborted');
    this.name = 'AbortedError';
  }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) return reject(new AbortedError());
    const id = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = (): void => {
      clearTimeout(id);
      reject(new AbortedError());
    };
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

function withTimeout<T>(p: Promise<T>, ms: number, fallback: T): Promise<T> {
  return Promise.race([p, new Promise<T>((r) => setTimeout(() => r(fallback), ms))]);
}

interface Deferred {
  promise: Promise<never>;
  reject: (e: Error) => void;
}
function failure(): Deferred {
  let reject!: (e: Error) => void;
  const promise = new Promise<never>((_, rej) => (reject = rej));
  promise.catch(() => undefined); // handled via race
  return { promise, reject };
}

export class Orchestrator {
  state: State = 'idle';
  private ctl: AbortController | null = null;
  private latencyWorker: Worker | null = null;
  private transferWorkers: Worker[] = [];
  private fail: Deferred = failure();

  // per-run measurements
  private phase: State = 'idle';
  private phaseStart = 0;
  private graceMs = 0;
  private idleRtts: number[] = [];
  private downRtts: number[] = [];
  private upRtts: number[] = [];
  private tabHidden = false;
  private lastStats: { t: number; up: number } | null = null;
  private onStats: ((t: number, up: number) => void) | null = null;

  constructor(
    private readonly config: ConfigResponse,
    private readonly ip: () => IpResponse | null,
    private readonly emit: (e: EngineEvent) => void,
  ) {}

  get running(): boolean {
    return this.state === 'starting' || this.state === 'latency' || this.state === 'download' || this.state === 'upload';
  }

  private setState(s: State): void {
    this.state = s;
    this.phase = s;
    this.emit({ type: 'state', state: s });
  }

  /** User Stop: abort everything, discard partial results, return to idle. */
  stop(): void {
    if (!this.running) return;
    this.setState('aborting');
    this.ctl?.abort();
    this.teardown();
    this.setState('idle');
  }

  private teardown(): void {
    for (const w of this.transferWorkers) {
      w.postMessage({ type: 'stop' } satisfies TransferCommand);
      w.terminate(); // kills in-flight fetches: all streams stop immediately
    }
    this.transferWorkers = [];
    if (this.latencyWorker) {
      this.latencyWorker.postMessage({ type: 'close' });
      // Give the worker a moment to send the WS close frame (server marks the session finishing).
      const w = this.latencyWorker;
      setTimeout(() => w.terminate(), 200);
    }
    this.latencyWorker = null;
    this.onStats = null;
    document.removeEventListener('visibilitychange', this.onVisibility);
  }

  private onVisibility = (): void => {
    if (document.visibilityState === 'hidden' && this.running) this.tabHidden = true;
  };

  async start(opts: RunOptions): Promise<void> {
    if (this.running) return;
    const ctl = new AbortController();
    this.ctl = ctl;
    this.fail = failure();
    this.idleRtts = [];
    this.downRtts = [];
    this.upRtts = [];
    this.lastStats = null;
    this.tabHidden = document.visibilityState === 'hidden';
    document.addEventListener('visibilitychange', this.onVisibility);
    this.setState('starting');
    try {
      const result = await this.run(opts, ctl.signal);
      if (ctl.signal.aborted) return;
      this.teardown();
      this.setState('done');
      this.emit({ type: 'done', result });
    } catch (e) {
      if (ctl.signal.aborted || e instanceof AbortedError) return; // stop() already reset to idle
      this.teardown();
      ctl.abort();
      const signedOut = e instanceof SignedOutError;
      this.setState('error');
      this.emit({ type: 'error', message: e instanceof Error ? e.message : String(e), signedOut });
      this.setState('idle');
    }
  }

  private async run(opts: RunOptions, signal: AbortSignal): Promise<Result> {
    const session = await createSession(opts.connections, signal);
    if (signal.aborted) throw new AbortedError();
    this.emit({ type: 'session', session });

    await this.openLatency(session.sid, signal);

    // ---- Phase 1: latency (+ loss in parallel) ----
    this.setState('latency');
    this.phaseStart = performance.now();
    const lossOn = opts.loss && this.config.rtc.enabled;
    const lossP: Promise<LossResult> = lossOn
      ? runLossTest({ offerUrl: `/api/rtc/offer?sid=${encodeURIComponent(session.sid)}`, ...LOSS_DEFAULTS, signal }).catch(() =>
          unavailableLoss(),
        )
      : Promise.resolve(unavailableLoss());
    this.setPing(IDLE_PING_MS);
    await Promise.race([sleep(latencyPhaseMs(lossOn), signal), this.fail.promise]);
    const loss = await Promise.race([withTimeout(lossP, LOSS_DEFAULTS.durationMs + 8000, unavailableLoss()), this.fail.promise]);
    if (signal.aborted) throw new AbortedError();
    this.emit({ type: 'idle-result', idle: idleLatency(this.idleRtts) });
    this.emit({ type: 'loss-result', loss });

    // ---- Phases 2 + 3: download, upload ----
    this.graceMs = opts.graceSec * 1000;
    const durationMs = opts.durationSec * 1000;
    this.setPing(LOADED_PING_MS);
    const download = await this.transfer('down', session, durationMs, signal);
    const upload = await this.transfer('up', session, durationMs, signal);
    this.setPing(0);

    return buildResult({
      timestamp: new Date(),
      config: this.config,
      serverUrl: location.origin,
      ip: this.ip(),
      userAgent: navigator.userAgent,
      connections: session.connections,
      durationSec: opts.durationSec,
      graceSec: opts.graceSec,
      lossEnabled: lossOn,
      idleRtts: this.idleRtts,
      downRtts: this.downRtts,
      upRtts: this.upRtts,
      download,
      upload,
      loss,
      tabHidden: this.tabHidden,
      aborted: false,
    });
  }

  private setPing(ms: number): void {
    this.latencyWorker?.postMessage({ type: 'interval', ms });
  }

  private openLatency(sid: string, signal: AbortSignal): Promise<void> {
    const w = new Worker(new URL('./latency.worker.ts', import.meta.url), { type: 'module', name: 'latency' });
    this.latencyWorker = w;
    let opened = false;
    return new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('Could not open the control connection (WebSocket timeout)')), 5000);
      signal.addEventListener('abort', () => reject(new AbortedError()), { once: true });
      w.onmessage = (ev: MessageEvent<LatencyEvent>) => {
        const m = ev.data;
        switch (m.type) {
          case 'ws-open':
            opened = true;
            clearTimeout(timer);
            resolve();
            break;
          case 'rtt':
            this.onRtt(m.ms);
            break;
          case 'stats':
            this.lastStats = { t: m.t, up: m.up };
            this.onStats?.(m.t, m.up);
            break;
          case 'ws-error':
            if (!opened) {
              clearTimeout(timer);
              reject(new Error('Could not open the control connection (WebSocket)'));
            } else if (m.code === 'session_expired') {
              this.fail.reject(new Error('Test session expired'));
            }
            // Other WS losses mid-test: keep going; upload falls back to approximate counting.
            break;
        }
      };
      w.onerror = () => reject(new Error('Latency worker failed to start'));
      w.postMessage({ type: 'open', wsUrl: wsUrl(sid) });
    });
  }

  private onRtt(ms: number): void {
    const t = performance.now() - this.phaseStart;
    if (this.phase === 'latency') {
      this.idleRtts.push(ms);
      const xs = this.idleRtts.length > 3 ? this.idleRtts.slice(3) : this.idleRtts;
      this.emit({ type: 'ping', ms: median(xs) });
    } else if (this.phase === 'download' && t >= this.graceMs) this.downRtts.push(ms);
    else if (this.phase === 'upload' && t >= this.graceMs) this.upRtts.push(ms);
  }

  private spawnTransferWorkers(n: number): Worker[] {
    while (this.transferWorkers.length < n) {
      const w = new Worker(new URL('./transfer.worker.ts', import.meta.url), { type: 'module', name: 'transfer' });
      this.transferWorkers.push(w);
    }
    return this.transferWorkers.slice(0, n);
  }

  private async transfer(
    dir: Direction,
    session: SessionResponse,
    durationMs: number,
    signal: AbortSignal,
  ): Promise<PhaseThroughput & { approx: boolean }> {
    const phase: TransferPhase = dir === 'down' ? 'download' : 'upload';
    const n = session.connections;
    const W = workerCount(n, navigator.hardwareConcurrency);
    const plan = planStreams(n, session.testHosts, W);
    const workers = this.spawnTransferWorkers(W);
    const limits = this.config.limits;

    const tl = new Timeline(); // download: client bytes; upload: server-counted bytes (server time)
    const approxTl = new Timeline(); // upload fallback: completed requests (client time)
    const series: { t: number; bps: number }[] = [];
    let clientBytes = 0;
    let streamErrors = 0;
    let lastError = '';
    let active = true;
    let statsSeen = false;

    const publish = (t: number, bps: number): void => {
      series.push({ t, bps });
      this.emit({ type: 'rate', phase, t, bps });
    };

    const stopped: Promise<void>[] = workers.map(
      (w) =>
        new Promise<void>((resolve) => {
          w.onmessage = (ev: MessageEvent<TransferEvent>) => {
            const m = ev.data;
            if (m.type === 'stopped') return resolve();
            if (!active) return;
            if (m.type === 'progress') clientBytes += m.bytes;
            else if (m.type === 'error') {
              if (m.auth) this.fail.reject(new SignedOutError());
              lastError = m.message;
              if (++streamErrors >= n) this.fail.reject(new Error(`All ${phase} streams failed: ${lastError}`));
            }
          };
        }),
    );

    this.setState(phase);
    this.phaseStart = performance.now();
    const start = this.phaseStart;

    if (dir === 'up') {
      const base = this.lastStats?.up ?? 0;
      let t0: number | null = null; // server ns corresponding to the client phase start
      this.onStats = (t, up) => {
        const recv = performance.now() - start;
        if (!active || recv > durationMs) return; // client receipt time decides the window
        if (t0 === null) t0 = t - recv * 1e6;
        statsSeen = true;
        tl.add((t - t0) / 1e6, up - base);
        publish(tl.lastT, tl.live());
      };
    }

    const sampler = setInterval(() => {
      if (!active) return;
      const t = performance.now() - start;
      if (dir === 'down') {
        tl.add(t, clientBytes);
        publish(t, tl.live());
      } else {
        approxTl.add(t, clientBytes);
        if (!statsSeen) publish(t, approxTl.live(t, 1000));
      }
    }, SAMPLE_MS);

    workers.forEach((w, i) =>
      w.postMessage({
        type: 'start',
        dir,
        sid: session.sid,
        urls: plan[i]!,
        streams: plan[i]!.length,
        downBytes: Math.min(DOWN_BYTES, limits.maxDownloadBytes),
        upStartSize: Math.min(UP_START_SIZE, limits.maxUploadBytes),
        upMaxSize: Math.min(UP_MAX_SIZE, limits.maxUploadBytes),
      } satisfies TransferCommand),
    );

    try {
      await Promise.race([sleep(durationMs, signal), this.fail.promise]);
    } finally {
      clearInterval(sampler);
      const end = performance.now() - start;
      if (dir === 'down') tl.add(end, clientBytes);
      else approxTl.add(end, clientBytes);
      active = false;
      this.onStats = null;
      for (const w of workers) w.postMessage({ type: 'stop' } satisfies TransferCommand);
    }
    // Let the streams wind down before the next phase so they don't compete for bandwidth.
    await withTimeout(Promise.all(stopped), 2000, []);
    if (signal.aborted) throw new AbortedError();

    const approx = dir === 'up' && !statsSeen;
    const src = approx ? approxTl : tl;
    const fin = src.final(this.graceMs);
    const loaded = loadedLatency(dir === 'down' ? this.downRtts : this.upRtts);
    this.emit({
      type: 'phase-result',
      phase,
      bps: fin.tp.bps,
      p90bps: fin.p90bps,
      loadedMs: loaded.medianMs,
      loadedSamples: loaded.samples,
      approx,
      series,
    });
    return { ...fin, approx };
  }
}

/** Debug overrides `?duration=<sec>&grace=<sec>`, honored only when the server runs with --dev (§13.5). */
export function debugOverrides(config: ConfigResponse, search: string): { durationSec?: number; graceSec?: number } {
  if (!config.dev) return {};
  const q = new URLSearchParams(search);
  const num = (k: string): number | undefined => {
    const v = q.get(k);
    if (v === null) return undefined;
    const n = Number(v);
    return Number.isFinite(n) && n >= 0 && n <= 120 ? n : undefined;
  };
  const out: { durationSec?: number; graceSec?: number } = {};
  const d = num('duration');
  const g = num('grace');
  if (d !== undefined && d > 0) out.durationSec = d;
  if (g !== undefined) out.graceSec = g;
  return out;
}
