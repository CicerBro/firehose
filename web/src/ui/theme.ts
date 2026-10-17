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

/** User preference: follow the OS setting, or force light/dark. */
export type ThemePref = 'system' | Theme;

export class ThemeController {
  private listeners: (() => void)[] = [];
  private mq = matchMedia('(prefers-color-scheme: light)');
  private pref: ThemePref = 'system';
  private readonly inputs: HTMLInputElement[];

  constructor(group: HTMLElement) {
    this.inputs = [...group.querySelectorAll<HTMLInputElement>('input[name="theme"]')];
    const stored = load('theme');
    this.apply(stored === 'dark' || stored === 'light' ? stored : 'system');
    // In System mode, follow live OS switches.
    this.mq.addEventListener('change', () => {
      if (this.pref === 'system') this.changed();
    });
    for (const input of this.inputs) {
      input.addEventListener('change', () => {
        if (input.checked) this.set(input.value as ThemePref);
      });
    }
  }

  /** The theme actually shown. */
  get current(): Theme {
    if (this.pref !== 'system') return this.pref;
    return this.mq.matches ? 'light' : 'dark';
  }

  get preference(): ThemePref {
    return this.pref;
  }

  set(p: ThemePref): void {
    this.apply(p);
    save('theme', p);
    this.changed();
  }

  onChange(f: () => void): void {
    this.listeners.push(f);
  }

  private apply(p: ThemePref): void {
    this.pref = p;
    if (p === 'system') delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = p;
    for (const input of this.inputs) input.checked = input.value === p;
  }

  private changed(): void {
    for (const f of this.listeners) f();
  }
}
