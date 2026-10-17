// Controls (§11.5): Start/Stop, Single|Multi, Advanced (streams, duration, packet loss), persisted.
import type { ConfigResponse } from '../engine/protocol';
import { load, save } from './storage';

export const STREAM_CHOICES = [2, 4, 8, 16, 32];
export const DURATIONS = [6, 12, 20];
export const SINGLE_ORIGIN_CAP = 6;

export interface Settings {
  mode: 'single' | 'multi';
  streams: number;
  durationSec: number;
  loss: boolean;
}

/** Stream options ≤ maxConnections; single-origin mode caps at 6 (adds 6 itself as an option). */
export function streamOptions(maxConnections: number, singleOrigin: boolean): number[] {
  const cap = singleOrigin ? Math.min(SINGLE_ORIGIN_CAP, maxConnections) : maxConnections;
  const opts = STREAM_CHOICES.filter((n) => n <= cap);
  if (singleOrigin && cap >= 2 && !opts.includes(cap)) opts.push(cap);
  if (opts.length === 0) opts.push(Math.max(1, cap));
  return opts;
}

/** Default stream count: 8, or the largest option below it. */
export function defaultStreams(options: number[]): number {
  const le8 = options.filter((n) => n <= 8);
  return le8.length ? le8[le8.length - 1]! : options[0]!;
}

export class Controls {
  readonly startBtn = document.getElementById('start-btn') as HTMLButtonElement;
  private startText = document.getElementById('start-text')!;
  private startIcon = document.getElementById('start-icon')!;
  private streams = document.getElementById('streams') as HTMLSelectElement;
  private duration = document.getElementById('duration') as HTMLSelectElement;
  private loss = document.getElementById('loss') as HTMLInputElement;
  private modeInputs = Array.from(document.querySelectorAll<HTMLInputElement>('input[name="mode"]'));
  private lossField = document.getElementById('loss-field')!;
  private hint = document.getElementById('streams-hint')!;
  private rtcEnabled = true;

  constructor(config: ConfigResponse) {
    const opts = streamOptions(config.limits.maxConnections, config.singleOrigin);
    this.streams.replaceChildren(
      ...opts.map((n) => {
        const o = document.createElement('option');
        o.value = String(n);
        o.textContent = String(n);
        return o;
      }),
    );
    if (config.singleOrigin) {
      this.hint.textContent = `Single-origin mode: the server has no separate test hosts, so at most ${SINGLE_ORIGIN_CAP} connections are used (browser per-host limit).`;
      this.hint.hidden = false;
    }
    this.rtcEnabled = config.rtc.enabled;
    this.lossField.hidden = !config.rtc.enabled;

    // restore persisted choices
    const mode = load('mode');
    const streams = Number(load('streams'));
    const dur = Number(load('duration'));
    const loss = load('loss');
    this.setMode(mode === 'single' ? 'single' : 'multi');
    this.streams.value = String(opts.includes(streams) ? streams : defaultStreams(opts));
    if (DURATIONS.includes(dur)) this.duration.value = String(dur);
    if (loss !== null) this.loss.checked = loss === '1';

    const persist = (): void => {
      const s = this.settings();
      save('mode', s.mode);
      save('streams', String(this.streams.value));
      save('duration', String(s.durationSec));
      save('loss', this.loss.checked ? '1' : '0');
    };
    for (const el of [this.streams, this.duration, this.loss, ...this.modeInputs]) el.addEventListener('change', persist);
  }

  private setMode(m: 'single' | 'multi'): void {
    for (const i of this.modeInputs) i.checked = i.value === m;
  }

  settings(): Settings {
    const mode = this.modeInputs.find((i) => i.checked)?.value === 'single' ? 'single' : 'multi';
    return {
      mode,
      streams: mode === 'single' ? 1 : Number(this.streams.value) || 8,
      durationSec: Number(this.duration.value) || 12,
      loss: this.rtcEnabled && this.loss.checked,
    };
  }

  /** Running: everything disabled except the Stop button. */
  setRunning(running: boolean, done = false): void {
    for (const el of [this.streams, this.duration, this.loss, ...this.modeInputs]) el.disabled = running;
    this.startText.textContent = running ? 'Stop' : done ? 'Run again' : 'Start';
    this.startIcon.textContent = running ? '■' : '▶';
    this.startBtn.classList.toggle('stop', running);
  }
}
