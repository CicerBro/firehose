/// <reference lib="webworker" />
// Download / upload streams (§7.3, §7.4). Every request stays a CORS "simple request" (§5.5):
// download = plain GET without custom headers, upload = POST of a Blob with an empty type.
// The sid travels in the query string; credentials are included so the Authelia cookie reaches s1…sN.
import type { TransferCommand, TransferEvent } from './protocol';
import { MiB, uploadSizes } from './plan';

declare const self: DedicatedWorkerGlobalScope;

const FLUSH_MS = 50;
const MAX_CONSECUTIVE_ERRORS = 3;

let controller: AbortController | null = null;
let pending = 0;
let flushTimer: ReturnType<typeof setInterval> | null = null;
let running: Promise<unknown> | null = null;

const post = (m: TransferEvent): void => self.postMessage(m);

function flush(): void {
  if (pending > 0) {
    post({ type: 'progress', bytes: pending });
    pending = 0;
  }
}

const isAbort = (e: unknown): boolean => e instanceof DOMException && e.name === 'AbortError';
const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

class StreamError extends Error {
  constructor(
    message: string,
    readonly auth: boolean,
    readonly fatal: boolean,
  ) {
    super(message);
  }
}

function httpError(status: number): StreamError {
  if (status === 401) return new StreamError('Not authenticated (401)', true, true);
  if (status === 410) return new StreamError('Session expired (410)', false, true);
  if (status === 413) return new StreamError('Request too large (413)', false, true);
  // 429 (stream cap) and 5xx: transient, retried.
  return new StreamError(`HTTP ${status}`, false, false);
}

/**
 * Run `once` in a loop until aborted. Errors: a network error before this stream ever received data
 * is treated as an expired login (§7.1); otherwise transient errors are retried a few times.
 */
async function loop(signal: AbortSignal, once: () => Promise<number>): Promise<void> {
  let total = 0;
  let consecutive = 0;
  while (!signal.aborted) {
    try {
      total += await once();
      consecutive = 0;
    } catch (e) {
      if (signal.aborted || isAbort(e)) return;
      const err =
        e instanceof StreamError
          ? e
          : new StreamError(e instanceof Error ? e.message : String(e), e instanceof TypeError && total === 0, total === 0);
      if (err.fatal || ++consecutive >= MAX_CONSECUTIVE_ERRORS) {
        post({ type: 'error', message: err.message, auth: err.auth });
        return;
      }
      await sleep(100 * consecutive);
    }
  }
}

// ---------- download (§7.3) ----------

function downloadStream(base: string, sid: string, bytes: number, signal: AbortSignal): Promise<void> {
  const url = `${base}/api/down?sid=${encodeURIComponent(sid)}&bytes=${bytes}`;
  return loop(signal, async () => {
    const res = await fetch(url, { cache: 'no-store', credentials: 'include', signal });
    if (!res.ok || !res.body) throw httpError(res.status);
    const reader = res.body.getReader();
    let got = 0;
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        got += value.byteLength;
        pending += value.byteLength;
      }
    } catch (e) {
      // Mid-stream failure after data: not an auth problem, just retry.
      if (got > 0 && !isAbort(e) && !signal.aborted) throw new StreamError(String(e), false, false);
      throw e;
    }
    return got;
  });
}

// ---------- upload (§7.4) ----------

let chunk: Uint8Array<ArrayBuffer> | null = null;
const blobs = new Map<number, Blob>();

function randomChunk(): Uint8Array<ArrayBuffer> {
  if (!chunk) {
    chunk = new Uint8Array(MiB);
    // getRandomValues is limited to 65 536 bytes per call.
    for (let off = 0; off < MiB; off += 65536) crypto.getRandomValues(chunk.subarray(off, off + 65536));
  }
  return chunk;
}

function blobOf(size: number): Blob {
  let b = blobs.get(size);
  if (!b) {
    const c = randomChunk();
    const parts: BlobPart[] = [];
    let left = size;
    while (left >= MiB) {
      parts.push(c);
      left -= MiB;
    }
    if (left > 0) parts.push(c.subarray(0, left));
    // No `type`: keeps the POST a CORS simple request (no Content-Type header, no preflight).
    b = new Blob(parts);
    blobs.set(size, b);
  }
  return b;
}

function uploadStreams(urls: string[], sid: string, start: number, max: number, signal: AbortSignal): Promise<void>[] {
  const sizes = uploadSizes(start, max);
  const levels = urls.map(() => 0);
  // Free Blobs no stream uses anymore (all streams moved past them): caps memory per worker.
  const prune = (): void => {
    const minLevel = Math.min(...levels);
    for (let i = 0; i < minLevel; i++) blobs.delete(sizes[i]!);
  };
  return urls.map((base, i) => {
    const url = `${base}/api/up?sid=${encodeURIComponent(sid)}`;
    return loop(signal, async () => {
      const size = sizes[levels[i]!]!;
      const t0 = performance.now();
      const res = await fetch(url, { method: 'POST', body: blobOf(size), cache: 'no-store', credentials: 'include', signal });
      if (!res.ok) throw httpError(res.status);
      await res.arrayBuffer().catch(() => undefined);
      pending += size; // completed request bytes: only used as a fallback when WS stats are missing
      if (performance.now() - t0 < 1000 && levels[i]! < sizes.length - 1) {
        levels[i]!++;
        prune();
      }
      return size;
    });
  });
}

// ---------- control ----------

async function stop(): Promise<void> {
  controller?.abort();
  controller = null;
  await running;
  running = null;
  if (flushTimer) clearInterval(flushTimer);
  flushTimer = null;
  flush();
  post({ type: 'stopped' });
}

self.onmessage = (ev: MessageEvent<TransferCommand>): void => {
  const m = ev.data;
  if (m.type === 'stop') {
    void stop();
    return;
  }
  controller?.abort();
  const ctl = new AbortController();
  controller = ctl;
  pending = 0;
  if (flushTimer) clearInterval(flushTimer);
  flushTimer = setInterval(flush, FLUSH_MS);
  const urls = m.urls.slice(0, m.streams);
  const tasks =
    m.dir === 'down'
      ? urls.map((u) => downloadStream(u, m.sid, m.downBytes, ctl.signal))
      : uploadStreams(urls, m.sid, m.upStartSize, m.upMaxSize, ctl.signal);
  running = Promise.allSettled(tasks);
};
