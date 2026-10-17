import { describe, expect, test } from 'bun:test';
import {
  KIND_DOWN,
  KIND_UP,
  LOSS_DEFAULTS,
  PROBE_HEADER_SIZE,
  ProbeReceiver,
  decodeProbe,
  encodeProbe,
  lossResult,
  parseReport,
  runLossTest,
  totalProbes,
  unavailableLoss,
} from '../../src/engine/loss';

describe('probe encoding', () => {
  test('layout matches §8.2 (big-endian, zero padded)', () => {
    const buf = encodeProbe(KIND_UP, 0xdeadbeef, 2 ** 40 + 7, 200);
    expect(buf.byteLength).toBe(200);
    const b = new Uint8Array(buf);
    expect([...b.subarray(0, PROBE_HEADER_SIZE)]).toEqual([2, 0xde, 0xad, 0xbe, 0xef, 0, 0, 1, 0, 0, 0, 0, 7]);
    expect(b.subarray(PROBE_HEADER_SIZE).every((x) => x === 0)).toBe(true);
  });

  test('round trip', () => {
    const us = 1_234_567_890_123;
    expect(decodeProbe(encodeProbe(KIND_DOWN, 599, us, 200))).toEqual({ kind: KIND_DOWN, seq: 599, sendUs: us });
    expect(decodeProbe(encodeProbe(KIND_UP, 0, 0, 13))).toEqual({ kind: KIND_UP, seq: 0, sendUs: 0 });
  });

  test('short packets are rejected; size never below the header', () => {
    expect(decodeProbe(new ArrayBuffer(12))).toBeNull();
    expect(encodeProbe(KIND_UP, 1, 1, 4).byteLength).toBe(PROBE_HEADER_SIZE);
  });

  test('totalProbes', () => {
    expect(totalProbes(LOSS_DEFAULTS.pps, LOSS_DEFAULTS.durationMs)).toBe(600);
    expect(totalProbes(100, 5)).toBe(1);
  });
});

describe('ProbeReceiver', () => {
  test('counts reordering, ignores duplicates and out-of-range seqs', () => {
    const rx = new ProbeReceiver(10);
    for (const seq of [0, 2, 1, 1, 3, 5, 4, 42]) rx.add(seq, seq * 1000, seq * 1000 + 500);
    expect(rx.reordered).toBe(2); // 1 after 2, 4 after 5
    expect(rx.summary(10).received).toBe(6);
    expect(rx.summary(3).received).toBe(3);
    expect(rx.summary(10).jitterMs).toBe(0); // constant transit time
  });

  test('in-order stream has no reordering', () => {
    const rx = new ProbeReceiver(100);
    for (let i = 0; i < 100; i++) rx.add(i, i * 10_000, i * 10_000 + 3000 + (i % 2) * 1000);
    expect(rx.reordered).toBe(0);
    expect(rx.summary(100).received).toBe(100);
    expect(rx.summary(100).jitterMs).toBeGreaterThan(0);
  });
});

describe('report handling', () => {
  test('parseReport', () => {
    expect(parseReport('{"type":"report","sentDown":600,"receivedUp":599,"jitterUpMs":0.5,"reorderedUp":0}')).toEqual({
      type: 'report',
      sentDown: 600,
      receivedUp: 599,
      jitterUpMs: 0.5,
      reorderedUp: 0,
    });
    expect(parseReport('{"type":"start"}')).toBeNull();
    expect(parseReport('nope')).toBeNull();
    expect(parseReport(new ArrayBuffer(3))).toBeNull();
  });

  test('lossResult: every 10th probe lost each way → 10 %', () => {
    const rx = new ProbeReceiver(600);
    for (let i = 0; i < 600; i++) if (i % 10 !== 9) rx.add(i, i * 10_000, i * 10_000 + 2000);
    const r = lossResult(rx, { type: 'report', sentDown: 600, receivedUp: 540, jitterUpMs: 0.456, reorderedUp: 0 }, 600);
    expect(r).toEqual({
      available: true,
      downPct: 10,
      upPct: 10,
      jitterDownMs: 0,
      jitterUpMs: 0.46,
      sentDown: 600,
      sentUp: 600,
      reorderedDown: 0,
    });
  });
});

test('runLossTest resolves unavailable without WebRTC', async () => {
  const r = await runLossTest({ offerUrl: '/api/rtc/offer?sid=x', ...LOSS_DEFAULTS, signal: new AbortController().signal });
  expect(r).toEqual(unavailableLoss());
});
