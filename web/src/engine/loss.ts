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
//   - opts.signal     aborts the test (user Stop): close the peer connection and resolve
//                     (or reject with AbortError; the orchestrator ignores the outcome after abort).
//
//   Resolves with the §7.5 `loss` object. It NEVER rejects for network reasons: if WebRTC is
//   unsupported, ICE does not connect within 5 s (UDP blocked, §8.3), or anything else fails,
//   it resolves with `unavailableLoss()` (available:false). The orchestrator runs it in parallel
//   with the latency phase when the user enabled it and config.rtc.enabled is true, and waits
//   for it (bounded by durationMs + 8 s) before the download phase starts.
//
// Implementation is milestone M6 (another worker). This stub reports "unavailable".
import type { LossResult } from './protocol';

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

export async function runLossTest(opts: LossTestOptions): Promise<LossResult> {
  void opts;
  return unavailableLoss();
}
