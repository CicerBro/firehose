import { describe, expect, test } from 'bun:test';
import { Timeline } from '../../src/engine/timeline';

// 100 Mbit/s = 12.5 MB/s = 1 250 000 bytes per 100 ms
const RATE = 1_250_000;

function linear(ms: number): Timeline {
  const tl = new Timeline();
  for (let t = 100; t <= ms; t += 100) tl.add(t, (t / 100) * RATE);
  return tl;
}

describe('Timeline', () => {
  test('starts with {0, 0}', () => {
    const tl = new Timeline();
    expect(tl.samples).toEqual([{ t: 0, bytes: 0 }]);
    expect(tl.lastT).toBe(0);
    expect(tl.live()).toBe(0);
  });

  test('linear interpolation between samples', () => {
    const tl = new Timeline();
    tl.add(100, 1000);
    tl.add(200, 3000);
    expect(tl.bytesAt(50)).toBe(500);
    expect(tl.bytesAt(150)).toBe(2000);
    expect(tl.bytesAt(200)).toBe(3000);
    expect(tl.bytesAt(500)).toBe(3000); // clamped after the last sample
  });

  test('keeps the timeline monotonic', () => {
    const tl = new Timeline();
    tl.add(100, 1000);
    tl.add(100, 1500); // same t: newest count wins
    tl.add(50, 9999); // out of order: ignored
    tl.add(200, 800); // decreasing bytes: clamped
    expect(tl.samples).toEqual([
      { t: 0, bytes: 0 },
      { t: 100, bytes: 1500 },
      { t: 200, bytes: 1500 },
    ]);
  });

  test('live rate over 500 ms (bit/s)', () => {
    const tl = linear(3000);
    expect(tl.live()).toBeCloseTo(100e6, 3);
    expect(tl.live(1050)).toBeCloseTo(100e6, 3); // interpolated window [550, 1050]
  });

  test('live rate reacts to a rate change within the window', () => {
    const tl = linear(1000);
    // stall: no new bytes for 500 ms
    for (let t = 1100; t <= 1500; t += 100) tl.add(t, tl.lastBytes);
    expect(tl.live()).toBe(0);
    tl.add(1600, tl.lastBytes + 2 * RATE); // 200 Mbit/s for 100 ms
    expect(tl.live()).toBeCloseTo((2 * RATE * 8) / 0.5, 3);
  });

  test('final throughput over the steady window', () => {
    const tl = linear(12_000);
    const { tp, p90bps } = tl.final(2000);
    expect(tp.short).toBe(false);
    expect(tp.steadyMs).toBe(10_000);
    expect(tp.bps).toBeCloseTo(100e6, 3);
    expect(tp.bytes).toBeCloseTo(100 * RATE, 3);
    expect(p90bps).toBeCloseTo(100e6, 3);
  });

  test('short window falls back to [0, end]', () => {
    const tl = linear(2500);
    const { tp } = tl.final(2000);
    expect(tp.short).toBe(true);
    expect(tp.steadyMs).toBe(2500);
  });
});
