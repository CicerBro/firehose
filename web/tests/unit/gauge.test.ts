import { describe, expect, test } from 'bun:test';
import { angleAt, ARC_START, ARC_SWEEP, ease, pos, ticks, VMAX_DEFAULT, VMAX_HIGH } from '../../src/ui/gauge';

describe('pos() log mapping', () => {
  test('pos(v) = log10(1 + v) / log10(1 + vMax)', () => {
    expect(pos(0)).toBe(0);
    expect(pos(VMAX_DEFAULT)).toBe(1);
    expect(pos(100)).toBeCloseTo(Math.log10(101) / Math.log10(10001), 12);
    expect(pos(1000, VMAX_HIGH)).toBeCloseTo(Math.log10(1001) / Math.log10(100001), 12);
  });
  test('clamped to [0, 1]', () => {
    expect(pos(-5)).toBe(0);
    expect(pos(Number.NaN)).toBe(0);
    expect(pos(50_000)).toBe(1);
  });
  test('monotonic', () => {
    let prev = -1;
    for (const v of [0, 0.1, 1, 5, 10, 99, 100, 1000, 9999, 10000]) {
      const p = pos(v);
      expect(p).toBeGreaterThan(prev);
      prev = p;
    }
  });
});

describe('ticks', () => {
  test('default scale: 0, 1, 10, 100, 1G, 10G', () => {
    const t = ticks();
    expect(t.map((x) => x.label)).toEqual(['0', '1', '10', '100', '1G', '10G']);
    expect(t[0]!.pos).toBe(0);
    expect(t[t.length - 1]!.pos).toBe(1);
    for (const x of t) expect(x.pos).toBeCloseTo(pos(x.value), 12);
    // 10, 100, 1G, 10G are (nearly) evenly spaced decades: ~0.25 apart.
    for (let i = 3; i < t.length; i++) expect(t[i]!.pos - t[i - 1]!.pos).toBeCloseTo(0.25, 1);
  });
  test('high scale adds 100G', () => {
    const t = ticks(VMAX_HIGH);
    expect(t.map((x) => x.label)).toEqual(['0', '1', '10', '100', '1G', '10G', '100G']);
    expect(t[t.length - 1]!.pos).toBe(1);
  });
});

describe('arc geometry', () => {
  test('240° sweep opening at the bottom (symmetric around 90°)', () => {
    expect(ARC_SWEEP).toBeCloseTo((240 * Math.PI) / 180, 12);
    const start = angleAt(0);
    const end = angleAt(1);
    expect(start).toBe(ARC_START);
    // gap centered on +90° (straight down in canvas coordinates)
    expect((start + end) / 2 - Math.PI * 1.5).toBeCloseTo(0, 12);
    expect(Math.cos(angleAt(0.5))).toBeCloseTo(0, 12); // midpoint at the top
    expect(Math.sin(angleAt(0.5))).toBeCloseTo(-1, 12);
  });
});

describe('ease', () => {
  test('exponential approach with τ = 150 ms', () => {
    expect(ease(0, 1, 150)).toBeCloseTo(1 - Math.exp(-1), 12);
    expect(ease(0.5, 0.5, 16)).toBe(0.5);
    expect(ease(0, 1, 0)).toBe(0);
    expect(ease(0, 1, 10_000)).toBeCloseTo(1, 12);
  });
});
