// Shared types: API responses (§5.5), WebSocket messages, worker messages (§7.2) and the
// results object (§7.5, shared with the Go CLI's --json output).

// ---------- HTTP API ----------

export interface ConfigResponse {
  serverName: string;
  version: string;
  testHosts: string[];
  limits: { maxConnections: number; maxDownloadBytes: number; maxUploadBytes: number };
  rtc: { enabled: boolean };
  singleOrigin: boolean;
  /** Only present (true) when the server runs with --dev; enables debug query params (§13.5). */
  dev?: boolean;
}

export type IpFamily = 'ipv4' | 'ipv6';

export interface IpResponse {
  ip: string;
  family: IpFamily;
}

export interface SessionResponse {
  sid: string;
  expiresInSec: number;
  testHosts: string[];
  connections: number;
}

export interface ApiErrorBody {
  error: string;
  message?: string;
}

// ---------- WebSocket (/api/ws) ----------

export interface WsPing {
  type: 'ping';
  id: number;
}

export type WsServerMessage =
  | { type: 'pong'; id: number }
  | { type: 'stats'; t: number; up: number; down: number; streams?: { up: number; down: number } }
  | { type: 'error'; code: string; message: string };

// ---------- Workers ----------

export type Direction = 'down' | 'up';

/** main → transfer worker */
export type TransferCommand =
  | {
      type: 'start';
      dir: Direction;
      sid: string;
      /** One base URL (test host origin) per stream run by this worker. */
      urls: string[];
      streams: number;
      downBytes: number;
      upStartSize: number;
      upMaxSize: number;
    }
  | { type: 'stop' };

/** transfer worker → main */
export type TransferEvent =
  /** Delta since the last progress message, posted every 50 ms. For uploads: bytes of completed requests. */
  | { type: 'progress'; bytes: number }
  /**
   * A stream failed for good. `auth` = the first request failed with a network error (TypeError) before
   * any data arrived or got 401: an expired Authelia login looks exactly like that (§7.1).
   */
  | { type: 'error'; message: string; auth: boolean }
  /** All streams stopped after a 'stop' command; final progress was flushed before this. */
  | { type: 'stopped' };

/** main → latency worker */
export type LatencyCommand =
  | { type: 'open'; wsUrl: string }
  | { type: 'interval'; ms: number }
  | { type: 'close' };

/** latency worker → main */
export type LatencyEvent =
  | { type: 'ws-open' }
  | { type: 'rtt'; ms: number }
  | { type: 'stats'; t: number; up: number; down: number }
  | { type: 'ws-error'; message: string; code?: string };

// ---------- Results (§7.5) ----------

export interface IdleLatency {
  medianMs: number;
  minMs: number;
  jitterMs: number;
  samples: number;
}

export interface LoadedLatency {
  medianMs: number;
  p90Ms: number;
  samples: number;
}

export interface ThroughputResult {
  bps: number;
  p90bps: number;
  bytes: number;
  steadyMs: number;
  short: boolean;
}

export interface UploadResult extends ThroughputResult {
  approx: boolean;
}

export interface LossResult {
  available: boolean;
  downPct: number;
  upPct: number;
  jitterDownMs: number;
  jitterUpMs: number;
  sentDown: number;
  sentUp: number;
  reorderedDown: number;
}

export interface Result {
  version: 1;
  timestamp: string;
  server: { name: string; url: string; version: string };
  client: { ip: string; family: IpFamily | ''; userAgent: string };
  config: { connections: number; durationSec: number; graceSec: number; loss: boolean };
  latency: { idle: IdleLatency; download: LoadedLatency; upload: LoadedLatency };
  download: ThroughputResult;
  upload: UploadResult;
  loss: LossResult;
  flags: { tabHidden: boolean; singleOrigin: boolean; aborted: boolean };
}
