// Unit formatting (§2 Units: SI decimal bits/s; sizes binary).

export type RateUnit = 'kbps' | 'Mbps' | 'Gbps';

/** 3 significant digits, trailing zeros kept (stable width with tabular numerals). */
export function sig3(v: number): string {
  if (!Number.isFinite(v) || v === 0) return '0';
  const s = v.toPrecision(3);
  return s.includes('e') ? String(Number(s)) : s;
}

/** Throughput with an auto unit: kbps < 1 Mbps ≤ Mbps < 1000 Mbps ≤ Gbps (§11.2). */
export function formatRate(bps: number): { value: string; unit: RateUnit } {
  if (!Number.isFinite(bps) || bps <= 0) return { value: '0', unit: 'Mbps' };
  const mbps = bps / 1e6;
  // Decide the unit on the *rounded* value so 999.7 Mbps reads 1.00 Gbps, not "1.00e+3 Mbps".
  if (Number(mbps.toPrecision(3)) >= 1000) return { value: sig3(mbps / 1000), unit: 'Gbps' };
  if (Number(mbps.toPrecision(3)) >= 1) return { value: sig3(mbps), unit: 'Mbps' };
  const kbps = bps / 1e3;
  if (Number(kbps.toPrecision(3)) >= 1000) return { value: '1.00', unit: 'Mbps' };
  return { value: sig3(kbps), unit: 'kbps' };
}

export function rateText(bps: number): string {
  const r = formatRate(bps);
  return `${r.value} ${r.unit}`;
}

const UNIT_WORDS: Record<RateUnit, string> = { kbps: 'kilobits', Mbps: 'megabits', Gbps: 'gigabits' };

/** Spoken form for screen readers: "9.41 gigabits per second". */
export function rateWords(bps: number): string {
  const r = formatRate(bps);
  return `${r.value} ${UNIT_WORDS[r.unit]} per second`;
}

/** Milliseconds: 2 decimals below 1 ms, 1 decimal below 100 ms, integer above. */
export function formatMsValue(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—';
  if (ms < 1) return ms.toFixed(2);
  if (ms < 100) return ms.toFixed(1);
  return Math.round(ms).toString();
}

export const formatMs = (ms: number): string => `${formatMsValue(ms)} ms`;

export const formatPct = (pct: number): string => `${(Number.isFinite(pct) ? pct : 0).toFixed(2)} %`;

/** Binary byte sizes (KiB/MiB/GiB). */
export function formatBytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i === 0 ? `${v} B` : `${sig3(v)} ${units[i]}`;
}

/** Round to `d` decimals (for the results JSON). */
export function round(v: number, d: number): number {
  if (!Number.isFinite(v)) return 0;
  const f = 10 ** d;
  return Math.round(v * f) / f;
}
