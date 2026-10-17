/// <reference lib="webworker" />
// Latency worker (§7.2): owns the WebSocket, sends pings at the interval set by the main thread,
// measures RTT with the worker-local clock, and relays server upload/download stats.
import type { LatencyCommand, LatencyEvent, WsPing, WsServerMessage } from './protocol';

declare const self: DedicatedWorkerGlobalScope;

let ws: WebSocket | null = null;
let timer: ReturnType<typeof setInterval> | null = null;
let intervalMs = 0;
let nextId = 1;
const inflight = new Map<number, number>();

const post = (m: LatencyEvent): void => self.postMessage(m);

function ping(): void {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  const id = nextId++;
  inflight.set(id, performance.now());
  // Drop pings that never got an answer so the map can't grow without bound.
  if (inflight.size > 64) {
    const oldest = inflight.keys().next().value;
    if (oldest !== undefined) inflight.delete(oldest);
  }
  const msg: WsPing = { type: 'ping', id };
  ws.send(JSON.stringify(msg));
}

function schedule(): void {
  if (timer) clearInterval(timer);
  timer = null;
  if (intervalMs > 0 && ws?.readyState === WebSocket.OPEN) {
    ping();
    timer = setInterval(ping, intervalMs);
  }
}

function close(): void {
  if (timer) clearInterval(timer);
  timer = null;
  inflight.clear();
  if (ws) {
    ws.onclose = ws.onerror = ws.onmessage = null;
    ws.close();
  }
  ws = null;
}

function open(url: string): void {
  close();
  const sock = new WebSocket(url);
  ws = sock;
  sock.onopen = () => {
    post({ type: 'ws-open' });
    schedule();
  };
  sock.onmessage = (ev: MessageEvent) => {
    const now = performance.now();
    if (typeof ev.data !== 'string') return;
    let m: WsServerMessage;
    try {
      m = JSON.parse(ev.data) as WsServerMessage;
    } catch {
      return;
    }
    switch (m.type) {
      case 'pong': {
        const sent = inflight.get(m.id);
        if (sent !== undefined) {
          inflight.delete(m.id);
          post({ type: 'rtt', ms: now - sent });
        }
        break;
      }
      case 'stats':
        post({ type: 'stats', t: m.t, up: m.up, down: m.down });
        break;
      case 'error':
        post({ type: 'ws-error', message: m.message || m.code, code: m.code });
        break;
    }
  };
  sock.onerror = () => post({ type: 'ws-error', message: 'WebSocket error' });
  sock.onclose = (ev: CloseEvent) => {
    if (ws === sock) {
      post({ type: 'ws-error', message: `WebSocket closed (${ev.code})`, code: 'closed' });
      close();
    }
  };
}

self.onmessage = (ev: MessageEvent<LatencyCommand>): void => {
  const m = ev.data;
  switch (m.type) {
    case 'open':
      open(m.wsUrl);
      break;
    case 'interval':
      intervalMs = m.ms;
      schedule();
      break;
    case 'close':
      close();
      break;
  }
};
