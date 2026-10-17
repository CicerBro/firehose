# Decisions

## Final decisions (from the build plan §2)

| Decision | Choice | Rationale |
|

## Changes and deviations

Each entry: date, what, why.

- **2026-10-17 — Bun instead of Node/npm for the frontend toolchain.** Owner's request. `bun install` / `bun.lock` replace `npm ci` / `package-lock.json`; scripts run with `bun --bun`. The Docker build stage uses `oven/bun` instead of `node:lts-alpine`.
- **2026-10-17 — `bun test` instead of vitest for TS unit tests.** Follows from using Bun: its built-in runner has a vitest/jest-compatible API, so no extra dependency. Same vectors (`testdata/stats-vectors.json`), same parity requirement.
- **2026-10-17 — `OWNER` placeholder set to `CicerBro`.** Module path `github.com/CicerBro/speedtest`, image `ghcr.io/cicerbro/speedtest`, taken from the local `gh` login. Domain (`speed.example.com`), server name (`ams-1`) and Authelia portal (`auth.example.com`) stay placeholders until the owner provides real values.
- **2026-10-17 — Traefik pinned to `traefik:v3.7`** (current v3 minor at implementation time) instead of v3.5 from the plan's example.
- **2026-10-17 — Compose reads app settings from `env_file: speedtest.env`** (same format as systemd's `/etc/speedtest.env`) instead of inline `environment:`; keeps the API token out of the compose file. `CF_DNS_API_TOKEN` comes from `deploy/.env`.
- **2026-10-17 — Release version strings drop the leading `v`** (`v1.2.3` tag → `1.2.3` in archives, image tags and `/api/config`), matching the plan's `"0.1.0"` examples.
- **2026-10-17 — All Traefik routers declare the wildcard `domains`** so a single certificate (`speed.example.com` + `*.speed.example.com`) is requested.
