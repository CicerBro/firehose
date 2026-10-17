import { describe, expect, test } from 'bun:test';
import { unavailableLoss } from '../../src/engine/loss';
import { debugOverrides } from '../../src/engine/orchestrator';
import type { ConfigResponse } from '../../src/engine/protocol';
import { buildResult, idleLatency, loadedLatency, resultText, type ResultInput } from '../../src/engine/results';
import { bloatGrade } from '../../src/ui/tiles';

const config: ConfigResponse = {
  serverName: 'ams-1',
  version: '0.1.0',
  testHosts: ['https://s1.speed.example.com'],
  limits: { maxConnections: 32, maxDownloadBytes: 4 * 2 ** 30, maxUploadBytes: 512 * 2 ** 20 },
  rtc: { enabled: true },
  singleOrigin: false,
};

function input(over: Partial<ResultInput> = {}): ResultInput {
  return {
    timestamp: new Date('2026-10-17T12:00:00.123Z'),
    config,
    serverUrl: 'https://speed.example.com',
    ip: { ip: '2001:db8::7', family: 'ipv6' },
    userAgent: 'test-agent',
    connections: 8,
    durationSec: 12,
    graceSec: 2,
    lossEnabled: true,
    idleRtts: [50, 40, 30, 4.0, 4.4, 3.8, 4.1],
    downRtts: [18, 20, 25, 17],
    upRtts: [9, 9, 12.1],
    download: { tp: { bps: 9.41e9 + 0.4, bytes: 11.76e9, steadyMs: 10000, short: false }, p90bps: 9.48e9 },
    upload: { tp: { bps: 9.12e9, bytes: 11.4e9, steadyMs: 10000, short: false }, p90bps: 9.2e9, approx: false },
    loss: { available: true, downPct: 0, upPct: 0.16666, jitterDownMs: 0.4, jitterUpMs: 0.5, sentDown: 600, sentUp: 600, reorderedDown: 0 },
    tabHidden: false,
    aborted: false,
    ...over,
  };
}

describe('latency summaries', () => {
  test('idle drops the first 3 (warm-up)', () => {
    const l = idleLatency([50, 40, 30, 4.0, 4.4, 3.8, 4.1]);
    expect(l.samples).toBe(4);
    expect(l.minMs).toBe(3.8);
    expect(l.medianMs).toBe(4.05);
    // |4.4-4| + |3.8-4.4| + |4.1-3.8| = 0.4+0.6+0.3 = 1.3 / 3
    expect(l.jitterMs).toBeCloseTo(0.43, 2);
  });
  test('idle with ≤ 3 samples keeps them', () => {
    expect(idleLatency([5, 6]).samples).toBe(2);
    expect(idleLatency([])).toEqual({ medianMs: 0, minMs: 0, jitterMs: 0, samples: 0 });
  });
  test('loaded median and nearest-rank p90', () => {
    const l = loadedLatency([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect(l).toEqual({ medianMs: 5.5, p90Ms: 9, samples: 10 });
    expect(loadedLatency([])).toEqual({ medianMs: 0, p90Ms: 0, samples: 0 });
  });
});

describe('buildResult (§7.5 schema)', () => {
  test('exact shape and values', () => {
    const r = buildResult(input());
    expect(Object.keys(r)).toEqual(['version', 'timestamp', 'server', 'client', 'config', 'latency', 'download', 'upload', 'loss', 'flags']);
    expect(r.version).toBe(1);
    expect(r.timestamp).toBe('2026-10-17T12:00:00Z');
    expect(r.server).toEqual({ name: 'ams-1', url: 'https://speed.example.com', version: '0.1.0' });
    expect(r.client).toEqual({ ip: '2001:db8::7', family: 'ipv6', userAgent: 'test-agent' });
    expect(r.config).toEqual({ connections: 8, durationSec: 12, graceSec: 2, loss: true });
    expect(Object.keys(r.latency)).toEqual(['idle', 'download', 'upload']);
    expect(Object.keys(r.latency.idle)).toEqual(['medianMs', 'minMs', 'jitterMs', 'samples']);
    expect(Object.keys(r.latency.download)).toEqual(['medianMs', 'p90Ms', 'samples']);
    expect(r.download).toEqual({ bps: 9410000000, p90bps: 9480000000, bytes: 11760000000, steadyMs: 10000, short: false });
    expect(r.upload).toEqual({ bps: 9120000000, p90bps: 9200000000, bytes: 11400000000, steadyMs: 10000, short: false, approx: false });
    expect(r.loss).toEqual({ available: true, downPct: 0, upPct: 0.17, jitterDownMs: 0.4, jitterUpMs: 0.5, sentDown: 600, sentUp: 600, reorderedDown: 0 });
    expect(r.flags).toEqual({ tabHidden: false, singleOrigin: false, aborted: false });
    // JSON round-trips unchanged
    expect(JSON.parse(JSON.stringify(r))).toEqual(r);
  });

  test('missing pieces degrade to zeros / unavailable', () => {
    const r = buildResult(input({ ip: null, download: null, upload: null, loss: unavailableLoss(), lossEnabled: false, tabHidden: true }));
    expect(r.client.ip).toBe('');
    expect(r.download.bps).toBe(0);
    expect(r.upload.approx).toBe(false);
    expect(r.loss.available).toBe(false);
    expect(r.config.loss).toBe(false);
    expect(r.flags.tabHidden).toBe(true);
  });

  test('text summary', () => {
    const t = resultText(buildResult(input({ tabHidden: true })));
    expect(t).toContain('Download    9.41 Gbps');
    expect(t).toContain('Upload      9.12 Gbps');
    expect(t).toContain('Packet loss ↓ 0.00 %  ↑ 0.17 %');
    expect(t).toContain('2001:db8::7 (IPv6)');
    expect(t).toContain('Tab was in background');
  });
});

describe('bufferbloat grade', () => {
  test('thresholds 30 / 100 ms over idle', () => {
    expect(bloatGrade(33, 4)).toEqual({ grade: 'ok', label: 'Good' });
    expect(bloatGrade(34, 4).grade).toBe('warn');
    expect(bloatGrade(103.9, 4).grade).toBe('warn');
    expect(bloatGrade(104, 4)).toEqual({ grade: 'bad', label: 'Poor' });
  });
});

describe('debug overrides (§13.5)', () => {
  test('ignored unless the server runs with --dev', () => {
    expect(debugOverrides(config, '?duration=3&grace=0.5')).toEqual({});
  });
  test('honored in dev', () => {
    expect(debugOverrides({ ...config, dev: true }, '?duration=3&grace=0.5')).toEqual({ durationSec: 3, graceSec: 0.5 });
    expect(debugOverrides({ ...config, dev: true }, '?duration=abc&grace=-1')).toEqual({});
  });
});
