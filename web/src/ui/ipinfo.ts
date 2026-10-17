// IP strip (§9): the address the browser connected with, plus a family badge.
import type { IpResponse } from '../engine/protocol';

export function renderIp(ip: IpResponse | null): void {
  const addr = document.getElementById('ip-addr')!;
  const badge = document.getElementById('ip-family')!;
  if (!ip) {
    addr.textContent = 'unavailable';
    badge.hidden = true;
    return;
  }
  addr.textContent = ip.ip;
  badge.textContent = ip.family === 'ipv6' ? 'IPv6' : 'IPv4';
  badge.hidden = false;
}
