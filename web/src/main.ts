import './styles.css';
import { getConfig, getIp, SignedOutError } from './engine/api';
import { debugOverrides, Orchestrator, type EngineEvent } from './engine/orchestrator';
import type { ConfigResponse, IdleLatency, IpResponse, Result } from './engine/protocol';
import { resultText } from './engine/results';
import { Controls } from './ui/controls';
import { formatMsValue, rateWords } from './ui/format';
import { Gauge } from './ui/gauge';
import { renderIp } from './ui/ipinfo';
import { Sparkline } from './ui/sparkline';
import { cssVar, ThemeController } from './ui/theme';
import { Tiles } from './ui/tiles';

const $ = <T extends HTMLElement = HTMLElement>(id: string): T => document.getElementById(id) as T;

const live = $('live');
function announce(msg: string): void {
  live.textContent = msg;
}

function showError(msg: string | null): void {
  const el = $('error');
  el.textContent = msg ?? '';
  el.hidden = !msg;
}

function showSignedOut(): void {
  $('signed-out').hidden = false;
  announce('Signed out. Reload to sign in again.');
}
$('reload-btn').addEventListener('click', () => location.reload());

async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    return;
  } catch {
    // fall back below (no clipboard API / permission)
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.className = 'offscreen';
  document.body.appendChild(ta);
  ta.select();
  const ok = document.execCommand('copy');
  ta.remove();
  if (!ok) throw new Error('copy failed');
}

async function init(): Promise<void> {
  const theme = new ThemeController($<HTMLElement>('theme-toggle'));
  const gauge = new Gauge({ canvas: $<HTMLCanvasElement>('gauge'), value: $('gauge-value'), unit: $('gauge-unit'), label: $('gauge-label') });
  const liveSpark = new Sparkline($<HTMLCanvasElement>('live-spark'));
  const tiles = new Tiles();
  const accent = (phase: 'download' | 'upload'): string =>
    phase === 'download' ? cssVar('--accent-down', '#22d3ee') : cssVar('--accent-up', '#a78bfa');

  let ip: IpResponse | null = null;
  const ipP = getIp()
    .then((r) => (ip = r))
    .catch((e: unknown) => {
      if (e instanceof SignedOutError) showSignedOut();
      return null;
    })
    .then(() => renderIp(ip));

  let config: ConfigResponse;
  try {
    config = await getConfig();
  } catch (e) {
    if (e instanceof SignedOutError) showSignedOut();
    else showError(`Could not load the server configuration: ${e instanceof Error ? e.message : String(e)}`);
    $<HTMLButtonElement>('start-btn').disabled = true;
    return;
  }
  $('server-info').textContent = `${config.serverName} · v${config.version}`;
  void ipP;

  const controls = new Controls(config);
  const overrides = debugOverrides(config, location.search);

  let lastResult: Result | null = null;
  let idle: IdleLatency | null = null;
  let downBps = 0;
  let spanMs = 12_000;
  let lossEnabled = false;

  theme.onChange(() => {
    gauge.refreshColors();
    liveSpark.setColor(accent(gauge.currentPhase === 'upload' ? 'upload' : 'download'));
  });

  const onEvent = (e: EngineEvent): void => {
    switch (e.type) {
      case 'state':
        switch (e.state) {
          case 'starting':
            showError(null);
            $('tab-warning').hidden = true;
            $('copy-row').hidden = true;
            tiles.reset();
            liveSpark.clear();
            controls.setRunning(true);
            gauge.setMode('idle');
            announce('Starting test');
            break;
          case 'latency':
            gauge.setMode('latency');
            announce('Measuring latency');
            break;
          case 'download':
          case 'upload':
            gauge.setMode(e.state);
            liveSpark.reset(accent(e.state), spanMs);
            announce(e.state === 'download' ? 'Measuring download' : 'Measuring upload');
            break;
          case 'idle':
            controls.setRunning(false, lastResult !== null);
            if (!lastResult) gauge.setMode('idle');
            else gauge.setMode('done', downBps / 1e6);
            break;
          case 'aborting':
            lastResult = null;
            tiles.reset();
            liveSpark.clear();
            $('copy-row').hidden = true;
            announce('Test stopped');
            break;
          default:
            break;
        }
        break;
      case 'session': {
        const note = $('session-note');
        const n = e.session.connections;
        const hosts = e.session.testHosts.length;
        note.textContent = `${n} connection${n === 1 ? '' : 's'} · ${hosts} test host${hosts === 1 ? '' : 's'}${config.singleOrigin ? ' · single-origin mode' : ''}`;
        note.hidden = false;
        break;
      }
      case 'ping':
        gauge.setPing(e.ms);
        break;
      case 'rate':
        gauge.setRate(e.bps);
        liveSpark.push(e.t, e.bps);
        break;
      case 'idle-result':
        idle = e.idle;
        tiles.idle(e.idle.medianMs, e.idle.jitterMs, e.idle.samples);
        $('gauge').setAttribute('aria-label', `Latency ${formatMsValue(e.idle.medianMs)} milliseconds`);
        announce(`Latency ${formatMsValue(e.idle.medianMs)} milliseconds`);
        break;
      case 'loss-result':
        tiles.lossResult(e.loss, lossEnabled);
        break;
      case 'phase-result': {
        const tile = e.phase === 'download' ? tiles.down : tiles.up;
        if (e.phase === 'download') downBps = e.bps;
        tile.result(
          e.bps,
          e.p90bps,
          e.loadedMs,
          e.loadedSamples,
          idle && idle.samples > 0 ? idle.medianMs : null,
          e.approx,
          e.series.map((p) => ({ t: p.t, v: p.bps })),
          accent(e.phase),
          spanMs,
        );
        const words = `${e.phase === 'download' ? 'Download' : 'Upload'} ${rateWords(e.bps)}`;
        $('gauge').setAttribute('aria-label', words);
        announce(words);
        break;
      }
      case 'done':
        lastResult = e.result;
        $('copy-row').hidden = false;
        $('tab-warning').hidden = !e.result.flags.tabHidden;
        announce(
          `Test complete. Download ${rateWords(e.result.download.bps)}, upload ${rateWords(e.result.upload.bps)}, ping ${formatMsValue(e.result.latency.idle.medianMs)} milliseconds.`,
        );
        controls.setRunning(false, true);
        gauge.setMode('done', e.result.download.bps / 1e6);
        $('gauge').setAttribute('aria-label', `Download ${rateWords(e.result.download.bps)}`);
        break;
      case 'error':
        if (e.signedOut) showSignedOut();
        else {
          showError(`Test failed: ${e.message}`);
          announce(`Test failed: ${e.message}`);
        }
        lastResult = null;
        break;
    }
  };

  const orch = new Orchestrator(config, () => ip, onEvent);

  controls.startBtn.addEventListener('click', () => {
    if (orch.running) {
      orch.stop();
      return;
    }
    const s = controls.settings();
    const durationSec = overrides.durationSec ?? s.durationSec;
    const graceSec = overrides.graceSec ?? 2;
    spanMs = durationSec * 1000;
    lossEnabled = s.loss;
    lastResult = null;
    void orch.start({ connections: s.streams, durationSec, graceSec: Math.min(graceSec, durationSec), loss: s.loss });
  });

  // Copy results (Text / JSON)
  const copyMenu = document.querySelector<HTMLDetailsElement>('details.copy')!;
  copyMenu.addEventListener('click', (ev) => {
    const btn = (ev.target as HTMLElement).closest<HTMLButtonElement>('button[data-copy]');
    if (!btn || !lastResult) return;
    const kind = btn.dataset.copy;
    const text = kind === 'json' ? JSON.stringify(lastResult, null, 2) : resultText(lastResult);
    copyText(text).then(
      () => announce(`Results copied as ${kind === 'json' ? 'JSON' : 'text'}`),
      () => announce('Copy failed'),
    );
    copyMenu.open = false;
  });
  document.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape' && copyMenu.open) {
      copyMenu.open = false;
      copyMenu.querySelector('summary')?.focus();
    }
  });
}

void init();
