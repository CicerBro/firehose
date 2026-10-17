# Decisions

## Final decisions (from the build plan §2)

| Decision | Choice | Rationale |
|

## Changes and deviations

Each entry: date, what, why.

- **2026-10-17 — Bun instead of Node/npm for the frontend toolchain.** Owner's request. `bun install` / `bun.lock` replace `npm ci` / `package-lock.json`; scripts run with `bun --bun`. The Docker build stage uses `oven/bun` instead of `node:lts-alpine`.
- **2026-10-17 — `bun test` instead of vitest for TS unit tests.** Follows from using Bun: its built-in runner has a vitest/jest-compatible API, so no extra dependency. Same vectors (`testdata/stats-vectors.json`), same parity requirement.
- **2026-10-17 — `OWNER` placeholder set to `CicerBro`.** Module path `github.com/CicerBro/speedtest`, image `ghcr.io/cicerbro/speedtest`, taken from the local `gh` login. Domain (`speed.example.com`), server name (`ams-1`) and Authelia portal (`auth.example.com`) stay placeholders until the owner provides real values.
