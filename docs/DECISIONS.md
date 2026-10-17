# Decisions

## Final decisions (from the build plan §2)

| Decision | Choice | Rationale |
|

## Changes and deviations

Each entry: date, what, why.

- **2026-10-17 — Bun instead of Node/npm for the frontend toolchain.** Owner's request. `bun install` / `bun.lock` replace `npm ci` / `package-lock.json`; scripts run with `bun --bun`. The Docker build stage uses `oven/bun` instead of `node:lts-alpine`.
- **2026-10-17 — `bun test` instead of vitest for TS unit tests.** Follows from using Bun: its built-in runner has a vitest/jest-compatible API, so no extra dependency. Same vectors (`testdata/stats-vectors.json`), same parity requirement.
- **2026-10-17 — `OWNER` placeholder set to `CicerBro`.** Module path `github.com/CicerBro/firehose`, image `ghcr.io/cicerbro/firehose`, taken from the local `gh` login. Domain (`speed.example.com`), server name (`ams-1`) and Authelia portal (`auth.example.com`) stay placeholders until the owner provides real values.
- **2026-10-17 — Traefik pinned to `traefik:v3.7`** (current v3 minor at implementation time) instead of v3.5 from the plan's example.
- **2026-10-17 — Compose reads app settings from `env_file: firehose.env`** (same format as systemd's `/etc/firehose.env`) instead of inline `environment:`; keeps the API token out of the compose file. `CF_DNS_API_TOKEN` comes from `deploy/.env`.
- **2026-10-17 — Release version strings drop the leading `v`** (`v1.2.3` tag → `1.2.3` in archives, image tags and `/api/config`), matching the plan's `"0.1.0"` examples.
- **2026-10-17 — All Traefik routers declare the wildcard `domains`** so a single certificate (`speed.example.com` + `*.speed.example.com`) is requested.
- **2026-10-17 — Theme control is a three-way switch (System / Light / Dark), System by default.** Owner's request; System follows `prefers-color-scheme` live. Replaces the plan's two-state toggle (§11.6).
- **2026-10-17 — Result JSON rounding.** Browser rounds ms to 2 decimals, CLI to 3; bps/bytes are integers; percentages 2 decimals. Phases that didn't run are present with zeros (`samples: 0`) so the shape is stable.
- **2026-10-17 — Server: `OPTIONS` on `/api/down` and `/api/up` is answered before the auth check** (preflights carry no cookies). `/api/config` reports `rtc.enabled` only when the WebRTC handler is wired; adds `"dev": true` in dev mode. Unknown sid on `/api/ws` upgrades, sends `session_expired` and closes with code 4410 (browsers can't read a 410 on an upgrade). Stream cap per session is `maxConnections + 2`.
- **2026-10-17 — WebRTC: `SetICEAddressRewriteRules` instead of the deprecated `SetNAT1To1IPs`;** `github.com/pion/ice/v4` imported (part of the pion stack) only to disable mDNS. Probe parameters validated (pps 1–1000, duration ≤ 10 s, size 13–1200 B). An offer waits up to 2 s for a free peer slot, then 503 (clients show "unavailable").
- **2026-10-17 — CLI: in single-origin or `--direct` mode all streams go to `--server`;** the CLI still opens `-n` streams (no per-host limit). `--timeout` expiry exits 4 with `aborted:true` (130 is only for Ctrl-C/SIGTERM). The client never uses an HTTP proxy.
- **2026-10-17 — Browser: upload Blobs are built per worker** (not shared across workers), freed when no stream uses a size any more. Gauge ticks are placed with `pos()`, so they are not exactly evenly spaced near 0.
- **2026-10-17 — Not verifiable locally:** M5 (real host, Traefik, Authelia, DNS), the 10G rows of §13.3, `netem` tests (Linux only) and the §13.6 manual matrix need the owner's infrastructure. Firefox e2e couldn't launch on the dev Mac ("Could not find profile folder", a local Playwright/Firefox issue); Chromium and WebKit e2e pass, and CI runs all three engines on Linux.
- **2026-10-17 — "Copy results" became a "Share" menu (Image / Copy text / Copy JSON).** Owner request. Image: 1200×630 PNG of the result tiles in the current theme; copied to the clipboard where supported, else the system share sheet (Web Share with a file), else downloaded. Not opened in a new tab: a `blob:` page would inherit the strict CSP.
