# Deploying the firehose

This guide takes you from an empty host to a working, authenticated speed test in
about 30 minutes. It assumes Docker Compose. A bare-metal (systemd) variant is in §9.

> **Placeholders.** Every file in `deploy/` uses these example values. Replace them
> everywhere before you start:
>
> | Placeholder | Meaning | Where |
> |---|---|---|
> | `speed.example.com` | Your firehose domain (UI host; test hosts are `s1…s8.` under it) | `traefik/dynamic/firehose.yml`, `firehose.env`, `authelia/configuration.snippet.yml` |
> | `example.com` | Parent domain, used for the Authelia cookie | `authelia/configuration.snippet.yml` |
> | `auth.example.com` | Your Authelia portal | `authelia/configuration.snippet.yml` |
> | `ams-1` | Server name shown in the UI | `firehose.env` |
> | `admin@example.com` | ACME account e-mail | `traefik/traefik.yml` |
> | `127.0.0.1:9091` | Authelia's address, as seen by Traefik | `traefik/dynamic/firehose.yml` |
> | `YOUR_USER` | Your Authelia username | `authelia/configuration.snippet.yml` |
>
> A quick way to do the domain:
> `grep -rl speed.example.com deploy | xargs sed -i 's/speed\.example\.com/speed.yourdomain.net/g'`

## 0. Requirements

- A Linux host with a **public IP** (IPv4, IPv6, or both), not behind a CDN or a load balancer. For 10 Gbps: a 10G NIC, ≥ 4 physical cores with AES-NI (Traefik does TLS), and ~1 GiB RAM.
- Docker Engine with the Compose plugin.
- A domain whose DNS is hosted by a provider that [Traefik's DNS-01 challenge supports](https://doc.traefik.io/traefik/https/acme/#providers), and an API token for it.
- **Authelia ≥ 4.38**, already running and reachable from the host (the `/api/authz/forward-auth` endpoint). Authelia is not part of this compose file.

How it fits together: Traefik terminates TLS on :443 and asks Authelia whether each
browser request is logged in (`forwardAuth`). It then proxies plain HTTP to the
firehose on `127.0.0.1:8080`. The CLI skips Authelia with a bearer token that the app
checks itself. Packet-loss probes use WebRTC directly on **UDP 50000**, bypassing Traefik.

## 1. DNS records

Create **nine** hostnames, all pointing at the same server:

| Name | Type | Value |
|---|---|---|
| `speed.example.com` | A / AAAA | server IPv4 / IPv6 |
| `s1.speed.example.com` … `s8.speed.example.com` | A / AAAA | the same addresses |

Notes:

- Create the AAAA records only if the host really has working IPv6. Browsers pick one family per connection (Happy Eyeballs), and the UI shows which one they used.
- Instead of eight records you can use one wildcard, `*.speed.example.com`, plus the `speed.example.com` record.
- Why `s1…s8`? Browsers allow about 6 HTTP/1.1 connections per host. Spreading streams over hostnames allows up to 32 real TCP connections.

> ⚠️ **Cloudflare (or any CDN): DNS-only / grey cloud.** Every one of these records
> must be **DNS only** (grey cloud). If it's proxied (orange cloud), you're measuring
> Cloudflare's edge, not your server, and the proxy also breaks the HTTP/1.1-only test
> hosts and WebRTC.

Check it:

```bash
for h in speed s1 s2 s3 s4 s5 s6 s7 s8; do dig +short A $h.speed.example.com; dig +short AAAA $h.speed.example.com; done
```

Every line must be your server's address, not a CDN range.

## 2. Firewall

Allow inbound:

| Port | Protocol | For |
|---|---|---|
| 80 | TCP | HTTP → HTTPS redirect |
| 443 | TCP | Traefik (UI, API, test streams) |
| **50000** | **UDP** | WebRTC packet-loss test (`FIREHOSE_RTC_UDP_PORT`) |

Don't expose 8080. The app listens on `127.0.0.1:8080` only.

```bash
# ufw
sudo ufw allow 80/tcp && sudo ufw allow 443/tcp && sudo ufw allow 50000/udp
# firewalld
sudo firewall-cmd --permanent --add-port={80/tcp,443/tcp,50000/udp} && sudo firewall-cmd --reload
```

Check any cloud security group or router port-forward as well. If UDP 50000 is
blocked, the test still works, but the loss tile shows "Unavailable (UDP blocked)".

If the server is **behind NAT** (it has a private address and the router forwards
443/50000), set `FIREHOSE_RTC_PUBLIC_IPS` to the public address(es). Otherwise WebRTC
advertises the private IP and the loss test can't connect.

## 3. Authelia

Merge [`deploy/authelia/configuration.snippet.yml`](../deploy/authelia/configuration.snippet.yml)
into your Authelia `configuration.yml` and restart Authelia. Two things matter:

1. **Cookie domain = a parent of all firehose hostnames.** `domain: example.com` covers `speed.example.com` *and* `s1…s8.speed.example.com`. If the cookie only covers `speed.example.com`, the browser doesn't send it to the test hosts: every stream is redirected to the login page and the test fails immediately with what looks like a CORS error.
2. **An access-control rule for both `speed.example.com` and `*.speed.example.com`**, limited to your user (`two_factor` recommended).

In `deploy/traefik/dynamic/firehose.yml`, set the `forwardAuth.address` to where
Traefik can reach Authelia. Traefik uses host networking, so if Authelia publishes
port 9091 on the host, `http://127.0.0.1:9091/api/authz/forward-auth` works as is.

## 4. TLS: wildcard certificate via DNS-01

Traefik requests one certificate for `speed.example.com` + `*.speed.example.com` from
Let's Encrypt. **A wildcard needs the DNS-01 challenge**, so Traefik needs an API token
for your DNS provider.

- `deploy/traefik/traefik.yml`: set `email`, and set `dnsChallenge.provider` (default `cloudflare`).
- Put the token in `deploy/.env`. For Cloudflare, create an API token with **Zone → DNS → Edit** for the zone:

  ```bash
  echo "CF_DNS_API_TOKEN=xxxxxxxx" > deploy/.env && chmod 600 deploy/.env
  ```

  For other providers, use their variables (see the Traefik provider list) and adjust `environment:` in `docker-compose.yml`.

Certificates are stored in `deploy/acme/acme.json`. Traefik creates the file with mode `600`.

## 5. Configure and start

```bash
cd deploy
cp firehose.env.example firehose.env && chmod 600 firehose.env
```

Edit `firehose.env`: `FIREHOSE_PUBLIC_URL`, `FIREHOSE_TEST_HOSTS`, `FIREHOSE_SERVER_NAME`.

**Bearer token for the CLI.** Generate it and put it in `firehose.env`:

```bash
sed -i "s/^FIREHOSE_API_TOKEN=.*/FIREHOSE_API_TOKEN=$(openssl rand -hex 32)/" firehose.env
grep ^FIREHOSE_API_TOKEN firehose.env      # copy this value to your CLI machine as FIREHOSE_TOKEN
```

The token is compared in constant time and never logged. To rotate it, change the
value and run `docker compose up -d`. Leave it empty to turn off CLI access completely.

Start:

```bash
docker compose pull && docker compose up -d
docker compose logs -f
```

Within about a minute, Traefik should log that it obtained the certificate. The
firehose logs a JSON `listening` line.

Both containers run with **host networking** (`network_mode: host`). This is
deliberate: Docker's bridge NAT costs throughput at 10 Gbps and breaks WebRTC host
candidates. Because of it, the app's `127.0.0.1:8080` and Traefik's
`http://127.0.0.1:8080` refer to the same loopback, and nothing else on the host may
already use ports 80, 443, 8080 or UDP 50000.

## 6. Host tuning (recommended for ≥ 1 Gbps)

```bash
sudo cp deploy/sysctl/99-firehose.conf /etc/sysctl.d/
echo tcp_bbr | sudo tee /etc/modules-load.d/bbr.conf && sudo modprobe tcp_bbr
sudo sysctl --system
sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc   # → bbr, fq
```

The file sets the `fq` qdisc with BBR and 256 MiB maximum socket buffers, so a single
stream can fill a high bandwidth-delay path. It also raises `netdev_max_backlog`,
turns on MTU probing, and turns off slow-start-after-idle.

NIC and CPU checks for 10G (replace `eth0` with your interface):

```bash
ethtool -g eth0                     # ring sizes: if "Current" < "Pre-set maximums", raise them:
sudo ethtool -G eth0 rx 4096 tx 4096
ethtool -k eth0 | grep -E 'tcp-segmentation-offload|generic-segmentation-offload|generic-receive-offload'
                                    # TSO / GSO / GRO should all be "on"
sudo ethtool -K eth0 tso on gso on gro on
systemctl status irqbalance         # spread NIC interrupts over cores (or pin them manually)
grep -o -m1 -w aes /proc/cpuinfo    # AES-NI present → cheap TLS in Traefik (arm64: grep -m1 -w aes)
nproc                               # ≥ 4 physical cores recommended for 10G with TLS
```

`ethtool` settings don't persist across reboots. Persist them with your network
manager (e.g. a systemd-networkd `.link` file or a NetworkManager dispatcher script).

## 7. Verify (milestone M5 checklist)

Run these from a **client machine**, not the server. `$TOKEN` is your `FIREHOSE_API_TOKEN`.

**1. ALPN.** The test hosts must speak HTTP/1.1 only; the main host offers h2:

```bash
curl -sv --http2 -o /dev/null https://s1.speed.example.com/api/ip 2>&1 | grep -i alpn
#   → "ALPN: server accepted http/1.1"   (must NOT be h2)
curl -sv --http2 -o /dev/null https://speed.example.com/api/ip 2>&1 | grep -i alpn
#   → "ALPN: server accepted h2"
```

If a test host says `h2`, its router isn't getting the `http11only` TLS options; see
Troubleshooting. With h2, "8 connections" is fake.

**2. Unauthenticated requests redirect to Authelia** (on the main host and on the test hosts):

```bash
curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' https://speed.example.com/
curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' https://s1.speed.example.com/api/ip
#   → 302 (or 401/303) pointing at https://auth.example.com/…
```

**3. A spoofed `Remote-User` without a valid token doesn't get in.** Without a bearer
header, the request goes through Authelia (redirect). With a bearer header, the
`strip-auth-headers` middleware removes `Remote-User` and the app rejects the wrong token:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -H 'Remote-User: admin' https://speed.example.com/api/config     # → 302 to Authelia
curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer wrong' -H 'Remote-User: admin' \
     https://speed.example.com/api/config                                                                 # → 401
```

**4. The CLI works with the token and exits with code 3 without it:**

```bash
FIREHOSE_TOKEN=$TOKEN firehose client -s https://speed.example.com; echo "exit $?"   # → results, exit 0
firehose client -s https://speed.example.com -q; echo "exit $?"                      # → auth error, exit 3
docker run --rm -e FIREHOSE_TOKEN=$TOKEN ghcr.io/cicerbro/firehose client -s https://speed.example.com
```

**5. Browser test.** Open `https://speed.example.com`, log in, and keep DevTools open (Network + Console):

- The IP strip shows the address you connect from, with the right IPv4/IPv6 badge.
- Run a **Multi, 8 streams** test. Requests to `s1…s2.speed.example.com/api/down` and `/api/up` succeed (status 200). Their request headers include the Authelia cookie.
- Filter the Network panel by method: **there are no `OPTIONS` requests**.
- The Console shows **no CSP violations**.
- The packet-loss tile shows a value, not "Unavailable" (if it is unavailable, check UDP 50000).

**6. Real TCP connections.** While a Multi-8 download runs, on the server:

```bash
ss -tn state established '( sport = :443 )' | grep -c <your client IP>
#   → ≥ 8 browser connections to Traefik (plus the main-host connection)
ss -tn state established '( dport = :8080 )' | wc -l
#   → ≥ 8 connections from Traefik to the app
```

## 8. Updating

```bash
cd deploy && docker compose pull && docker compose up -d
```

In production, pin the image to a release (`ghcr.io/cicerbro/firehose:1.0.0`) instead
of `latest`. Releases are listed on the GitHub releases page and come with `SHA256SUMS`.

## 9. Bare metal (systemd) instead of Docker

```bash
# Binary from the releases page (see the README), or `make build`
sudo install -m 755 firehose /usr/local/bin/firehose
sudo install -m 600 deploy/firehose.env.example /etc/firehose.env     # edit it as in §5
sudo cp deploy/systemd/firehose.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now firehose
journalctl -u firehose -f
```

The unit runs as a dynamic, unprivileged user with a strict sandbox and
`LimitNOFILE=65536`. Run Traefik however you already do; the two files in
`deploy/traefik/` work unchanged. The DNS, firewall, Authelia, tuning and
verification steps above all still apply.

## 10. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Test hosts negotiate **h2** in the ALPN check | The `http11only` TLS option isn't applied | Use explicit `Host()` rules, never `HostRegexp`. Every router for a test host (Authelia **and** bearer) must use `options: http11only`; two routers with different TLS options for one host conflict, and Traefik falls back to the default. Check the Traefik log for "TLS options" conflicts |
| Download/upload fails at once; Console shows a **CORS error**; the UI says "Signed out" | The Authelia cookie isn't sent to `s1…s8`, or the login expired (Authelia's redirect has no CORS headers) | Cookie `domain` must be a parent (`example.com`). The access rule must include `*.speed.example.com`. Reload and log in again |
| `OPTIONS` requests appear and are redirected | A transfer request became non-simple (a custom header or a `Content-Type`) | A frontend bug: preflights carry no cookies, so Authelia blocks them. Report it |
| Results look like **Cloudflare speeds**, or the IP shown is a Cloudflare IP | The DNS records are proxied | Set every record to **DNS only** (grey cloud) |
| Multi is no faster than Single; only one or two connections in `ss` | HTTP/2 on the test hosts, or single-origin mode | Run the ALPN check; make sure `FIREHOSE_TEST_HOSTS` is set (empty = single origin, max 6) |
| Packet loss always "Unavailable (UDP blocked)" | UDP 50000 is blocked, or a NAT advertises the private IP | Open UDP 50000 (host firewall **and** cloud security group). Behind NAT, set `FIREHOSE_RTC_PUBLIC_IPS`. Keep host networking (bridge NAT breaks host candidates) |
| CLI exits with **3** | No token, the wrong token, or the request didn't match the bearer router (it got Authelia's redirect) | Check that `FIREHOSE_TOKEN` equals the server's `FIREHOSE_API_TOKEN` and that the server value isn't empty. Check that the `*-token` routers exist in Traefik's dynamic config |
| CLI exits with **2** | DNS, firewall or TLS problem | `curl -v https://speed.example.com/healthz`; use `--ca-file` for a private CA |
| Every request returns `401` behind Authelia | Authelia's response headers aren't forwarded | `authResponseHeaders` must include `Remote-User` (or whatever `FIREHOSE_AUTH_HEADER` is) |
| The IP strip shows `127.0.0.1` or Traefik's IP | The app doesn't trust the proxy hop | `FIREHOSE_TRUSTED_PROXIES` must include the address Traefik connects from (with host networking: loopback, the default) |
| Downloads stop after ~60 s, or uploads fail | Traefik timeouts, or a buffering/retry middleware | Use `deploy/traefik/traefik.yml` (`readTimeout: 90s`, `writeTimeout: 0s`). No buffering, compress or retry middleware on these routers |
| Throughput much lower than `iperf3` | Host not tuned; Traefik CPU-bound on TLS | §6 tuning; check AES-NI and that Traefik's CPU isn't pinned at 100 %; compare with the CLI over plain HTTP (see [PERFORMANCE.md](PERFORMANCE.md)) |
| Certificate not issued | DNS API token permissions, or a wrong provider | `docker compose logs traefik | grep -i acme`. The token needs DNS edit rights on the zone |
| Page loads but is blank; Console shows a CSP error | `FIREHOSE_PUBLIC_URL` or `FIREHOSE_TEST_HOSTS` don't match the hostnames you're using | They must exactly match the scheme and host in the browser (no trailing slash) |
| Results warn "Tab was in background" | The tab was hidden during the test; browsers throttle background tabs | Keep the tab in the foreground and run again |

Still stuck? Run the server with `FIREHOSE_LOG_LEVEL=debug`, and compare the browser
result with `firehose client` from the same machine: if the CLI is fast and the browser
is slow, the limit is the browser.
