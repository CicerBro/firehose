// Pure planning helpers for the transfer phases (§7.2–7.4). Kept DOM/worker-free for unit tests.

export const MiB = 1 << 20;
export const GiB = 1 << 30;

/** Bound browser response buffering; requests restart automatically if they finish early (§7.3). */
export const DOWN_BYTES = 64 * MiB;
export const UP_START_SIZE = 16 * MiB;
export const UP_MAX_SIZE = 256 * MiB;
const UP_LADDER = [16, 32, 64, 128, 256].map((m) => m * MiB);

/**
 * Number of transfer workers (§7.2): W = clamp(ceil(N / 2), 1, 4), further capped at
 * hardwareConcurrency / 2 (min 1). Unknown hardwareConcurrency → no extra cap.
 */
export function workerCount(streams: number, hardwareConcurrency?: number): number {
  let w = Math.min(4, Math.max(1, Math.ceil(streams / 2)));
  if (hardwareConcurrency && hardwareConcurrency > 0) w = Math.min(w, Math.max(1, Math.floor(hardwareConcurrency / 2)));
  return w;
}

/**
 * Distribute streams round-robin across workers; stream i uses testHosts[i % hosts.length].
 * Returns, per worker, the base URL of each of its streams.
 */
export function planStreams(streams: number, hosts: string[], workers: number): string[][] {
  if (hosts.length === 0) throw new Error('no test hosts');
  const plan: string[][] = Array.from({ length: workers }, () => []);
  for (let i = 0; i < streams; i++) plan[i % workers]!.push(hosts[i % hosts.length]!.replace(/\/+$/, ''));
  return plan;
}

/**
 * Origins the transfer streams use. In single-origin mode the only test host is the server's own PUBLIC_URL,
 * which is the page's origin unless the page was opened under another name (127.0.0.1 vs localhost, a LAN IP).
 * Then that URL would be cross-origin without CORS approval and every stream would fail, so use the page's origin.
 */
export function transferHosts(testHosts: string[], singleOrigin: boolean, pageOrigin: string): string[] {
  return singleOrigin ? [pageOrigin] : testHosts;
}

/** Upload size ladder 16…256 MiB bounded by [start, max] (§7.4). */
export function uploadSizes(start: number, max: number): number[] {
  const sizes = UP_LADDER.filter((s) => s >= start && s <= max);
  if (sizes.length === 0) sizes.push(Math.min(start, max));
  return sizes;
}

/** Hosts needed for N connections (§3.1): no host gets more than 4 connections. */
export const hostsNeeded = (connections: number): number => Math.ceil(connections / 4);
