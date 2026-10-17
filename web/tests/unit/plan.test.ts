import { describe, expect, test } from 'bun:test';
import { MiB, planStreams, uploadSizes, workerCount } from '../../src/engine/plan';
import { defaultStreams, streamOptions } from '../../src/ui/controls';

describe('workerCount: clamp(ceil(N/2), 1, 4), capped at hardwareConcurrency/2', () => {
  test.each([
    [1, 16, 1],
    [2, 16, 1],
    [3, 16, 2],
    [4, 16, 2],
    [8, 16, 4],
    [16, 16, 4],
    [32, 16, 4],
    [8, 4, 2],
    [8, 2, 1],
    [8, 1, 1],
    [8, 3, 1],
    [8, undefined, 4],
    [8, 0, 4],
  ] as [number, number | undefined, number][])('N=%d hc=%p → %d', (n, hc, w) => {
    expect(workerCount(n, hc)).toBe(w);
  });
});

describe('planStreams', () => {
  test('round-robin over workers; stream i uses host i % hosts', () => {
    const hosts = ['https://s1.x', 'https://s2.x/'];
    const plan = planStreams(8, hosts, 4);
    expect(plan).toEqual([
      ['https://s1.x', 'https://s1.x'], // streams 0, 4
      ['https://s2.x', 'https://s2.x'], // streams 1, 5
      ['https://s1.x', 'https://s1.x'], // streams 2, 6
      ['https://s2.x', 'https://s2.x'], // streams 3, 7
    ]);
    expect(plan.flat().length).toBe(8);
  });
  test('single stream', () => {
    expect(planStreams(1, ['http://a'], 1)).toEqual([['http://a']]);
  });
  test('no hosts throws', () => {
    expect(() => planStreams(2, [], 1)).toThrow();
  });
});

describe('uploadSizes', () => {
  test('16…256 MiB ladder', () => {
    expect(uploadSizes(16 * MiB, 256 * MiB)).toEqual([16, 32, 64, 128, 256].map((m) => m * MiB));
  });
  test('capped by the server maxUploadBytes', () => {
    expect(uploadSizes(16 * MiB, 100 * MiB)).toEqual([16, 32, 64].map((m) => m * MiB));
  });
  test('tiny server limit → single size', () => {
    expect(uploadSizes(8 * MiB, 8 * MiB)).toEqual([8 * MiB]);
  });
});

describe('stream options', () => {
  test('filtered by maxConnections', () => {
    expect(streamOptions(32, false)).toEqual([2, 4, 8, 16, 32]);
    expect(streamOptions(10, false)).toEqual([2, 4, 8]);
  });
  test('single-origin mode caps at 6', () => {
    expect(streamOptions(32, true)).toEqual([2, 4, 6]);
  });
  test('default 8 or the largest below', () => {
    expect(defaultStreams([2, 4, 8, 16, 32])).toBe(8);
    expect(defaultStreams([2, 4, 6])).toBe(6);
  });
});
