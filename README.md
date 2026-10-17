# Speedtest

A self-hosted, lightweight network speed test that keeps up with **10 Gbps**.
It is one Go binary that serves the web UI and the test endpoints, and that also
contains a **CLI client** for scripted runs and for measuring past browser limits.
It is designed to run behind **Traefik v3** (TLS) and **Authelia** (login) on a
host you control.

<!-- TODO: screenshots -->
| Dark | Light | Mobile |
|---|---|---|
| ![Dark theme](docs/img/dark.png) | ![Light theme](docs/img/light.png) | ![Mobile](docs/img/mobile.png) |

## Features

- **Download and upload** throughput, measured with **Single** (1 TCP connection) or **Multi** (up to 32 connections, default 8)
- **Idle latency, loaded latency and jitter** over a WebSocket. Loaded latency is measured during download and upload, so you can see bufferbloat
- **Packet loss and UDP jitter** over a WebRTC data channel (unordered, no retransmits). It shows "Unavailable" when UDP is blocked
- Your **public IP** as the server sees it, with an IPv4/IPv6 badge
- An animated semicircular **live gauge** with sparklines, light and dark themes, and a layout that works down to 360 px wide
- **Copy results** as text or JSON. The CLI produces the same JSON schema
- **CLI client** (`speedtest client`) with human, JSON and CSV output, a repeat interval for monitoring, and clear exit codes
- Honest numbers: random incompressible payload, no caching, no compression, HTTP/1.1 only on the test hosts (so "8 connections" really means 8 TCP connections), and connections spread over `s1…s8` hostnames to get past the browser limit of 6 connections per host
- Small: one static binary, a `FROM scratch` image, and no runtime dependencies in the frontend

Not in scope for v1: multiple users, result history or share links, multi-server selection, HTTP/3.

## Quick start (Docker Compose)

You need:

- A Linux host with a public IPv4 and/or IPv6 address and Docker
- A domain whose DNS you control (via an API that Traefik's DNS-01 challenge supports)
- An Authelia instance (≥ 4.38) that is already running

```bash
git clone https://github.com/CicerBro/speedtest && cd speedtest/deploy

# 1. Configure. Replace speed.example.com / ams-1 with your own values.
cp speedtest.env.example speedtest.env
sed -i "s/^SPEEDTEST_API_TOKEN=.*/SPEEDTEST_API_TOKEN=$(openssl rand -hex 32)/" speedtest.env
echo "CF_DNS_API_TOKEN=<cloudflare token with Zone:DNS:Edit>" > .env
$EDITOR traefik/traefik.yml traefik/dynamic/speedtest.yml   # ACME e-mail, domain, Authelia address

# 2. Run
docker compose up -d
```

Then open `https://speed.example.com`, sign in through Authelia, and press **Start**.

DNS records, the Authelia cookie domain, firewall rules, host tuning and the
verification checklist are in **[docs/DEPLOY.md](docs/DEPLOY.md)**. Read it before
you deploy; it takes about 30 minutes end to end.

## CLI client

The CLI ships in the same binary as the server. It uses the same protocol and
methodology as the browser, so you can use it to:

1. **Check server capacity** beyond browser limits (no 6-connections-per-host cap, no JS overhead). It is the reference measurement for 10G validation.
2. **Run headless or scripted tests**: cron, monitoring, CI, machines without a browser.
3. **Debug**: if the CLI is fast and the browser is slow, the bottleneck is the browser, not the network.

### Install

Download an archive for your platform from the
[releases page](https://github.com/CicerBro/speedtest/releases)
(linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64) and verify it:

```bash
VERSION=1.0.0; OS=linux; ARCH=amd64   # darwin / windows, arm64
curl -LO https://github.com/CicerBro/speedtest/releases/download/v$VERSION/speedtest_${VERSION}_${OS}_${ARCH}.tar.gz
curl -LO https://github.com/CicerBro/speedtest/releases/download/v$VERSION/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing          # macOS: shasum -a 256 -c SHA256SUMS --ignore-missing
tar -xzf speedtest_${VERSION}_${OS}_${ARCH}.tar.gz
sudo install -m 755 speedtest_${VERSION}_${OS}_${ARCH}/speedtest /usr/local/bin/
```

Or use Docker, or build from source:

```bash
docker run --rm -e SPEEDTEST_TOKEN ghcr.io/cicerbro/speedtest client --server https://speed.example.com
go install github.com/CicerBro/speedtest/cmd/speedtest@latest   # CLI is fine; the embedded UI is empty (use `make build` for a server)
```

### Authentication

The browser logs in through Authelia. The CLI can't do that, so it uses a static
**bearer token** instead. The server's `SPEEDTEST_API_TOKEN` must be set (generate
one with `openssl rand -hex 32`). Pass the same value to the client, preferably
through the environment so it stays out of your shell history:

```bash
export SPEEDTEST_SERVER=https://speed.example.com
export SPEEDTEST_TOKEN=…        # same value as the server's SPEEDTEST_API_TOKEN
speedtest client
```

### Examples

```bash
speedtest client                                  # full test: latency, loss, download, upload
speedtest client -n 1                             # single connection
speedtest client -n 16 -d 20s                     # 16 streams, 20 s per direction
speedtest client --only latency,down              # selected phases only
speedtest client -6 --no-loss                     # force IPv6, skip the packet-loss test
speedtest client --json | jq .download.bps        # machine-readable result (schema below)
speedtest client --format csv --csv-header -q     # one CSV line with header
speedtest client --interval 1h --format csv -q >> speed.csv   # monitoring: append one line per hour
speedtest client --server http://127.0.0.1:8080 --direct      # loopback / LAN, no TLS
```

Example output:

```
Server   ams-1 (https://speed.example.com) · v0.1.0
Client   2001:db8::7 (IPv6)
Mode     8 connections · 12 s per direction

Latency     4.1 ms   (min 3.8, jitter 0.3, 17 samples)
Download    9.41 Gbps   ▕████████████████████▏ loaded latency 18.2 ms
Upload      9.12 Gbps   ▕███████████████████▌▏ loaded latency 9.0 ms
Packet loss ↓ 0.00 %  ↑ 0.17 %   (UDP jitter ↓ 0.4 ms ↑ 0.5 ms)
```

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--server`, `-s` | `$SPEEDTEST_SERVER` (required if unset) | Server base URL (the server's `PUBLIC_URL`) |
| `--token` | `$SPEEDTEST_TOKEN` | Bearer token (the server's `SPEEDTEST_API_TOKEN`). Sent on every request, including the WebSocket upgrade. Prefer the env var |
| `--connections`, `-n` | `8` | Streams per direction. `1` = single-connection mode |
| `--duration`, `-d` | `12s` | Duration per direction |
| `--grace` | `2s` | Warm-up excluded from the final result |
| `--only` | *(all)* | Comma list of `latency,down,up,loss` |
| `--no-loss` | `false` | Skip the packet-loss test |
| `-4` / `-6` | *(off)* | Force IPv4 / IPv6 for all connections (TCP and WebRTC) |
| `--direct` | `false` | Ignore the server's test hosts and send all streams to `--server` |
| `--json` | `false` | Print only the result JSON to stdout |
| `--format` | `human` | `human`, `json` or `csv` (one line per run) |
| `--csv-header` | `false` | Print the CSV header line first |
| `--quiet`, `-q` | `false` | No live progress, summary only |
| `--interval` | `0` | Repeat the test every interval (e.g. `1h`). One result per run (NDJSON with `--json`). `0` = run once |
| `--timeout` | `90s` | Overall timeout for one run |
| `--insecure` | `false` | Skip TLS verification (self-signed lab setups; prints a warning) |
| `--ca-file` | *(system)* | Extra CA bundle (PEM) |
| `--version` | | Print the version |

If stdout isn't a TTY, live progress is turned off (as with `--quiet`). Colors are
turned off if `NO_COLOR` is set or the output isn't a TTY. Logs and warnings always
go to stderr, so `--json` output stays clean.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Test completed. Packet loss being unavailable (UDP blocked) still counts as success: `loss.available` is `false` |
| `1` | Usage or config error |
| `2` | Server unreachable, or a TLS error |
| `3` | Authentication failed: `401` (wrong or missing token), or a redirect to the Authelia login (the request didn't match the bearer-token router) |
| `4` | The test ran but a phase failed (e.g. all streams errored) |
| `130` | Interrupted (Ctrl-C). Partial results are printed with `flags.aborted: true` |

### Result JSON

The browser's "Copy JSON" and `speedtest client --json` produce the same object.
Throughput is in bits per second (SI: 1 Gbps = 10⁹ bit/s):

```json
{
  "version": 1,
  "timestamp": "2026-10-17T12:00:00Z",
  "server": { "name": "ams-1", "url": "https://speed.example.com", "version": "0.1.0" },
  "client": { "ip": "2001:db8::7", "family": "ipv6", "userAgent": "speedtest-cli/0.1.0" },
  "config": { "connections": 8, "durationSec": 12, "graceSec": 2, "loss": true },
  "latency": {
    "idle":     { "medianMs": 4.1, "minMs": 3.8, "jitterMs": 0.3, "samples": 17 },
    "download": { "medianMs": 18.2, "p90Ms": 25.0, "samples": 40 },
    "upload":   { "medianMs": 9.0,  "p90Ms": 12.1, "samples": 40 }
  },
  "download": { "bps": 9410000000, "p90bps": 9480000000, "bytes": 11760000000, "steadyMs": 10000, "short": false },
  "upload":   { "bps": 9120000000, "p90bps": 9200000000, "bytes": 11400000000, "steadyMs": 10000, "short": false, "approx": false },
  "loss":     { "available": true, "downPct": 0.00, "upPct": 0.17, "jitterDownMs": 0.4, "jitterUpMs": 0.5, "sentDown": 600, "sentUp": 600, "reorderedDown": 0 },
  "flags":    { "tabHidden": false, "singleOrigin": false, "aborted": false }
}
```

## Server configuration

`speedtest serve` reads `SPEEDTEST_*` environment variables. Every variable has an
equivalent flag in kebab case (`SPEEDTEST_MAX_CONNECTIONS` ↔ `--max-connections`),
and flags take precedence. Sizes accept `KiB`/`MiB`/`GiB`, and durations use Go syntax
(`35s`, `5m`). The server checks the configuration at startup and exits with a clear
message if something is invalid. See [`deploy/speedtest.env.example`](deploy/speedtest.env.example).

| Env | Flag | Default | Meaning |
|---|---|---|---|
| `SPEEDTEST_LISTEN` | `--listen` | `127.0.0.1:8080` | HTTP listen address (Traefik proxies here) |
| `SPEEDTEST_PUBLIC_URL` | `--public-url` | *(required in prod)* | UI origin, e.g. `https://speed.example.com`. Used for the CORS allowlist and CSP |
| `SPEEDTEST_TEST_HOSTS` | `--test-hosts` | *(empty)* | Comma-separated test origins `https://s1.speed.example.com,…`. Empty = single-origin mode (streams go to `PUBLIC_URL`, browser capped at 6 connections) |
| `SPEEDTEST_API_TOKEN` | `--api-token` | *(empty)* | Bearer token for the CLI. Empty disables bearer access |
| `SPEEDTEST_AUTH_HEADER` | `--auth-header` | `Remote-User` | Header that Authelia sets. Requests without a valid bearer token must carry it |
| `SPEEDTEST_TRUSTED_PROXIES` | `--trusted-proxies` | `127.0.0.1/32,::1/128` | CIDRs whose `X-Forwarded-For` is trusted |
| `SPEEDTEST_SERVER_NAME` | `--server-name` | hostname | Shown in the UI |
| `SPEEDTEST_MAX_ACTIVE_SESSIONS` | `--max-active-sessions` | `1` | Concurrent tests. A new test cancels the oldest one ("newest wins") |
| `SPEEDTEST_MAX_CONNECTIONS` | `--max-connections` | `32` | Max streams per session per direction |
| `SPEEDTEST_MAX_REQUEST_DURATION` | `--max-request-duration` | `35s` | Hard deadline per download/upload request |
| `SPEEDTEST_MAX_DOWNLOAD_BYTES` | `--max-download-bytes` | `4GiB` | Max bytes per download request |
| `SPEEDTEST_MAX_UPLOAD_BYTES` | `--max-upload-bytes` | `512MiB` | Max body per upload request |
| `SPEEDTEST_SESSION_TTL` | `--session-ttl` | `120s` | Idle expiry |
| `SPEEDTEST_SESSION_MAX_AGE` | `--session-max-age` | `5m` | Hard expiry |
| `SPEEDTEST_RTC_ENABLED` | `--rtc-enabled` | `true` | Enable the packet-loss test |
| `SPEEDTEST_RTC_UDP_PORT` | `--rtc-udp-port` | `50000` | Single UDP port for all WebRTC traffic |
| `SPEEDTEST_RTC_PUBLIC_IPS` | `--rtc-public-ips` | *(empty)* | Public IPs to advertise as ICE candidates (only behind NAT) |
| `SPEEDTEST_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `SPEEDTEST_LOG_FORMAT` | `--log-format` | `json` | `json` or `text` |
| `SPEEDTEST_METRICS_LISTEN` | `--metrics-listen` | *(empty)* | e.g. `127.0.0.1:9090` to expose Prometheus `/metrics`. Empty = off |
| — | `--dev` | `false` | Local development: turns off the auth check and enables debug query overrides. Logs a loud warning. **Never use in production** |

## Architecture

```
Browser / CLI
   │ TCP 443 (TLS)                                   UDP 50000 (WebRTC,
   ▼                                                 DTLS end-to-end)
Traefik v3 (TLS termination)                                 │
   │  speed.example.com   → h2 + http/1.1                    │
   │  s1..s8.speed.…      → http/1.1 only                    │
   │  forwardAuth → Authelia (browser), or                   │
   │  "Authorization: Bearer" router (CLI)                   │
   ▼  plain HTTP/1.1 → 127.0.0.1:8080                        ▼
┌─────────────────────────────────────────────────────────────────┐
│ speedtest serve (single Go binary)                              │
│  embedded UI · /api/config · /api/session · /api/ip             │
│  /api/ws (latency + live upload stats) · /api/rtc/offer         │
│  /api/down · /api/up                                            │
│  auth check (Remote-User or bearer) · sessions · pion WebRTC    │
└─────────────────────────────────────────────────────────────────┘
```

- **Download** is counted client-side (bytes actually received). **Upload** is counted server-side and pushed over the WebSocket every 100 ms. Browser upload progress counts bytes handed to the socket buffer, which inflates early readings, so it isn't used.
- **Latency** uses WebSocket ping/pong on its own connection, which is authenticated once at upgrade, so pings never pay for an Authelia round trip.
- **Final throughput** uses a steady window: the first 2 s of each direction are a warm-up and are excluded. Go and TypeScript share the same statistics code paths, checked against shared test vectors (`testdata/stats-vectors.json`).
- The full specification is in [docs/BUILD-PLAN.md](docs/BUILD-PLAN.md), and agreed deviations are in [docs/DECISIONS.md](docs/DECISIONS.md).

## Development

Requirements: Go ≥ 1.25, [Bun](https://bun.sh) ≥ 1.4, `make`.

```bash
make dev          # Go server (--dev, single-origin) on :8080 + Vite dev server on http://localhost:5173
make build        # bun install + vite build, then go build → ./speedtest (UI embedded)
make test         # go test -race ./... + bun test
make lint         # go vet, staticcheck, tsc --noEmit
make e2e          # Playwright (Chromium, Firefox, WebKit)
make docker       # local image
make bench-loopback   # CLI against a local server, -n 8 and -n 1 (target ≥ 20 Gbps)
```

`make dev` runs without Traefik or Authelia: `--dev` turns off the auth check and logs a
warning. The frontend must be built before `go build`, because `web/dist` is embedded.
`web/dist/.gitkeep` keeps the embed valid in a fresh checkout, so `go vet` and `go test` work
without building the UI.

## Documentation

- [docs/DEPLOY.md](docs/DEPLOY.md): production deployment (DNS, certificates, Authelia, firewall, tuning, verification, troubleshooting)
- [docs/PERFORMANCE.md](docs/PERFORMANCE.md): 10 Gbps validation report
- [docs/BUILD-PLAN.md](docs/BUILD-PLAN.md): full specification
- [docs/DECISIONS.md](docs/DECISIONS.md): decisions and deviations
