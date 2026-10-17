import { describe, expect, test } from 'bun:test';
import { formatBytes, formatMs, formatMsValue, formatPct, formatRate, rateWords, round, sig3 } from '../../src/ui/format';

describe('sig3', () => {
  test('three significant digits', () => {
    expect(sig3(9.4123)).toBe('9.41');
    expect(sig3(94.123)).toBe('94.1');
    expect(sig3(941.23)).toBe('941');
    expect(sig3(1.5)).toBe('1.50');
    expect(sig3(0)).toBe('0');
  });
});

describe('formatRate', () => {
  test('auto units: kbps < 1 Mbps ≤ Mbps < 1000 ≤ Gbps', () => {
    expect(formatRate(9.41e9)).toEqual({ value: '9.41', unit: 'Gbps' });
    expect(formatRate(941e6)).toEqual({ value: '941', unit: 'Mbps' });
    expect(formatRate(94.1e6)).toEqual({ value: '94.1', unit: 'Mbps' });
    expect(formatRate(1e6)).toEqual({ value: '1.00', unit: 'Mbps' });
    expect(formatRate(512e3)).toEqual({ value: '512', unit: 'kbps' });
    expect(formatRate(1.5e3)).toEqual({ value: '1.50', unit: 'kbps' });
    expect(formatRate(12.3e9)).toEqual({ value: '12.3', unit: 'Gbps' });
    expect(formatRate(123e9)).toEqual({ value: '123', unit: 'Gbps' });
  });
  test('rounding rolls over to the next unit', () => {
    expect(formatRate(999.7e6)).toEqual({ value: '1.00', unit: 'Gbps' });
    expect(formatRate(999.96e3)).toEqual({ value: '1.00', unit: 'Mbps' });
    expect(formatRate(999.4e6)).toEqual({ value: '999', unit: 'Mbps' });
  });
  test('zero / invalid', () => {
    expect(formatRate(0)).toEqual({ value: '0', unit: 'Mbps' });
    expect(formatRate(Number.NaN)).toEqual({ value: '0', unit: 'Mbps' });
    expect(formatRate(-5)).toEqual({ value: '0', unit: 'Mbps' });
  });
  test('spoken form', () => {
    expect(rateWords(9.41e9)).toBe('9.41 gigabits per second');
    expect(rateWords(50e6)).toBe('50.0 megabits per second');
  });
});

describe('ms / pct / bytes', () => {
  test('ms precision by magnitude', () => {
    expect(formatMsValue(0.345)).toBe('0.34');
    expect(formatMsValue(4.14)).toBe('4.1');
    expect(formatMsValue(18.25)).toBe('18.3');
    expect(formatMsValue(123.6)).toBe('124');
    expect(formatMs(4.1)).toBe('4.1 ms');
    expect(formatMsValue(-1)).toBe('—');
  });
  test('pct two decimals', () => {
    expect(formatPct(0)).toBe('0.00 %');
    expect(formatPct(0.1666)).toBe('0.17 %');
  });
  test('binary bytes', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(16 * 1024 * 1024)).toBe('16.0 MiB');
    expect(formatBytes(4 * 1024 ** 3)).toBe('4.00 GiB');
  });
  test('round', () => {
    expect(round(1.005, 2)).toBeCloseTo(1.0, 2);
    expect(round(4.149, 1)).toBe(4.1);
    expect(round(Number.NaN, 2)).toBe(0);
  });
});
