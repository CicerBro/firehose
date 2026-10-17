# Performance validation (10 Gbps)

This is the build plan §13.3 validation report (milestone M7). Each section says how to
run the test, and has a table to fill in. Cells marked **TODO** still need a measurement.
Throughput is in SI units (1 Gbps = 10⁹ bit/s). Unless noted otherwise, use the
defaults: 12 s per direction, 2 s grace.

## Targets

| # | Test | Setup | Target | Status |
|---|---|---|---|---|
| 1 | Loopback, no TLS | `speedtest client --server http://127.0.0.1:8080 -n 8` on 4+ cores | ≥ 20 Gbps (the server is not the bottleneck) | TODO |
| 2 | 10G LAN, no TLS | two hosts with 10G NICs, compared with `iperf3 -P 8` | CLI ≥ 90 % of iperf3 | TODO |
| 3 | 10G LAN, via Traefik + TLS | full production stack | CLI ≥ 85 % of iperf3; record Traefik CPU | TODO |
| 4 | Single stream | `-n 1` | Record the result | TODO |
| 5 | Browser, Chrome desktop, Multi 8 | 10G LAN | Soft target ≥ 7 Gbps; no long tasks; smooth gauge | TODO |
| 6 | Browser, Firefox, Safari | 10G LAN | Record; document the browser ceilings | TODO |

## Test environment

Fill this in once per hardware setup. Results without environment details are meaningless.

| | Server | Client |
|---|---|---|
| Hostname / role | TODO | TODO |
| CPU (model, physical cores, AES-NI) | TODO | TODO |
| RAM | TODO | TODO |
| NIC (model, driver, firmware) | TODO | TODO |
| Link (speed, MTU, switch) | TODO | TODO |
| OS / kernel (`uname -r`) | TODO | TODO |
| sysctl (`deploy/sysctl/99-speedtest.conf` applied?) | TODO | TODO |
| Congestion control / qdisc | TODO | TODO |
| NIC offloads (TSO/GSO/GRO), ring sizes | TODO | TODO |
| speedtest version (`speedtest version`) | TODO | TODO |
| Traefik version | TODO | n/a |
| Browser versions | n/a | TODO |

Commands to collect it:

```bash
lscpu | grep -E 'Model name|Socket|Core|Thread'; grep -o -m1 -w aes /proc/cpuinfo
uname -r; ethtool -i eth0; ethtool eth0 | grep Speed; ip link show eth0 | grep -o 'mtu [0-9]*'
sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc net.core.rmem_max net.core.wmem_max
ethtool -k eth0 | grep -E 'tcp-segmentation|generic-(segmentation|receive)'; ethtool -g eth0
```

## How to measure CPU

Run the test with `-d 20s` so there is a long steady state, and sample CPU in the
middle of it:

```bash
pidstat -u -p "$(pgrep -d, -x speedtest),$(pgrep -d, -x traefik)" 1 10
# or: top -b -n 5 -d 2 -p "$(pgrep -x speedtest)" -p "$(pgrep -x traefik)"
```

Report the average `%CPU`; 100 % = one core.

## 1. Loopback, no TLS

Checks that the Go server itself isn't the bottleneck. Run it on the server (or any 4+ core machine):

```bash
make bench-loopback
# equivalent:
./speedtest serve --dev --listen 127.0.0.1:18080 --log-level warn &
./speedtest client --server http://127.0.0.1:18080 -n 8 --no-loss --duration 6s
./speedtest client --server http://127.0.0.1:18080 -n 1 --no-loss --duration 6s
```

Use `--json` to capture exact values. Also check that `-n 8` really opens 8 connections:
`ss -tn state established '( dport = :18080 )' | wc -l` during the run.

| Machine (CPU, cores) | OS | Streams | Download | Upload | Idle latency | Loaded latency ↓ / ↑ | Server CPU | Meets ≥ 20 Gbps? |
|---|---|---|---|---|---|---|---|---|
| TODO | TODO | 8 | TODO | TODO | TODO | TODO | TODO | TODO |
| TODO | TODO | 1 | TODO | TODO | TODO | TODO | TODO | n/a |

## 2. 10G LAN, no TLS (CLI vs iperf3)

Two hosts on the same 10G segment. First establish the baseline with iperf3, then run
the CLI against the server's **plain HTTP** port. For this test only, make the app
listen on the LAN address (never do this in production):

```bash
# server
iperf3 -s
./speedtest serve --dev --listen 0.0.0.0:8080
# client
iperf3 -c <server> -P 8 -t 20          # client → server (upload)
iperf3 -c <server> -P 8 -t 20 -R       # server → client (download)
speedtest client --server http://<server>:8080 --direct -n 8 --no-loss -d 20s --json > lan-plain.json
```

| Tool | Streams | Client → server (upload) | Server → client (download) | Server CPU | Notes |
|---|---|---|---|---|---|
| `iperf3 -P 8` | 8 | TODO | TODO (`-R`) | TODO | |
| `speedtest client` | 8 | TODO | TODO | TODO | |
| **Ratio CLI / iperf3** | | TODO % | TODO % | | target ≥ 90 % |

## 3. 10G LAN via Traefik + TLS

The full production stack (Traefik + Authelia + host networking), with the bearer token,
test hosts `s1…s8` active, and **no** `--direct`:

```bash
export SPEEDTEST_TOKEN=…
speedtest client --server https://speed.example.com -n 8 -d 20s --json > lan-tls.json
speedtest client --server https://speed.example.com -n 8 -d 20s --direct --json > lan-tls-direct.json
```

Record Traefik's CPU (TLS is usually the cost here).

| Mode | Streams | Download | Upload | % of iperf3 ↓ / ↑ | Traefik CPU | speedtest CPU | Idle / loaded latency |
|---|---|---|---|---|---|---|---|
| test hosts (`s1…s8`) | 8 | TODO | TODO | TODO | TODO | TODO | TODO |
| `--direct` (main host) | 8 | TODO | TODO | TODO | TODO | TODO | TODO |
| test hosts | 16 | TODO | TODO | TODO | TODO | TODO | TODO |

Target: CLI ≥ 85 % of iperf3.

## 4. Single stream

```bash
speedtest client --server https://speed.example.com -n 1 -d 20s --json > single.json
iperf3 -c <server> -P 1 -t 20 ; iperf3 -c <server> -P 1 -t 20 -R
```

| Path | Download | Upload | iperf3 -P 1 ↓ / ↑ | Notes (CPU core saturated? RTT?) |
|---|---|---|---|---|
| Loopback, no TLS | TODO | TODO | n/a | |
| LAN, no TLS | TODO | TODO | TODO | |
| LAN, Traefik + TLS | TODO | TODO | TODO | |

## 5–6. Browsers

Run from the client host on the 10G LAN against `https://speed.example.com`, logged in
through Authelia. Use Multi 8 streams, 12 s, and the loss test on. Run each three times
and record the median. For Chrome, also record a Performance-panel trace during the
download phase, and check for **long tasks > 50 ms** and gauge frame rate.

| Browser (version, OS) | Mode | Download | Upload | Idle latency | Loaded ↓ / ↑ | Loss ↓ / ↑ | Long tasks > 50 ms | Gauge smooth? | Client CPU |
|---|---|---|---|---|---|---|---|---|---|
| Chrome TODO | Multi 8 | TODO | TODO | TODO | TODO | TODO | TODO | TODO | TODO |
| Chrome TODO | Multi 16 | TODO | TODO | TODO | TODO | TODO | TODO | TODO | TODO |
| Chrome TODO | Single | TODO | TODO | TODO | TODO | TODO | TODO | TODO | TODO |
| Firefox TODO | Multi 8 | TODO | TODO | TODO | TODO | TODO | TODO | TODO | TODO |
| Firefox TODO | Single | TODO | TODO | TODO | TODO | TODO | TODO | TODO | TODO |
| Safari TODO | Multi 8 | TODO | TODO | TODO | TODO | TODO | TODO | TODO | TODO |
| Safari TODO | Single | TODO | TODO | TODO | TODO | TODO | TODO | TODO | TODO |

Soft target: Chrome Multi 8 ≥ 7 Gbps.

**Browser ceilings observed:** TODO. Describe where each browser tops out and why
(e.g. a main-thread or worker CPU bound, upload Blob handling, per-host connection
limits).

## Network-condition sanity checks (§13.4)

On the server: `tc qdisc add dev eth0 root netem delay 30ms 5ms loss 2%`.
Remove it with `tc qdisc del dev eth0 root`.

| Condition | Expected | Idle latency | Jitter | Loss ↓ / ↑ | Download / upload | Pass? |
|---|---|---|---|---|---|---|
| `netem delay 30ms 5ms loss 2%` | ping ≈ 30 ms, jitter > 0, loss ≈ 2 % ± 0.7 % | TODO | TODO | TODO | TODO | TODO |
| UDP 50000 blocked | loss "Unavailable", rest completes | n/a | n/a | TODO | TODO | TODO |

## Analysis and deviations

TODO: for each target that isn't met, explain the bottleneck (CPU profile, NIC, TLS,
browser) and what was tried. Link raw JSON results and traces here.
