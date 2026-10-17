// Theme: dark by default, light via prefers-color-scheme, manual toggle via data-theme on <html> (§11.6).
import { load, save } from './storage';

export type Theme = 'dark' | 'light';

export function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

function parseHex(c: string): [number, number, number] | null {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(c.trim());
  if (!m) return null;
  let h = m[1]!;
  if (h.length === 3) h = h.replace(/./g, (x) => x + x);
  const n = parseInt(h, 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

const toHex = (rgb: number[]): string => '#' + rgb.map((v) => Math.round(v).toString(16).padStart(2, '0')).join('');

/** Linear mix of two hex colors (k = 0 → a, 1 → b). Non-hex input returns b. */
export function mixColor(a: string, b: string, k: number): string {
  const x = parseHex(a);
  const y = parseHex(b);
  if (!x || !y) return k < 0.5 ? a : b;
  return toHex(x.map((v, i) => v + (y[i]! - v) * k));
}

export function withAlpha(c: string, alpha: number): string {
  const x = parseHex(c);
  return x ? `rgba(${x[0]},${x[1]},${x[2]},${alpha})` : c;
}

export class ThemeController {
  private listeners: (() => void)[] = [];
  private mq = matchMedia('(prefers-color-scheme: light)');

  constructor(private readonly button: HTMLButtonElement) {
    const stored = load('theme');
    if (stored === 'dark' || stored === 'light') document.documentElement.dataset.theme = stored;
    this.mq.addEventListener('change', () => {
      if (!document.documentElement.dataset.theme) this.changed();
    });
    button.addEventListener('click', () => this.set(this.current === 'dark' ? 'light' : 'dark'));
    this.updateButton();
  }

  get current(): Theme {
    const t = document.documentElement.dataset.theme;
    if (t === 'dark' || t === 'light') return t;
    return this.mq.matches ? 'light' : 'dark';
  }

  set(t: Theme): void {
    document.documentElement.dataset.theme = t;
    save('theme', t);
    this.changed();
  }

  onChange(f: () => void): void {
    this.listeners.push(f);
  }

  private changed(): void {
    this.updateButton();
    for (const f of this.listeners) f();
  }

  private updateButton(): void {
    const next = this.current === 'dark' ? 'light' : 'dark';
    this.button.setAttribute('aria-label', `Switch to ${next} theme`);
    this.button.title = `Switch to ${next} theme`;
  }
}
