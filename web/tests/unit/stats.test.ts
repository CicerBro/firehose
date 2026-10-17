import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  interpolate,
  jitter,
  liveRate,
  lossPct,
  median,
  min,
  p90,
  rfc3550JitterMs,
  rollingP90,
  steady,
  type Probe,
  type Sample,
  type Throughput,
} from '../../src/engine/stats';

interface ListCase {
  name: string;
  in: number[];
  want: number;
}
interface Vectors {
  median: ListCase[];
  p90: ListCase[];
  min: ListCase[];
  jitter: ListCase[];
  interpolate: { name: string; timeline: Sample[]; t: number; want: number }[];
  liveRate: { name: string; timeline: Sample[]; now: number; windowMs: number; want: number }[];
  steady: { name: string; timeline: Sample[]; graceMs: number; endMs: number; want: Throughput }[];
  rollingP90: { name: string; timeline: Sample[]; graceMs: number; endMs: number; want: number }[];
  rfc3550: { name: string; probes: Probe[]; want: number }[];
  lossPct: { name: string; sent: number; received: number; want: number }[];
}

// Repo root is two levels above web/tests/unit.
const vectors = JSON.parse(
  readFileSync(join(import.meta.dir, '..', '..', '..', 'testdata', 'stats-vectors.json'), 'utf8'),
) as Vectors;

function near(got: number, want: number): void {
  if (got === want) return;
  const tol = 1e-6 * Math.max(1, Math.abs(got), Math.abs(want));
  if (Math.abs(got - want) > tol) expect(got).toBe(want);
}

describe('stats vectors', () => {
  test('every section has vectors', () => {
    for (const [k, v] of Object.entries(vectors)) {
      if (Array.isArray(v)) expect(v.length, k).toBeGreaterThan(0);
    }
  });

  const listFns = { median, p90, min, jitter } as const;
  for (const [key, fn] of Object.entries(listFns)) {
    for (const c of vectors[key as keyof typeof listFns]) {
      test(`${key}: ${c.name}`, () => {
        const before = [...c.in];
        near(fn(c.in), c.want);
        expect(c.in).toEqual(before);
      });
    }
  }

  for (const c of vectors.interpolate) {
    test(`interpolate: ${c.name}`, () => near(interpolate(c.timeline, c.t), c.want));
  }
  for (const c of vectors.liveRate) {
    test(`liveRate: ${c.name}`, () => near(liveRate(c.timeline, c.now, c.windowMs), c.want));
  }
  for (const c of vectors.steady) {
    test(`steady: ${c.name}`, () => {
      const got = steady(c.timeline, c.graceMs, c.endMs);
      near(got.bps, c.want.bps);
      near(got.bytes, c.want.bytes);
      near(got.steadyMs, c.want.steadyMs);
      expect(got.short).toBe(c.want.short);
    });
  }
  for (const c of vectors.rollingP90) {
    test(`rollingP90: ${c.name}`, () => near(rollingP90(c.timeline, c.graceMs, c.endMs), c.want));
  }
  for (const c of vectors.rfc3550) {
    test(`rfc3550JitterMs: ${c.name}`, () => near(rfc3550JitterMs(c.probes), c.want));
  }
  for (const c of vectors.lossPct) {
    test(`lossPct: ${c.name}`, () => near(lossPct(c.sent, c.received), c.want));
  }
});

test('lossPct clamps', () => {
  expect(lossPct(10, -5)).toBe(100);
  expect(lossPct(-1, 0)).toBe(0);
});
