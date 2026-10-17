// localStorage wrapped in try/catch: the app works without it (§11.5).
const PREFIX = 'speedtest.';

export function load(key: string): string | null {
  try {
    return localStorage.getItem(PREFIX + key);
  } catch {
    return null;
  }
}

export function save(key: string, value: string): void {
  try {
    localStorage.setItem(PREFIX + key, value);
  } catch {
    // ignore (private mode, disabled storage)
  }
}
