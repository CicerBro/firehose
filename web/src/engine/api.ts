// HTTP API calls on the main host (§5.5): config, ip, session.
import type { ApiErrorBody, ConfigResponse, IpResponse, SessionResponse } from './protocol';

/** The Authelia login expired (redirect or 401). The UI offers a reload (§7.1). */
export class SignedOutError extends Error {
  constructor() {
    super('Signed out. Reload to sign in again.');
    this.name = 'SignedOutError';
  }
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

async function errorFrom(res: Response): Promise<ApiError> {
  let body: ApiErrorBody | null = null;
  try {
    body = (await res.json()) as ApiErrorBody;
  } catch {
    // not JSON
  }
  return new ApiError(res.status, body?.error ?? 'http_' + res.status, body?.message ?? body?.error ?? `HTTP ${res.status}`);
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { cache: 'no-store', credentials: 'same-origin', redirect: 'manual' });
  if (res.type === 'opaqueredirect' || res.status === 401) throw new SignedOutError();
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as T;
}

export const getConfig = (): Promise<ConfigResponse> => getJSON<ConfigResponse>('/api/config');
export const getIp = (): Promise<IpResponse> => getJSON<IpResponse>('/api/ip');

export async function createSession(connections: number, signal?: AbortSignal): Promise<SessionResponse> {
  const res = await fetch('/api/session', {
    method: 'POST',
    cache: 'no-store',
    credentials: 'same-origin',
    // Detect an expired Authelia login instead of following its redirect (§7.1).
    redirect: 'manual',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ connections }),
    signal,
  });
  if (res.type === 'opaqueredirect' || res.status === 401) throw new SignedOutError();
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as SessionResponse;
}

/** WebSocket URL on the main host for a session. */
export function wsUrl(sid: string, loc: { protocol: string; host: string } = location): string {
  const proto = loc.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${loc.host}/api/ws?sid=${encodeURIComponent(sid)}`;
}
