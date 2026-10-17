# Speedtest — Build Plan

> Handoff document for the implementing agent. Read it end to end before writing code.
> The decisions in §2 are **final**. Don't revisit them without asking the project owner.
> Placeholders to replace everywhere: `speed.example.com` (domain), `OWNER` (GitHub owner), `ams-1` (server name), `auth.example.com` (Authelia portal).

---

## 0. How to use this document

- Work **milestone by milestone** (§16). Each milestone has acceptance criteria (AC). A milestone is done only when all of its ACs pass.
- Commit at least once per milestone. Use conventional commit messages (`feat:`, `fix:`, `chore:`…).
- If something in this plan turns out to be wrong or impossible, don't silently deviate. Add an entry to `docs/DECISIONS.md` (date, what, why) and flag it to the owner.
- Prefer the standard library. The only allowed third-party Go dependencies are listed in §5.3. The frontend has **zero runtime dependencies**.
- Everything that measures something must be testable in isolation (pure functions plus shared test vectors, §13).

---

## 1. Goal and scope

A self-hosted, light, easy-to-deploy **network speed test**. It consists of:

- **One Go binary** that serves the web UI (embedded) and the test endpoints, and also contains a CLI client.
- A **TypeScript web frontend**: simple, modern design with an animated semicircular live gauge.
- **Traefik** in front, handling TLS. We already host this, no need to build anything Traefik here.

### 1.1 Functional requirements

| # | Requirement | Priority |
|---|---|---|
| F1 | Measure **download** throughput | Must |
| F2 | Measure **upload** throughput | Must |
| F3 | User chooses **Single** (1 TCP connection) or **Multi** (N TCP connections, default 8) | Must |
| F4 | Support **≥ 10 Gbps** end-to-end; design must not cap below that | Must |
| F5 | Show the client's public IP address as the server sees it, and whether it's IPv4 or IPv6 (whichever the browser connected with) | Must |
| F6 | Show **latency** (idle) to the server | Must |
| F7 | Show **latency under load** (during download and during upload; this reveals bufferbloat) | Should |
| F8 | Show **jitter** | Should (bonus) |
| F9 | Show **packet loss** (UDP-like, via WebRTC) | Should (bonus) |
| F10 | **CLI client** using the same protocol, to verify server capacity beyond browser limits | Must |
| F11 | Copy results as text/JSON | Should |
| F12 | Light and dark theme, responsive down to 360 px width | Must |
| F13 | **Single user, behind Authelia** (Traefik forwardAuth). Reachable over the internet, but every request is authenticated | Must |

### 1.2 Non-goals (v1)

Public or multi-user operation (user management, quotas, abuse protection beyond basic safety limits), detecting both IPv4 and IPv6 addresses (only the address in use is shown; split A/AAAA hostnames can be added later if ever needed), stored result history or share links, multi-server discovery or selection, HTTP/3 testing, ads/analytics, native mobile apps, WebAssembly, i18n. (Several are listed as stretch goals in §16.)

---

## 2. Final decisions (do not revisit)

| Decision | Choice | Rationale |
|---|---|---|
| Server language | **Go** (latest stable, ≥ 1.25) | Single static binary, `go:embed`, trivial cross-compiling, goroutines, best server-side WebRTC library (pion). Traefik (Go) does TLS in front anyway, so Rust's CPU advantage would be mostly unused. |
| Frontend | **Vanilla TypeScript + Vite**, no UI framework, no runtime deps | The bottleneck is browser networking APIs, not language. Small bundle. |
| WebAssembly | **No** | Has to go through the same `fetch`/WebSocket APIs, so no speed gain and a bigger bundle. |
| TLS / edge | **Traefik v3** terminates TLS, proxies plain HTTP to the app on loopback | Owner's existing infra. |
| Test transport | **HTTP/1.1 only** on test hostnames (ALPN restricted in Traefik) | HTTP/2 multiplexes all streams onto one TCP connection (and Chrome coalesces HTTP/2 connections across hostnames), which would make "multi-connection" fake. |
| Connection fan-out | Multiple test hostnames `s1…s8.speed.example.com` → same server | Browsers allow ~6 HTTP/1.1 connections per host. Spreading over hostnames allows up to 32 connections. |
| HTTP/3 / QUIC | **Not enabled** on the Traefik entrypoint | Browser QUIC is CPU-bound and tops out well below TCP. It would also distort results. |
| CDN / proxy | **Never** behind a CDN (if DNS is on Cloudflare: records are **DNS-only / grey cloud**) | A CDN would measure itself, not this server. |
| Download measurement | Counted **client-side** (bytes read from the response stream) | Accurate: these are bytes the browser actually received. |
| Upload measurement | Counted **server-side**, pushed to the client over WebSocket every 100 ms | Browser upload progress counts bytes handed to the socket buffer, which inflates early readings. |
| Latency / jitter | **WebSocket** ping/pong on a separate connection, run in a Web Worker | Low noise. Also measures queueing during load. |
| Packet loss | **WebRTC data channel**, `ordered:false, maxRetransmits:0`, server via **pion/webrtc v4** | The only portable UDP-like transport in browsers. Bypasses Traefik (DTLS is end-to-end). |
| Test payload | **Random, incompressible** bytes, `Cache-Control: no-store`, never compressed | Prevents compression or caching by middleboxes. |
| Units | Throughput in **SI decimal bits/s** (1 Mbps = 10⁶ bit/s). Buffer and byte sizes in binary (KiB/MiB/GiB) | Industry convention. |
| Deploy | Single binary with embedded UI. Docker image `FROM scratch`. **Host networking** in production | Simplest setup. Avoids Docker NAT overhead at 10G and UDP NAT trouble for WebRTC. |
| Auth | **Authelia** via Traefik `forwardAuth` on **every** hostname (UI and test hosts). The app also checks the `Remote-User` header as a second line of defense | Single user, reachable over the internet. Auth is handled by the owner's existing infra. |
| CLI auth | Static **bearer token** (`SPEEDTEST_API_TOKEN`). Separate Traefik routers match `Authorization: Bearer …`, skip Authelia, and the app validates the token | A CLI can't do Authelia's browser login and 2FA. |
| Abuse protection | Minimal safety limits only: per-request byte/time caps, stream cap, one active test at a time (a new test cancels the old one). **No rate limiting** | Only the owner can reach the app. |
| IP display | Only the address the browser connected with (`/api/ip` on the main host) | Owner's choice. Dual-stack detection dropped. |

---

## 3. Architecture

```
Browser / CLI
   │ TCP 443 (TLS)                                  UDP 50000 (WebRTC,
   ▼                                                DTLS end-to-end)
Traefik v3 (TLS termination)                                │
   │  speed.example.com    → h2 + http/1.1                  │
   │  s1..s8.speed.…       → http/1.1 ONLY                  │
   │  forwardAuth → Authelia on every request, except       │
   │  "Authorization: Bearer" requests (CLI), which the     │
   │  app validates itself                                  │
   ▼  plain HTTP/1.1 → 127.0.0.1:8080                       ▼
┌────────────────────────────────────────────────────────────────┐
│ speedtest serve (single Go binary)                             │
│                                                                │
│ UI (embedded dist/)   /api/config  /api/session  /api/ip       │
│ /api/ws (latency + upload stats)   /api/rtc/offer              │
│ /api/down  /api/up                                             │
│                                                                │
│ auth check (Remote-User from Authelia, or bearer token)        │
│ session store ── atomic counters ── janitor                    │
│ pion WebRTC (ICE-lite, single UDP port mux)                    │
└────────────────────────────────────────────────────────────────┘

Browser
  main thread : UI, gauge rendering (rAF), WebRTC loss test, orchestration
  worker(s)   : download / upload streams (2–4 workers)
  worker      : latency (WebSocket: ping/pong + server upload stats)
```

### 3.1 Hostnames and DNS

| Hostname | DNS records | Purpose | ALPN |
|---|---|---|---|
| `speed.example.com` | A + AAAA | UI, session, WebSocket, WebRTC signaling, `/api/ip` | h2, http/1.1 |
| `s1.speed.example.com` … `s8.speed.example.com` | A + AAAA | Download/upload streams | **http/1.1 only** |

TLS cert: one wildcard `*.speed.example.com` + `speed.example.com` from Let's Encrypt via **DNS-01** challenge (a wildcard requires DNS-01).

All hostnames sit behind Authelia. Authelia's session cookie domain must be a parent of all of them (e.g. `example.com`), so the login cookie is also sent to `s1…s8` (§5.9).

The number of test hosts is configurable. With N connections, use `hostsNeeded = ceil(N / 4)` hosts, so that no host gets more than 4 connections. That leaves headroom under Chrome's 6-per-host limit.

---

## 4. Repository layout

```
speedtest/
├── cmd/speedtest/main.go          # subcommands: serve | client | version
├── internal/
│   ├── config/                    # env + flags parsing, validation, defaults
│   ├── server/                    # http.Server, mux, middleware (CORS, security headers, logging)
│   │   ├── server.go
│   │   ├── routes.go
│   │   ├── cors.go
│   │   ├── clientip.go            # trusted-proxy aware client IP extraction
│   │   └── auth.go                # Remote-User / bearer token check
│   ├── session/                   # session store, counters, limits, janitor
│   ├── transfer/                  # /api/down, /api/up, random buffer
│   ├── control/                   # /api/ws WebSocket (ping/pong, stats push)
│   ├── rtc/                       # pion WebRTC: signaling, loss/jitter test
│   ├── stats/                     # median, p90, jitter, steady-window throughput, RFC3550 jitter
│   └── client/                    # CLI client engine (latency, down, up, loss)
├── web/
│   ├── embed.go                   # package web; //go:embed all:dist
│   ├── index.html
│   ├── src/
│   │   ├── main.ts
│   │   ├── engine/
│   │   │   ├── protocol.ts        # shared types: API responses, WS messages, worker messages, Result
│   │   │   ├── api.ts             # config, session, ip detection
│   │   │   ├── orchestrator.ts    # phase state machine
│   │   │   ├── timeline.ts        # cumulative byte samples → rates
│   │   │   ├── stats.ts           # mirrors internal/stats (same vectors)
│   │   │   ├── transfer.worker.ts # download/upload streams
│   │   │   ├── latency.worker.ts  # WebSocket ping/pong + stats relay
│   │   │   └── loss.ts            # WebRTC loss/jitter test (main thread)
│   │   ├── ui/
│   │   │   ├── gauge.ts           # canvas semicircle gauge
│   │   │   ├── sparkline.ts       # canvas live area chart
│   │   │   ├── tiles.ts           # result tiles
│   │   │   ├── controls.ts        # start/stop, single/multi, advanced
│   │   │   ├── ipinfo.ts
│   │   │   ├── theme.ts
│   │   │   └── format.ts          # unit formatting
│   │   └── styles.css
│   ├── tests/                     # vitest unit tests, playwright e2e
│   ├── package.json
│   ├── tsconfig.json
│   └── vite.config.ts
├── testdata/stats-vectors.json    # shared Go/TS test vectors for stats functions
├── deploy/
│   ├── traefik/traefik.yml        # static config
│   ├── traefik/dynamic/speedtest.yml
│   ├── docker-compose.yml
│   ├── sysctl/99-speedtest.conf
│   └── systemd/speedtest.service
├── docs/
│   ├── DECISIONS.md
│   ├── DEPLOY.md
│   └── PERFORMANCE.md             # 10G validation report (M7)
├── .github/workflows/ci.yml
├── Dockerfile
├── Makefile
└── README.md
```

Note: `go:embed` can't reference parent directories, so `web/embed.go` (package `web`) embeds `web/dist`, and `internal/server` imports `web.FS`. The frontend build must run before `go build`. The Makefile enforces this.

---

## 5. Server specification

### 5.1 Subcommands

```
speedtest serve   [flags]      # run server
speedtest client  [flags]      # CLI client (§10)
speedtest version
```

### 5.2 Configuration

Flags override environment variables. Every flag has an env equivalent (`--max-connections` ↔ `SPEEDTEST_MAX_CONNECTIONS`). Sizes accept `KiB/MiB/GiB` suffixes. Durations use Go syntax.

| Env | Default | Meaning |
|---|---|---|
| `SPEEDTEST_LISTEN` | `127.0.0.1:8080` | HTTP listen address (Traefik → here) |
| `SPEEDTEST_PUBLIC_URL` | *(required in prod)* | UI origin, e.g. `https://speed.example.com`. Used for the CORS allowlist and CSP |
| `SPEEDTEST_TEST_HOSTS` | *(empty)* | Comma-separated origins `https://s1.speed.example.com,...`. **Empty means single-origin mode**: test streams go to `PUBLIC_URL`, and the client caps connections at 6 and shows a notice |
| `SPEEDTEST_API_TOKEN` | *(empty)* | Bearer token for the CLI (§5.9). Empty disables bearer access |
| `SPEEDTEST_AUTH_HEADER` | `Remote-User` | Header Authelia sets via Traefik `authResponseHeaders`. Requests without a valid bearer token must carry it |
| `SPEEDTEST_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | CIDRs whose `X-Forwarded-For` is trusted |
| `SPEEDTEST_SERVER_NAME` | hostname | Shown in UI |
| `SPEEDTEST_MAX_ACTIVE_SESSIONS` | `1` | Concurrent tests. At the limit, creating a session cancels the oldest one ("newest wins"), so a stale tab never blocks you |
| `SPEEDTEST_MAX_CONNECTIONS` | `32` | Max streams per session per direction |
| `SPEEDTEST_MAX_REQUEST_DURATION` | `35s` | Hard deadline per down/up request |
| `SPEEDTEST_MAX_DOWNLOAD_BYTES` | `4GiB` | Max `bytes` per download request |
| `SPEEDTEST_MAX_UPLOAD_BYTES` | `512MiB` | Max body per upload request |
| `SPEEDTEST_SESSION_TTL` | `120s` | Idle expiry (no requests and no WS) |
| `SPEEDTEST_SESSION_MAX_AGE` | `5m` | Hard expiry |
| `SPEEDTEST_RTC_ENABLED` | `true` | Enable packet-loss test |
| `SPEEDTEST_RTC_UDP_PORT` | `50000` | Single UDP port for all WebRTC traffic |
| `SPEEDTEST_RTC_PUBLIC_IPS` | *(empty)* | Public IPs to advertise as ICE host candidates (needed if the server is behind NAT) |
| `SPEEDTEST_LOG_LEVEL` | `info` | slog level |
| `SPEEDTEST_LOG_FORMAT` | `json` | `json` or `text` |
| `SPEEDTEST_METRICS_LISTEN` | *(empty)* | e.g. `127.0.0.1:9090` to expose `/metrics`. Empty means off |

Validate at startup: URLs parse, CIDRs parse, limits > 0. Fail fast with a clear message.

### 5.3 Dependencies (allowed)

- `github.com/coder/websocket`: WebSocket
- `github.com/pion/webrtc/v4`: WebRTC

Everything else comes from the standard library: `net/http` (Go 1.22+ pattern routing), `log/slog`, `crypto/rand`, `sync/atomic`, `embed`.

### 5.4 HTTP server setup

- `http.Server{ReadHeaderTimeout: 10s, IdleTimeout: 120s, MaxHeaderBytes: 16 KiB}`.
- **Don't** set a global `WriteTimeout` or `ReadTimeout` (they would kill long transfers). Use per-request deadlines via `http.NewResponseController(w).SetWriteDeadline/SetReadDeadline` = now + `MAX_REQUEST_DURATION`.
- Graceful shutdown on SIGINT/SIGTERM: stop accepting connections, cancel all session contexts, `Shutdown` with a 10 s timeout.
- Never compress responses. No compression middleware anywhere.

### 5.5 Endpoints

All `/api/*` responses carry `Cache-Control: no-store`. JSON responses use `Content-Type: application/json`.

#### `GET /healthz`
`200 ok` (text). No CORS, no logging.

#### `GET /api/config` (main host)
```json
{
  "serverName": "ams-1",
  "version": "0.1.0",
  "testHosts": ["https://s1.speed.example.com", "..."],
  "limits": { "maxConnections": 32, "maxDownloadBytes": 4294967296, "maxUploadBytes": 536870912 },
  "rtc": { "enabled": true },
  "singleOrigin": false
}
```

#### `GET /api/ip` (main host, same-origin)
```json
{ "ip": "2001:db8::7", "family": "ipv6" }
```
- The client IP comes from §5.7. IPv4-mapped IPv6 addresses (`::ffff:a.b.c.d`) are unmapped and reported as `ipv4`.

#### `POST /api/session` (main host, same-origin)
Request: `{"connections": 8}` (1…`MAX_CONNECTIONS`).
Response `201`:
```json
{
  "sid": "b3f1c2…(32 hex chars, 128-bit crypto/rand)",
  "expiresInSec": 120,
  "testHosts": ["https://s1.speed.example.com", "https://s2.speed.example.com"],
  "connections": 8
}
```
- `testHosts` is the subset the client should use: `ceil(connections/4)` hosts, or `[PUBLIC_URL]` in single-origin mode (connections clamped to 6).
- If `MAX_ACTIVE_SESSIONS` is reached, the oldest session is cancelled (its streams stop) and the new one is created.
- Errors: `400` bad input; `401` not authenticated (§5.9). Body `{"error":"code","message":"…"}`.

#### `GET /api/down?sid=…&bytes=N` (test hosts; CORS)
- Validates sid (`410` unknown/expired; deliberately not `401`, which means "not authenticated"), `1 ≤ bytes ≤ MAX_DOWNLOAD_BYTES` (`413` otherwise), and session stream limit (`429` if the session already has `maxConnections+2` active down streams).
- Response headers: `200`, `Content-Type: application/octet-stream`, `Content-Length: N`, `Cache-Control: no-store`.
- Body: random bytes (§5.6) until N bytes are written, the client disconnects, the request deadline passes, or the session is cancelled.
- After each `Write`, add the bytes to `session.downBytes` (used for logging/metrics, not for the client's download figure).

#### `POST /api/up?sid=…` (test hosts; CORS)
- Same sid and stream checks. Body capped with `http.MaxBytesReader(MAX_UPLOAD_BYTES)`.
- Read loop with a pooled 256 KiB buffer (`sync.Pool`). **After every `Read`**, `session.upBytes.Add(n)`. This is what makes live upload stats possible.
- **Don't** use `io.Copy(io.Discard, body)`: `io.Discard`'s `ReadFrom` uses small 8 KiB reads.
- Response `200`: `{"bytes": n, "durationMs": d}`.

#### CORS (test hosts)
The UI runs on `speed.example.com` and the streams go to `s1…s8`. Those are different origins, so the browser applies CORS. Authelia's cookie must travel with every request, which makes these **credentialed** CORS requests:
- `/api/down` and `/api/up` respond with `Access-Control-Allow-Origin: <PUBLIC_URL>` (exact origin, never `*`, which is invalid with credentials), `Access-Control-Allow-Credentials: true`, and `Vary: Origin`. Requests from any other `Origin` get no CORS headers.
- **There must be no preflight requests.** Browsers send preflight `OPTIONS` requests without cookies, so Authelia rejects them and the real request never runs. Every transfer request must stay a CORS "simple request":
  - Download: plain `GET`, no custom headers.
  - Upload: `POST` with a Blob of **empty type**, so no `Content-Type` header is sent. No custom headers.
  - The session ID travels in the query string, never in a header.
- The server still answers `OPTIONS` correctly (204, `Access-Control-Allow-Methods: GET, POST`, `Access-Control-Max-Age: 7200`) in case a request accidentally becomes non-simple during development. The e2e test (§13.5) asserts that no `OPTIONS` requests occur.

#### `GET /api/ws?sid=…` (main host, WebSocket)
JSON text messages.

Client → server:
```json
{ "type": "ping", "id": 17 }
```
Server → client:
```json
{ "type": "pong", "id": 17 }
{ "type": "stats", "t": 1234567890, "up": 987654321, "down": 123456789, "streams": { "up": 8, "down": 0 } }
{ "type": "error", "code": "session_expired", "message": "…" }
```
- `pong` is sent **immediately** on receiving `ping` (no batching).
- `stats` is pushed every **100 ms** by a ticker goroutine while the WS is open. `t` = server monotonic nanoseconds since session start (`time.Since(sess.start)`). `up`/`down` are **cumulative** byte counters.
- `coder/websocket` allows concurrent writes, but keep the pong path free of any locks shared with the stats ticker.
- Closing the WS marks the session as finishing. It expires `10 s` later unless new requests arrive.

#### `POST /api/rtc/offer?sid=…` (main host)
Request `{"sdp": "<offer>"}` → response `{"sdp": "<answer>"}`. Non-trickle ICE: the client waits for ICE gathering to finish before sending, and the server waits for `GatheringCompletePromise` before answering. Details in §8.

### 5.6 Random payload

- At startup, fill a **4 MiB** buffer from `crypto/rand`. All download writes come from this shared, read-only buffer.
- Each download request starts at a random offset (`rand.IntN(len)`) and wraps around. Write in slices of up to 1 MiB so `w.Write` bypasses the 4 KiB `bufio` buffer in `net/http` (large writes go straight to the connection).
- No per-request allocations in the hot loop.

### 5.7 Client IP extraction (`clientip.go`)

```
remote := RemoteAddr host
if remote ∉ TRUSTED_PROXIES: return remote
chain := X-Forwarded-For entries (all headers, comma-split, trimmed), left→right
walk chain right→left; return the first entry NOT in TRUSTED_PROXIES
if none: return leftmost valid entry, else remote
```
- Parse with `netip`. Unmap IPv4-mapped addresses. Ignore invalid entries.
- Table-driven tests: untrusted remote with a spoofed XFF (must return remote), trusted single hop, multiple hops, IPv6, mapped addresses, garbage values.

### 5.8 Sessions (`internal/session`)

```go
type Session struct {
    ID          string
    ClientIP    netip.Addr
    Connections int
    Start       time.Time        // monotonic
    lastSeen    atomic.Int64     // unix nanos
    UpBytes     atomic.Int64
    DownBytes   atomic.Int64
    ActiveUp    atomic.Int32
    ActiveDown  atomic.Int32
    ctx         context.Context  // cancelled on expiry/shutdown
    cancel      context.CancelFunc
}
```
- Store: `map[string]*Session` + `sync.RWMutex` (lookups are hot, so use `RLock`).
- Active-session limit `MAX_ACTIVE_SESSIONS`: when creating a session at the limit, cancel the oldest active session first (newest wins).
- Janitor goroutine every 5 s: expire on idle TTL, max age, or 10 s after WS close. On expiry: `cancel()`, log a summary line (`sid`, client IP, `Remote-User` or `token`, `down_bytes`, `up_bytes`, `duration`, `connections`).
- Handlers derive their context from **both** the request context and the session context, so expiry stops transfers.

### 5.9 Authentication

Authelia does the real authentication in Traefik. The app adds a small check, so a Traefik misconfiguration can't silently expose it.

Middleware on the UI and every `/api/*` route (not `/healthz`):
```
if request has "Authorization: Bearer <t>":
    allow iff API_TOKEN != "" and constant-time-equal(t, API_TOKEN)   // CLI path (Authelia skipped)
else:
    allow iff header AUTH_HEADER (default Remote-User) is non-empty    // browser path (set by Authelia)
deny → 401 {"error":"unauthorized"}
```
- Traefik `forwardAuth` with `authResponseHeaders: [Remote-User, …]` **overwrites** any `Remote-User` the client sends, so the header can't be spoofed on the Authelia routers. The bearer-token routers strip `Remote-User` with a headers middleware (§12.2) for the same reason.
- **Authelia session cookie**: its domain must be a parent of all hostnames (e.g. `example.com`), so the cookie also reaches `s1…s8`. These hosts are all the same *site*, so the cookie is sent even with Authelia's default `SameSite=Lax`, as long as the browser uses `credentials: 'include'` (§7.3, §7.4).
- The WebSocket upgrade carries the cookie (same host), so it's authenticated once, at connect time.
- Packet loss (UDP 50000) bypasses Traefik and Authelia. That's safe: a peer can only connect with the ICE credentials and DTLS fingerprint from the authenticated `/api/rtc/offer` exchange.
- `--dev` disables this check (local development without Traefik) and logs a loud warning.

### 5.10 Static UI serving and security headers

- Serve `web.FS` (`dist/`). `index.html`: `Cache-Control: no-cache`. Hashed assets (`/assets/*`): `Cache-Control: public, max-age=31536000, immutable`.
- Headers on UI responses:
  - `Content-Security-Policy: default-src 'self'; connect-src 'self' <testHosts> wss://<public host>; worker-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'`. Build the string from config at startup.
  - `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Permissions-Policy: interest-cohort=()` (optional).
- Optional (stretch): `/metrics` on `METRICS_LISTEN` in Prometheus text format, hand-written, no dependency. Expose active sessions, bytes up/down totals, sessions created, and sessions preempted.

---

## 6. Measurement methodology (normative)

Implement this **identically** in `internal/stats` (Go) and `web/src/engine/stats.ts` (TS), verified by `testdata/stats-vectors.json`.

### 6.1 Phases and defaults

| Phase | Default duration | What runs |
|---|---|---|
| 0. Init (on page load) | – | `GET /api/config`; `GET /api/ip` (§9) in parallel |
| 1. Latency (+ loss) | 2 s, or 6 s if loss test enabled | WS pings every 100 ms; WebRTC loss test in parallel (both are tiny and don't interfere) |
| 2. Download | 12 s | N download streams + WS pings every 250 ms (loaded latency) |
| 3. Upload | 12 s | N upload streams + WS pings every 250 ms + server stats |
| 4. Done | – | Compute results, render summary |

User-selectable duration per direction: **6 / 12 / 20 s**. Grace (warm-up) period: **2 s** at the start of each transfer phase (discarded for the final result, still shown live).

### 6.2 Timeline

Throughput phases record a timeline of `{t: ms since phase start, bytes: cumulative}` samples every **100 ms** (main thread, `performance.now()`).

- **Download**: cumulative bytes = sum of worker progress deltas received so far. Timestamp on receipt in the main thread. (Don't use worker-side `performance.now()`: workers have a different `timeOrigin`.)
- **Upload**: cumulative bytes = server `stats.up` minus the value at phase start. Use the **server `t`** for time deltas. Use client receipt time only to decide whether a sample falls inside the steady window.

### 6.3 Definitions

- **Live rate** (gauge): `(bytes(now) − bytes(now − 500 ms)) × 8 / 0.5` bit/s, with linear interpolation between samples.
- **Final throughput**: steady window `[grace, end]`: `(bytes(end) − bytes(grace)) × 8 / (end − grace)`. If the steady window is shorter than 1 s (aborted test), use `[0, end]` and set `"short": true`.
- **Peak (p90)**: in the steady window, compute 1 s rolling-window rates sampled every 100 ms, then take the nearest-rank p90. Stored in results; shown as secondary text.
- **Median**: sort, take the middle element (average of the two middles for an even count).
- **p90**: nearest-rank, `ceil(0.9 × n)`-th smallest.
- **Idle latency**: all ping RTTs in phase 1 **except the first 3** (warm-up). Report median, min, jitter, sample count.
- **Loaded latency**: ping RTTs during phases 2 and 3 **after the grace period**. Report median and p90 per phase.
- **Jitter (TCP / WS)**: mean of `|rtt[i] − rtt[i−1]|` over consecutive samples.
- **UDP jitter (WebRTC)**: RFC 3550 interarrival jitter: for each consecutive pair, `D = (Rj − Ri) − (Sj − Si)`, `J += (|D| − J) / 16`. Sender clock offset cancels out. Reported per direction in ms.
- **Packet loss**: `1 − received / sent` per direction, as a percentage with 2 decimals.

### 6.4 Live gauge smoothing

The gauge needle/arc position eases toward the target in **gauge-position space** (after log mapping): `pos += (target − pos) × (1 − exp(−dt / 150 ms))`. The text readout updates at most 10×/s. With `prefers-reduced-motion`, skip easing and update at 4 Hz.

---

## 7. Client test engine (browser)

### 7.1 Orchestrator (state machine)

```
idle → starting → latency → download → upload → done
            ↘ error (any phase) → idle
any running state —[user Stop]→ aborting → idle (partial results discarded)
```
- On Start: `POST /api/session` → open the latency worker (WS) → (optional) WebRTC loss test → phases in order.
- **Expired Authelia login**: call `POST /api/session` with `redirect: 'manual'`. If the response type is `opaqueredirect` or the status is `401`, the login has expired: show "Signed out. Reload to sign in again" with a Reload button. A transfer request that fails with a `TypeError` before receiving any data is treated the same way (Authelia's redirect has no CORS headers, so it surfaces as a network error).
- If `document.visibilityState` becomes `hidden` during a test, record `"tabHidden": true` in results and show a small warning on the result ("Tab was in background; results may be inaccurate").

### 7.2 Workers

- **Transfer workers**: `W = clamp(ceil(N / 2), 1, 4)`, further capped at `navigator.hardwareConcurrency / 2` (min 1). Streams are distributed round-robin across workers. Stream `i` uses `testHosts[i % testHosts.length]`.
- **Latency worker**: owns the WebSocket. Sends pings at the interval set by the main thread, measures RTT with `performance.now()` inside the worker (a worker-local clock is fine for RTT), and relays `{rtt}` and server `stats` messages to the main thread.

Worker message protocol (in `protocol.ts`):
```ts
// main → transfer worker
{ type: 'start', dir: 'down' | 'up', sid: string, urls: string[], streams: number, downBytes: number, upStartSize: number, upMaxSize: number }
{ type: 'stop' }
// transfer worker → main
{ type: 'progress', bytes: number }          // delta since last progress, posted every 50 ms
{ type: 'error', message: string }
// main → latency worker
{ type: 'open', wsUrl: string } | { type: 'interval', ms: number } | { type: 'close' }
// latency worker → main
{ type: 'rtt', ms: number } | { type: 'stats', t: number, up: number, down: number } | { type: 'ws-error', message: string }
```

### 7.3 Download stream loop (per stream, in worker)

```ts
while (!stopped) {
  const res = await fetch(`${url}/api/down?sid=${sid}&bytes=${downBytes}`, { cache: 'no-store', credentials: 'include', signal });
  const reader = res.body!.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    pending += value.byteLength;           // flushed as 'progress' every 50 ms
  }
}
```
- `downBytes` default **2 GiB** (requests restart automatically if they finish early; a restart costs one RTT).
- Stop: `AbortController.abort()` on all streams. Swallow `AbortError`.
- Optimization (only if profiling shows GC pressure): BYOB reader `getReader({ mode: 'byob' })` with feature detection and a fallback.

### 7.4 Upload stream loop (per stream, in worker)

- Build a **1 MiB** random `Uint8Array` once (`crypto.getRandomValues` in 64 KiB slices: the API limit is 65 536 bytes per call).
- Blobs are built lazily and shared across all streams: `new Blob(Array(k).fill(chunk))` for sizes 16, 32, 64, 128, 256 MiB. Use **no `type`** (keeps it a CORS simple request, so there's no preflight; see §5.5).
- Adaptive size: start at **16 MiB**. After each completed request, if it took < 1 s, move up to the next size (max `upMaxSize`, default **256 MiB**, and never above the server's `maxUploadBytes`).
- Loop: `fetch(\`${url}/api/up?sid=${sid}\`, { method: 'POST', body: blob, credentials: 'include', signal })` until stopped.
- Fallback: if WS stats are unavailable, estimate upload from completed request bytes (`"approx": true` in results).
- **Don't** use fetch streaming request bodies (`duplex: 'half'`). They need HTTP/2, and our test hosts are HTTP/1.1.

### 7.5 Results object (shared by UI "copy JSON" and `speedtest client --json`)

```json
{
  "version": 1,
  "timestamp": "2026-10-17T12:00:00Z",
  "server": { "name": "ams-1", "url": "https://speed.example.com", "version": "0.1.0" },
  "client": { "ip": "2001:db8::7", "family": "ipv6", "userAgent": "…" },
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

"Copy results" provides plain text (human summary) and JSON (above).

---

## 8. Packet loss and jitter (WebRTC)

### 8.1 Server (pion)

- `SettingEngine`:
  - `SetICEUDPMux(webrtc.NewICEUDPMux(nil, udpConn))` with a single `net.ListenUDP` on `RTC_UDP_PORT` (listen on `[::]` for dual-stack, or one socket per family if needed).
  - `SetLite(true)` (ICE-lite: the server has a public IP and the browser initiates connectivity checks).
  - `SetNAT1To1IPs(RTC_PUBLIC_IPS, webrtc.ICECandidateTypeHost)` when configured.
  - `SetNetworkTypes(udp4, udp6)`.
- No STUN/TURN needed on the server. Client `iceServers: []` is fine because the server is publicly reachable. (Optionally allow a configured STUN URL to be passed to the client; stretch.)
- Max concurrent peer connections = `MAX_ACTIVE_SESSIONS`. Each PC has a 20 s hard timeout and closes when the session expires.

### 8.2 Channels

The client creates both channels before the offer:
- `ctl`: reliable, ordered, JSON text messages.
- `probe`: `{ ordered: false, maxRetransmits: 0 }`, binary.

Probe packet (binary, big-endian), default size **200 bytes** (padded):
```
offset 0  u8   kind      1 = down probe (server→client), 2 = up probe (client→server)
offset 1  u32  seq       starts at 0
offset 5  u64  sendUs    sender clock, microseconds
offset 13 …    padding   zeros to configured size
```

Control flow on `ctl`:
```
client → {"type":"start","pps":100,"durationMs":6000,"size":200}
          (server starts sending down probes at pps; client starts sending up probes at pps)
client → {"type":"end","sent":600}            // after durationMs + 0 ms
server → {"type":"report","sentDown":600,"receivedUp":599,"jitterUpMs":0.5,"reorderedUp":0}
          // server waits 1 s grace for stragglers before replying
```
- The client computes down loss, down jitter and reordering from the probes it received. It must also wait 1 s of grace after the last expected packet.
- Sending is paced with a ticker (server: `time.Ticker`; client: `setInterval` 10 ms sending `pps/100` packets per tick). Pacing precision isn't critical; counts are.
- Defaults: 100 pps × 6 s = 600 packets per direction, giving a resolution of about 0.17 %.

### 8.3 Failure handling

- If ICE doesn't connect within 5 s (UDP blocked, corporate firewall), show the loss tile as "Unavailable (UDP blocked)" with `available: false`. The rest of the test continues normally.
- Don't use an ICE-TCP fallback (TCP can't measure loss).

---

## 9. IP display

At page load: `GET /api/ip` on the main host (`cache: 'no-store'`). Show the address with a small family badge (`IPv4` / `IPv6`). This is the address the browser actually connected with, after Happy Eyeballs picked a family.

Dual-stack detection was dropped on purpose. If it's ever wanted: add `v4.` (A record only) and `v6.` (AAAA record only) hostnames behind the same Authelia setup, and call `/api/ip` on each from the UI, using credentialed CORS with the same rules as §5.5.

---

## 10. CLI client (`speedtest client`)

The CLI client is a **first-class deliverable**, not just a test tool. It serves three purposes:
1. **Verify server capacity** beyond browser limits (no 6-connections-per-host cap, no JS overhead). This is the reference measurement for the 10G validation.
2. **Headless and scripted use**: cron jobs, monitoring, servers without a browser, CI.
3. **Debugging**: compare browser vs CLI results to tell whether a slow result comes from the network or from the browser.

### 10.1 Distribution

- Ships inside the same binary as the server: `speedtest client …`.
- CI (M7) also publishes **standalone release archives** for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`. These are the same binary; users who only want the client just run `speedtest client`. Use GoReleaser or a plain `go build` matrix plus checksums (`SHA256SUMS`).
- The Docker image works too: `docker run --rm ghcr.io/OWNER/speedtest client --server https://speed.example.com`.
- The client must not depend on the embedded web UI at runtime (it's present but unused).

### 10.2 Usage and flags

```
speedtest client --server https://speed.example.com [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `--server, -s` | `$SPEEDTEST_SERVER` (required if unset) | Server base URL (the `PUBLIC_URL` of the server) |
| `--token` | `$SPEEDTEST_TOKEN` | Bearer token (matches the server's `SPEEDTEST_API_TOKEN`). Sent as `Authorization: Bearer …` on every request, including the WebSocket upgrade and `/api/rtc/offer`. Prefer the env var so the token stays out of shell history |
| `--connections, -n` | `8` | Streams per direction. `1` = single-connection mode |
| `--duration, -d` | `12s` | Duration per direction |
| `--grace` | `2s` | Warm-up excluded from the final result |
| `--only` | *(all)* | Comma list of `latency,down,up,loss` |
| `--no-loss` | `false` | Skip the packet-loss test |
| `-4` / `-6` | *(off)* | Force IPv4 / IPv6 for all connections (custom `net.Dialer` with `tcp4`/`tcp6`, and `udp4`/`udp6` for WebRTC) |
| `--direct` | `false` | Ignore `testHosts` and connect all streams to `--server` (the CLI has no per-host limit, so this is the simplest mode) |
| `--json` | `false` | Print only the result JSON (§7.5) to stdout |
| `--format` | `human` | `human` \| `json` \| `csv` (one line, for appending to a log file; header via `--csv-header`) |
| `--quiet, -q` | `false` | No live progress; print the summary only |
| `--interval` | `0` | Repeat the full test every interval (e.g. `1h`); each run prints one result. `0` = run once |
| `--timeout` | `90s` | Overall timeout for one run |
| `--insecure` | `false` | Skip TLS verification (for self-signed lab setups; prints a warning) |
| `--ca-file` | *(system)* | Extra CA bundle |
| `--version` | | Print version |

### 10.3 Behavior

- Same API flow as the browser: `GET /api/config` → `POST /api/session` → WS `/api/ws` → phases (§6.1) → result. Results use the §7.5 JSON schema with `client.userAgent = "speedtest-cli/<version>"`. `client.ip` and `client.family` come from `/api/ip` (honoring `-4`/`-6`).
- **Auth**: every request carries the bearer token. Redirects are never followed (`CheckRedirect` returns `http.ErrUseLastResponse`): a `3xx` (Authelia login redirect, meaning the request didn't match the bearer router) or a `401` (wrong token) fails fast with a clear message and exit code 3.
- **One `http.Transport` per stream** with `ForceAttemptHTTP2: false`, `TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}` (a non-nil empty map disables h2), `DisableCompression: true`, `MaxConnsPerHost: 1`, `ReadBufferSize`/`WriteBufferSize: 256 KiB`. This guarantees N real TCP connections.
- **Download**: N goroutines, each looping `GET /api/down?bytes=…` and reading into its own pooled 1 MiB buffer, adding to a shared `atomic.Int64`. A 100 ms ticker goroutine records the timeline (§6.2).
- **Upload**: N goroutines, each looping `POST /api/up` with an `io.Reader` that serves bytes from the shared 4 MiB random buffer up to `maxUploadBytes` per request. No allocation; `ContentLength` set explicitly. Server WS `stats` are the source of truth, as in the browser.
- **Latency**: WS ping/pong with the same schedule as the browser (§6.1). RTT measured with Go's monotonic clock.
- **Loss / jitter**: pion WebRTC client implementing the same `ctl`/`probe` protocol (§8.2). Implemented in M6; until then `--no-loss` is implied.
- **Stats**: uses `internal/stats`, so the methodology is identical to the browser (shared vectors, §13.1).
- **Cancellation**: Ctrl-C cancels the root context. All streams stop, the WS closes, partial results are printed with `"aborted": true` in `flags`, and the exit code is 130.

### 10.4 Output

Human mode (TTY), live progress on one line, updated 4×/s with carriage return:
```
Server   ams-1 (https://speed.example.com) · v0.1.0
Client   2001:db8::7 (IPv6)
Mode     8 connections · 12 s per direction

Latency     4.1 ms   (min 3.8, jitter 0.3, 17 samples)
Download    9.41 Gbps   ▕████████████████████▏ loaded latency 18.2 ms
Upload      9.12 Gbps   ▕███████████████████▌▏ loaded latency 9.0 ms
Packet loss ↓ 0.00 %  ↑ 0.17 %   (UDP jitter ↓ 0.4 ms ↑ 0.5 ms)
```
- If stdout is not a TTY: no carriage-return progress (behave like `--quiet`).
- No color if `NO_COLOR` is set or output is not a TTY.
- `--json` / `--format json`: exactly one JSON object per run on stdout (NDJSON when combined with `--interval`). Logs and warnings go to **stderr** only.

### 10.5 Exit codes

| Code | Meaning |
|---|---|
| `0` | Test completed |
| `1` | Usage or config error |
| `2` | Server unreachable or TLS error |
| `3` | Authentication failed: `401` (wrong or missing token) or a redirect to the Authelia login |
| `4` | Test ran but a phase failed (e.g. all streams errored) |
| `130` | Interrupted (Ctrl-C) |

Packet loss being unavailable (UDP blocked) is **not** an error: `loss.available=false` and exit 0.

### 10.6 Package structure

```
internal/client/
  client.go      // Run(ctx, Options) (Result, error): orchestrates phases
  api.go         // config/session/ip calls
  transport.go   // per-stream http.Transport factory (-4/-6, TLS opts)
  download.go
  upload.go
  latency.go     // WS ping/pong + stats subscription
  loss.go        // pion client (M6)
  output.go      // human / json / csv renderers, progress line
cmd/speedtest/client.go  // flag parsing → client.Run → output → exit code
```
`client.Run` is a library function with no direct printing (progress via a callback), so integration tests and future tools can reuse it.

### 10.7 Tests

- Unit: flag parsing, output renderers (golden files for human/json/csv), exit-code mapping, upload reader (exact byte count, no allocation in a benchmark: `testing.AllocsPerRun == 0`).
- Integration (§13.2): runs against the in-process server with `-n 1` and `-n 8`. Also a `-4` run, and a `-6` run when the test environment has IPv6 loopback.
- Auth: no token → exit 3; wrong token → exit 3; valid token → success.
- Verify real connection count: during an `-n 8` run, the server observes 8 distinct remote ports on `/api/down`.

---

## 11. UI / design specification

### 11.1 Layout (desktop ≥ 900 px)

```
┌───────────────────────────────────────────────────────────────────┐
│  ◉ Speedtest                                   ams-1 · v0.1   ◐   │  header: name, server, theme toggle
│                                                                   │
│  Your IP  2001:db8::7  [IPv6]                                     │  IP strip
│                                                                   │
│                    ╭────────────────────────╮                     │
│                ╭──╯                          ╰──╮                  │
│              ╭╯           9.41                 ╰╮                 │  main semicircle gauge (canvas)
│             ╱             Gbps                   ╲                │  big tabular numeral (DOM overlay)
│            │            ↓ DOWNLOAD                │               │  phase label
│            0   1    10    100    1G    10G                        │  log ticks
│                       [  ▶  START  ]                              │  primary button (becomes STOP)
│                                                                   │
│     ▁▂▃▅▆▇▇█▇▇▇█▇▇▇▇▇▇▇█▇▇▇▇▇  (live sparkline of current phase)    │
│                                                                   │
│  ┌ ↓ Download ──────┐ ┌ ↑ Upload ────────┐ ┌ Ping ─┐┌ Jitter┐┌ Loss ┐│
│  │ 9.41 Gbps        │ │ 9.12 Gbps        │ │ 4.1ms ││0.3 ms ││0.00 %││  result tiles
│  │ ▁▃▅▇▇▇▇▇▇ spark  │ │ ▁▃▅▇▇▇▇▇▇ spark  │ └───────┘└───────┘└──────┘│
│  │ loaded 18 ms     │ │ loaded 9 ms      │                         │
│  └──────────────────┘ └──────────────────┘                         │
│                                                                   │
│      Connections  [ Single │ Multi ]        ⚙ Advanced ▾            │  controls
│      (Advanced: streams 2/4/8/16/32 · duration 6/12/20 s · ☑ packet loss)│
└───────────────────────────────────────────────────────────────────┘
```

Mobile (< 600 px): single column. Gauge at full width (max 420 px). Tiles in a 2-column grid (Download, Upload full-width rows, then Ping/Jitter/Loss in 3 compact columns). Controls stack. 16 px side gutters, no horizontal scroll.

### 11.2 Gauge

- **Canvas 2D**, scaled by `devicePixelRatio`. Redraw only while animating (rAF loop stops when idle). Pause when the tab is hidden.
- Arc sweep **240°** (constant `ARC_SWEEP`, opening at the bottom). Track: thin neutral arc. Value arc: thicker arc with a gradient along the arc in the phase color, plus a soft glow (`shadowBlur`) and a bright leading dot at the arc tip. No needle (cleaner look); the leading dot does that job.
- **Log scale** in Mbps: `pos(v) = log10(1 + v) / log10(1 + vMax)`, with `vMax = 10 000` (10 Gbps). Ticks at 0, 1, 10, 100, 1G, 10G (evenly spaced with this mapping). If the live value exceeds `vMax`, tween the scale to `vMax = 100 000` (ticks …, 10G, 100G) over 600 ms.
- Center readout: DOM element over the canvas (crisp text, accessible). Value with 3 significant digits and an auto unit (`kbps` < 1 Mbps, `Mbps` < 1000, else `Gbps`). `font-variant-numeric: tabular-nums`. Phase label underneath ("Latency", "↓ Download", "↑ Upload", "Done").
- Phase colors: download = `--accent-down`, upload = `--accent-up`. The arc color crossfades over 300 ms at the phase switch, and the arc animates back to 0 between phases.
- Latency phase: the arc shows an indeterminate gentle pulse, and the readout shows the live median ping in ms.
- Done state: the gauge shows the download result faintly, and the Start button becomes "Run again".

### 11.3 Sparklines

- Canvas area chart, last 20 s (or the phase length), y-axis auto-scaled to the max seen in the phase (linear), filled with a gradient in the phase color at 25 % opacity plus a 1.5 px line. No axes or labels.
- The main sparkline under the gauge shows the current phase. Each result tile keeps its own sparkline of the finished phase.

### 11.4 Tiles

- Download / Upload: main value (final), secondary line "loaded latency 18 ms", sparkline. While pending: em dash, muted.
- Ping (idle median), Jitter, Packet loss (shows "↓ 0.00 % ↑ 0.17 %", or "Unavailable" with a tooltip explaining UDP is blocked).
- Loaded-latency color hint: green if `loaded − idle` < 30 ms, amber < 100 ms, red otherwise (bufferbloat indicator), with a text label too (not color alone).

### 11.5 Controls

- Primary button: Start ↔ Stop (Stop aborts and resets).
- Segmented control **Single | Multi** (default Multi). Multi uses the "streams" value from Advanced (default 8; options filtered to ≤ `limits.maxConnections`; in single-origin mode capped at 6, with a hint).
- Advanced (collapsible `<details>`): streams, duration per direction, packet-loss toggle (hidden if `rtc.enabled=false`).
- Persist control choices and theme in `localStorage` (wrapped in try/catch; works without it).
- Controls are disabled while a test runs (except Stop).
- After done: "Copy results" button with a dropdown: Text / JSON.

### 11.6 Visual style

- Typography: system UI font stack (no web fonts, to keep it light). Numerals tabular.
- Design tokens as CSS custom properties on `:root`. Dark is the default look; light theme via `prefers-color-scheme` plus a manual toggle (`data-theme` on `<html>`).

| Token | Dark | Light |
|---|---|---|
| `--bg` | `#0b0f17` | `#f6f7fb` |
| `--surface` | `#121826` | `#ffffff` |
| `--border` | `#1f2937` | `#e5e7eb` |
| `--text` | `#e5e7eb` | `#0f172a` |
| `--text-muted` | `#94a3b8` | `#64748b` |
| `--accent-down` | `#22d3ee` | `#0891b2` |
| `--accent-up` | `#a78bfa` | `#7c3aed` |
| `--ok` | `#34d399` | `#059669` |
| `--warn` | `#fbbf24` | `#d97706` |
| `--bad` | `#f87171` | `#dc2626` |

- Surfaces: 16 px radius, 1 px border, subtle shadow in light mode only. Generous spacing (8 px grid).
- Motion: 150–300 ms ease-out transitions. Button press scale 0.98. Everything respects `prefers-reduced-motion`.

### 11.7 Accessibility

- All controls are keyboard operable, with visible focus rings. Segmented control as a radio group.
- Gauge canvas: `role="img"` with `aria-label` updated **at phase end only** ("Download 9.41 gigabits per second").
- An `aria-live="polite"` region announces phase changes and final results.
- Contrast WCAG AA for text in both themes. Status is never conveyed by color alone.

### 11.8 Performance budgets

- JS ≤ **40 KB gzipped** total (main + workers), CSS ≤ **10 KB gzipped**, zero runtime deps.
- No main-thread long tasks > 50 ms during a 10G test (check in the Chrome Performance panel).
- Gauge animation steady at display refresh rate during the test on a mid-range laptop.

---

## 12. Deployment

### 12.1 Traefik static config (`deploy/traefik/traefik.yml`)

```yaml
entryPoints:
  web:
    address: ":80"
    http:
      redirections:
        entryPoint: { to: websecure, scheme: https }
  websecure:
    address: ":443"
    # Traefik is the edge: do NOT trust incoming X-Forwarded-* (no trustedIPs).
    transport:
      respondingTimeouts:
        readTimeout: 90s     # v3 default is 60s; uploads are per-request < 35s but be explicit
        writeTimeout: 0s
        idleTimeout: 180s
    # NOTE: do not enable http3 on this entrypoint.

providers:
  file:
    directory: /etc/traefik/dynamic
    watch: true

certificatesResolvers:
  le:
    acme:
      email: admin@example.com
      storage: /acme/acme.json
      dnsChallenge:
        provider: cloudflare        # or your DNS provider; required for wildcard
        resolvers: ["1.1.1.1:53", "9.9.9.9:53"]

log:
  level: INFO
# accessLog: optional; if enabled, consider filtering to status codes >= 400
```

### 12.2 Traefik dynamic config (`deploy/traefik/dynamic/speedtest.yml`)

Requires Authelia ≥ 4.38 (the `/api/authz/forward-auth` endpoint).

```yaml
http:
  middlewares:
    authelia:
      forwardAuth:
        address: "http://127.0.0.1:9091/api/authz/forward-auth"   # point at your Authelia
        trustForwardHeader: true
        authResponseHeaders: [Remote-User, Remote-Groups, Remote-Email, Remote-Name]
    strip-auth-headers:
      headers:
        customRequestHeaders:        # an empty value removes the header
          Remote-User: ""
          Remote-Groups: ""
          Remote-Email: ""
          Remote-Name: ""

  routers:
    # ---- Browser path: Authelia on every hostname ----
    speedtest-ui:
      rule: 'Host(`speed.example.com`)'
      entryPoints: [websecure]
      middlewares: [authelia]
      service: speedtest
      tls:
        certResolver: le
        domains:
          - main: "speed.example.com"
            sans: ["*.speed.example.com"]

    speedtest-test:
      # IMPORTANT: explicit Host() matchers, NOT HostRegexp.
      # Traefik maps TLS options to SNI using the router's Host() domains;
      # with HostRegexp the http11only option would not apply.
      rule: >-
        Host(`s1.speed.example.com`) || Host(`s2.speed.example.com`) ||
        Host(`s3.speed.example.com`) || Host(`s4.speed.example.com`) ||
        Host(`s5.speed.example.com`) || Host(`s6.speed.example.com`) ||
        Host(`s7.speed.example.com`) || Host(`s8.speed.example.com`)
      entryPoints: [websecure]
      middlewares: [authelia]
      service: speedtest
      tls:
        certResolver: le
        options: http11only

    # ---- CLI path: bearer token skips Authelia; the app validates the token (§5.9) ----
    # Each mirrors the router above for the same hosts, with the SAME TLS options
    # (two routers for one host with different TLS options = conflict).
    speedtest-ui-token:
      rule: 'Host(`speed.example.com`) && HeaderRegexp(`Authorization`, `^Bearer .+`)'
      priority: 1000
      entryPoints: [websecure]
      middlewares: [strip-auth-headers]
      service: speedtest
      tls:
        certResolver: le

    speedtest-test-token:
      rule: >-
        (Host(`s1.speed.example.com`) || Host(`s2.speed.example.com`) ||
        Host(`s3.speed.example.com`) || Host(`s4.speed.example.com`) ||
        Host(`s5.speed.example.com`) || Host(`s6.speed.example.com`) ||
        Host(`s7.speed.example.com`) || Host(`s8.speed.example.com`)) &&
        HeaderRegexp(`Authorization`, `^Bearer .+`)
      priority: 1000
      entryPoints: [websecure]
      middlewares: [strip-auth-headers]
      service: speedtest
      tls:
        certResolver: le
        options: http11only

  services:
    speedtest:
      loadBalancer:
        passHostHeader: true
        servers:
          - url: "http://127.0.0.1:8080"
        # no buffering, no compress middleware, no retry middleware

tls:
  options:
    http11only:
      minVersion: VersionTLS12
      alpnProtocols:
        - http/1.1
      # If you ever switch to the TLS-ALPN-01 ACME challenge, add "acme-tls/1" here.
      # With DNS-01 (used here) it is not needed.
```

Authelia side (`configuration.yml`, relevant parts only; adapt to your existing setup):

```yaml
session:
  cookies:
    - domain: example.com                 # parent of speed.example.com AND s1..s8
      authelia_url: https://auth.example.com
access_control:
  default_policy: deny
  rules:
    - domain: ["speed.example.com", "*.speed.example.com"]
      policy: two_factor
      subject: ["user:YOUR_USER"]
```
Authelia's default inactivity timeout (5 min) is longer than a test run (~35 s). If the page sits idle past it, the UI shows the sign-in prompt (§7.1).

### 12.3 Docker Compose (`deploy/docker-compose.yml`)

Production uses **host networking** for both containers (lowest overhead at 10G, and no UDP NAT for WebRTC).

```yaml
services:
  traefik:
    image: traefik:v3.5          # pin to the current v3.x minor at implementation time
    network_mode: host
    restart: unless-stopped
    volumes:
      - ./traefik/traefik.yml:/etc/traefik/traefik.yml:ro
      - ./traefik/dynamic:/etc/traefik/dynamic:ro
      - ./acme:/acme
    environment:
      CF_DNS_API_TOKEN: ${CF_DNS_API_TOKEN}

  speedtest:
    image: ghcr.io/OWNER/speedtest:latest
    network_mode: host
    restart: unless-stopped
    ulimits:
      nofile: { soft: 65536, hard: 65536 }
    environment:
      SPEEDTEST_LISTEN: 127.0.0.1:8080
      SPEEDTEST_PUBLIC_URL: https://speed.example.com
      SPEEDTEST_TEST_HOSTS: https://s1.speed.example.com,https://s2.speed.example.com,https://s3.speed.example.com,https://s4.speed.example.com,https://s5.speed.example.com,https://s6.speed.example.com,https://s7.speed.example.com,https://s8.speed.example.com
      SPEEDTEST_API_TOKEN: ${SPEEDTEST_API_TOKEN}   # for the CLI; generate with: openssl rand -hex 32
      SPEEDTEST_SERVER_NAME: ams-1
      SPEEDTEST_RTC_UDP_PORT: "50000"
```

Authelia isn't in this compose file; it's assumed to run already as part of your setup. Point the `forwardAuth` address in §12.2 at it.

Firewall: allow TCP 80, 443 and **UDP 50000** in.

### 12.4 Dockerfile (multi-stage)

```dockerfile
FROM node:lts-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build                      # outputs web/dist

FROM golang:1-alpine AS build          # pin minor version at implementation time
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /speedtest ./cmd/speedtest

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/   # for `client` subcommand
COPY --from=build /speedtest /speedtest
USER 65534:65534
EXPOSE 8080 50000/udp
ENTRYPOINT ["/speedtest"]
CMD ["serve"]
```

Build multi-arch images (`linux/amd64`, `linux/arm64`) in CI.

### 12.5 Bare-metal alternative

`deploy/systemd/speedtest.service`: `ExecStart=/usr/local/bin/speedtest serve`, `EnvironmentFile=/etc/speedtest.env`, `LimitNOFILE=65536`, `DynamicUser=yes`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `Restart=on-failure`.

### 12.6 Host tuning (`deploy/sysctl/99-speedtest.conf`)

```
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.core.rmem_max = 268435456
net.core.wmem_max = 268435456
net.ipv4.tcp_rmem = 4096 131072 268435456
net.ipv4.tcp_wmem = 4096 65536 268435456
net.core.netdev_max_backlog = 250000
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_slow_start_after_idle = 0
```
Document in `DEPLOY.md`: check `ethtool -g` ring sizes, `irqbalance`, NIC offloads (TSO/GSO/GRO on), and that the CPU has AES-NI (TLS in Traefik). Recommend ≥ 4 physical cores for 10G with TLS.

### 12.7 Makefile targets

`web` (npm ci + build), `build` (web + go build), `dev` (Go server on :8080 in single-origin mode + Vite dev server with `/api` proxy, WS enabled), `test` (go test -race ./... + vitest), `lint` (go vet, staticcheck, tsc --noEmit), `e2e` (playwright), `docker`, `bench-loopback` (§13.3).

---

## 13. Testing and verification

### 13.1 Unit tests

**Go**
- `stats`: all functions against `testdata/stats-vectors.json` (median even/odd, p90, steady window incl. short case, rolling p90, WS jitter, RFC 3550 jitter, loss).
- `clientip`: table tests (§5.7).
- `session`: create/lookup/expiry/max-age/newest-wins preemption/byte caps (use an injectable clock).
- `auth`: valid/invalid bearer token, empty `API_TOKEN` (bearer always rejected), `Remote-User` present/absent, bearer request with a spoofed `Remote-User` still requires the token, `--dev` bypass.
- `transfer` via `httptest`: exact `Content-Length` and body length for `bytes=N`; `413` over limit; `410` bad sid; `429` stream cap; upload counts match body length; credentialed CORS headers (exact origin + `Allow-Credentials`), no CORS headers for foreign origins, `OPTIONS` handling; no `Content-Encoding`.
- `config`: parsing sizes and durations, validation errors.

**TS (vitest)**
- `stats.ts` against the same vectors (Go/TS parity is mandatory).
- `format.ts` units/significant digits; gauge `pos()` mapping and tick positions; `timeline.ts` interpolation and live rate.

### 13.2 Integration

- Go test starts the server on loopback (single-origin mode, with a test `API_TOKEN` that the client passes) and runs `internal/client` with `-n 1` and `-n 8`, short durations (2 s + 0.5 s grace). Assert non-zero throughput, latency samples present, upload server-counted bytes ≈ client-sent bytes (±1 request size), and that session expiry cleans up.
- RTC integration test: pion client ↔ server on loopback with an injected drop function (drop every 10th probe) → reported loss ≈ 10 %.

### 13.3 Performance validation

| Test | Setup | Target |
|---|---|---|
| Loopback, no TLS | `speedtest client --server http://127.0.0.1:8080 -n 8` on 4+ cores | ≥ 20 Gbps (sanity check that the server is not the bottleneck) |
| 10G LAN, no TLS | two hosts with 10G NICs; compare with `iperf3 -P 8` | CLI ≥ 90 % of iperf3 |
| 10G LAN, via Traefik + TLS | full prod stack | CLI ≥ 85 % of iperf3; record Traefik CPU |
| Single stream | `-n 1` | Record result; document in `PERFORMANCE.md` |
| Browser, Chrome desktop, Multi 8 | 10G LAN | Record (soft target ≥ 7 Gbps); verify no long tasks, smooth gauge |
| Browser, Firefox, Safari | same | Record; document browser ceilings |

Record CPU usage (server and Traefik), results, kernel and NIC details in `docs/PERFORMANCE.md`.

### 13.4 Network condition tests (Linux `tc netem`)

```bash
tc qdisc add dev eth0 root netem delay 30ms 5ms loss 2%
```
- Expect: idle ping ≈ 30 ms, jitter > 0, packet loss ≈ 2 % (±0.7 % at 600 packets), throughput drops in a plausible way. Remove with `tc qdisc del dev eth0 root`.
- Block UDP 50000 → loss tile shows "Unavailable", the rest completes.

### 13.5 E2E (Playwright)

Against the local server with `--dev`, `PUBLIC_URL=http://localhost:8080` and `TEST_HOSTS=http://127.0.0.1:8080` (so transfers are cross-origin, like production) and short durations (debug query `?duration=3&grace=0.5`, only honored when the server runs with `--dev`): load page → IP strip populated → Start → phases progress → tiles show non-zero values → Copy JSON produces valid schema → **the network log contains no `OPTIONS` requests**. Run in Chromium, Firefox and WebKit.

### 13.6 Manual matrix (before v1.0)

Chrome / Firefox / Safari (desktop), iOS Safari, Android Chrome × an IPv4 connection, an IPv6 connection, and a network with UDP blocked. Also: an Authelia session that expires while the page is open (expect the sign-in prompt). Also light/dark theme, 360 px width, keyboard-only use, screen reader announcement of results.

---

## 14. Security

A private, single-user app that's reachable over the internet:
- **Authelia** (Traefik forwardAuth) protects every hostname. The app double-checks with `Remote-User` or a valid bearer token (§5.9).
- **CLI token**: long random value (`openssl rand -hex 32`), compared in constant time, never logged. Rotate it by changing the env var and restarting.
- Limits are there for safety, not abuse: per-request byte and time caps, a stream cap per session, one active test at a time (newest wins).
- Credentialed CORS is restricted to the exact `PUBLIC_URL` origin.
- Strict CSP, no inline scripts or styles (check that the Vite build doesn't inline anything into `index.html`).
- The WebRTC UDP port is only usable with credentials from the authenticated signaling exchange.
- No cookies of our own, no tracking.
- `--dev` (no auth check, debug query overrides) is off by default and logs a loud warning when enabled.

---

## 15. Gotchas checklist (read before coding)

1. **HTTP/2 makes multi-connection fake**: test hosts must negotiate http/1.1 (verify with `curl -sv --http2 https://s1.speed.example.com/api/ip 2>&1 | grep -i alpn`, which must show http/1.1).
2. **Traefik TLS options + HostRegexp** don't mix. Use explicit `Host()` rules. Two routers with the same host and different TLS options cause a conflict and fall back to the default.
3. **6 connections per host** in browsers: spread over `s1..sN`.
4. **No HTTP/3** on the entrypoint.
5. **No compression** anywhere (Traefik middleware, Go handlers). Random payload anyway.
6. **DNS on Cloudflare?** Records must be DNS-only (grey cloud), otherwise you're testing Cloudflare.
7. **Go global `WriteTimeout` kills downloads.** Use `ResponseController` per-request deadlines.
8. **`io.Copy(io.Discard, …)` uses 8 KiB reads.** Use an explicit read loop with a large pooled buffer.
9. **Set `Content-Length` on downloads** (no chunked encoding overhead, better client behavior).
10. **Fetch upload streaming needs HTTP/2.** Use Blobs over HTTP/1.1.
11. **Blob memory**: share Blob instances across streams and cap at 256 MiB.
12. **`crypto.getRandomValues` max 65 536 bytes per call.**
13. **Worker `performance.now()` has a different time origin.** Timestamp on main-thread receipt, or use `performance.timeOrigin + performance.now()`.
14. **Background tabs throttle timers.** Flag results if the tab was hidden.
15. **IPv4-mapped IPv6** (`::ffff:1.2.3.4`): unmap before display.
16. **Preflight requests don't carry cookies**, so Authelia blocks them. Keep every transfer request CORS-simple (§5.5).
17. **WebRTC behind Docker bridge NAT** breaks host candidates. Use host networking or `RTC_PUBLIC_IPS`.
18. **`RTCPeerConnection` is not available in workers.** Run the loss test on the main thread (it's low-rate).
19. **CSP `connect-src`** must list all test hosts and the `wss://` origin.
20. **Mixed content**: everything https/wss in production.
21. **Upload byte counts must be added per `Read`**, not after the request ends, otherwise live upload stats are jumpy.
22. **Traefik v3 default `readTimeout` is 60 s.** Our requests are shorter, but keep it explicit (§12.1).
23. **Credentialed CORS**: `fetch(…, { credentials: 'include' })` plus `Access-Control-Allow-Credentials: true` and the exact origin. Without it, the Authelia cookie isn't sent to `s1…s8` and every stream gets redirected to the login page.
24. **Authelia cookie domain** must cover `s1…s8` (a parent domain). If it only covers `speed.example.com`, the test hosts redirect to login.
25. **An expired Authelia login looks like a CORS error** (the redirect has no CORS headers). Detect it and offer a reload (§7.1).
26. **Never measure latency with HTTP requests**: each one would include an Authelia forwardAuth round trip. The WebSocket is authenticated once at upgrade, so pings stay clean.
27. **Two routers for the same host must use the same TLS options** (the bearer-token routers mirror the Authelia routers in §12.2).

---

## 16. Milestones

### M0: Scaffold
- Repo layout (§4), `go.mod` (module `github.com/OWNER/speedtest`), Vite TS project (strict), `web/embed.go`, Makefile, `.github/workflows/ci.yml` (go vet, staticcheck, `go test -race`, npm ci, typecheck, vitest, build, docker build), `docs/DECISIONS.md` (seeded from §2).
- `speedtest serve` serves a placeholder `index.html` from the embedded FS plus `/healthz`.
- **AC**: `make build` produces one binary; running it serves the placeholder page; CI green.

### M1: Core server
- Config (§5.2), client IP (§5.7), sessions (§5.8), auth check (§5.9), `/api/config`, `/api/ip`, `/api/session`, `/api/down`, `/api/up`, CORS, static serving and security headers (§5.10), graceful shutdown, slog logging.
- **AC**: all §13.1 Go unit tests pass. Manual curl (server started with `--dev`):
  `curl -s localhost:8080/api/ip` returns JSON;
  `curl -s -o /dev/null -w '%{size_download}' "localhost:8080/api/down?sid=$SID&bytes=1000000"` prints `1000000`;
  `head -c 50000000 /dev/urandom | curl -s --data-binary @- "localhost:8080/api/up?sid=$SID"` returns `{"bytes":50000000,…}`.

### M2: Control channel + CLI client
- `/api/ws` (§5.5), `internal/stats` with shared vectors, `internal/client`, `speedtest client` per §10 (all flags except loss; human/json/csv output; exit codes; `-4`/`-6`; `--interval`).
- **AC**: CLI unit tests (§10.7) pass; integration test (§13.2) passes; `--json` output validates against the §7.5 schema; Ctrl-C exits 130 with no leaked goroutines; loopback benchmark ≥ 20 Gbps with `-n 8` (§13.3 row 1); `-n 1` vs `-n 8` show distinct TCP connections (verify with `ss -tn` during a test: 8 established connections to :8080).

### M3: Browser engine (minimal UI)
- `protocol.ts`, `api.ts`, orchestrator, transfer worker, latency worker, timeline, `stats.ts` (vector parity), IP detection, results object, plain-HTML output (no design yet).
- Dev setup: `make dev` (single-origin mode).
- **AC**: in Chrome, Firefox and Safari locally a full test completes and prints a valid results JSON. Vitest vector parity passes. Stop mid-test aborts all streams (verify in the network panel) and the server session expires.

### M4: UI and design
- Gauge, sparklines, tiles, controls, IP strip, themes, responsive layout, reduced motion, accessibility, copy results (§11).
- **AC**: matches the §11 layout on desktop and at 360 px; both themes pass AA contrast; budgets met (§11.8: report gzipped sizes in the PR); no long tasks > 50 ms during a test; Playwright e2e (§13.5) green in all 3 engines.

### M5: Deployment
- Dockerfile, compose, Traefik static and dynamic config (incl. Authelia middleware and bearer-token routers), Authelia snippet, sysctl, systemd unit, `docs/DEPLOY.md` (DNS records, cert, firewall, tuning, verification steps), multi-arch image publishing in CI.
- **AC**: deployed on a real host with the §3.1 hostnames: the ALPN check (gotcha 1) shows http/1.1 on test hosts and h2 on the main host; unauthenticated requests to the main host and to `s1` redirect to Authelia; after logging in, a full browser test works with cookies sent to `s1…s8` and no `OPTIONS` requests; the CLI works with `--token` and exits with code 3 without it; a spoofed `Remote-User` header without a token gets `401`; the IP strip shows the address in use; a Multi-8 browser test opens 8 TCP connections (`ss -tn` on the server shows 8 connections from Traefik's client side); CSP shows no violations in the console.

### M6: Packet loss and jitter
- pion server (§8.1), channels and protocol (§8.2), browser `loss.ts`, CLI loss test, UI tile states, failure handling (§8.3).
- **AC**: RTC integration test passes; `netem loss 2%` gives a measured loss within ±0.7 %; blocking UDP 50000 shows "Unavailable" while the rest of the test completes; no leaked peer connections after 50 consecutive tests (check goroutine count via `/metrics` or pprof in dev).

### M7: Hardening and validation
- Release pipeline: multi-arch image plus standalone CLI archives for Linux/macOS/Windows with `SHA256SUMS` (§10.1).
- Optional `/metrics`, review of limits, 10G validation (§13.3) written to `docs/PERFORMANCE.md`, manual matrix (§13.6), README (features, screenshots, quick start: `docker compose up`).
- **AC**: §13.3 targets met or deviations documented with analysis; manual matrix completed and recorded; v1.0.0 tag and release image.

### Stretch (post-v1, only on request)
- **LAN mode without TLS/Traefik**: `SPEEDTEST_EXTRA_PORTS=8081-8087` so each port is its own origin (separate browser connection pools), enabling > 6 streams without DNS.
- ASN/ISP display via an optional local MaxMind GeoLite2 `.mmdb` file.
- Result share links (SQLite, still a single binary).
- Multi-server list with latency-based auto selection.
- Configurable STUN for clients behind symmetric NAT.
- i18n.

---

## 17. Definition of done (v1)

- One binary (and one `FROM scratch` image) serves the UI and all endpoints; deploys behind Traefik + Authelia using only the files in `deploy/`.
- Browser test measures download, upload (single and multi), idle and loaded latency, jitter, and packet loss (or shows it as unavailable), and shows the IP address in use.
- CLI client (§10) produces the same result schema and methodology, is released as standalone archives for Linux/macOS/Windows, and is documented in the README (install, examples, exit codes).
- 10G validation documented. All tests and CI green. `README.md` and `docs/DEPLOY.md` are enough for a newcomer to deploy it in under 30 minutes.
