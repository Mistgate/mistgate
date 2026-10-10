---
title: Benchmarks
description: Memory, CPU, disk, start time and subscription load of Mistgate, 3x-ui, Remnawave and PasarGuard on the same small server, and a one-server test with the VPN core and Hysteria2 traffic. Method, versions and limits included.
---

## Summary

- **Memory is the clear difference.** With 0 to 200 users the Mistgate panel alone holds 16-20 MiB of private memory; 3x-ui holds 68-74, PasarGuard 262-265 and Remnawave 426-454. With the VPN core and the database on one server: 57 MiB against 84, 299 and 707.
- **Users and restarts do not touch the tunnel.** Adding and removing users and restarting the panel under a running Hysteria2 tunnel dropped no connection in Mistgate. 3x-ui restarts its Xray core when users change.
- **The data plane is not a differentiator.** On two vCPU all four land within 17 % of each other; Mistgate's node moved 12-18 % more data per CPU-second than the Xray-based cores.
- **Weak spots of Mistgate.** A small request inside a loaded tunnel waited 29 ms at p95 against about 2 ms for the others (67 ms on 1 vCPU); a subscription render that missed the cache was slow (1,630 requests per second on one vCPU in a modified build, p99 1.7 s). Three fixes found by profiling are in `main` since v0.1.32 and roughly double the uncached path; see [After the fixes](#after-the-fixes). Creating 1000 users through the API took 42 s against 18 s for 3x-ui, mostly because of the admin API rate limit (see the note under the one-server table).
- **Subscription throughput of the shipped build is not measured:** it allows 60 fetches per link and hour, so the load test only measured how fast it refuses.
- One desktop CPU, one WSL2 machine, three repetitions: read [What the numbers do not tell you](#what-the-numbers-do-not-tell-you) before quoting any figure.

Measured on 9 and 10 October 2026. Numbers are medians of 3 repetitions with the minimum and maximum in brackets, unless a cell says n=1. Everything ran on one desktop machine inside WSL2. The scripts, compose files and aggregated results are in [`tools/bench`](https://github.com/Mistgate/mistgate/tree/main/tools/bench) in the repository.

## What was measured

Four panels, each installed as its own documentation describes, with its defaults:

| Panel | Version | How it was installed | Database | Processes in the measured group |
|:--|:--|:--|:--|:--|
| Mistgate | v0.1.32 (built from the tag, no release key) | one static binary, `mistgate setup`, `mistgate serve` | SQLite | 1 |
| 3x-ui | v3.9.0 | published image `ghcr.io/mhsanaei/3x-ui:v3.9.0`, project compose file | SQLite | 3: x-ui, the Xray core it supervises, fail2ban |
| Remnawave | panel 3.4.5 | official `docker-compose-prod.yml` (backend, Postgres 18.4, Valkey 9), image pinned | PostgreSQL | 22 in 3 containers |
| PasarGuard | v5.4.1 | the project's default (SQLite) compose file | SQLite | 1 (Python) |

Two scenarios:

1. **Panel only**: a small panel host, 1 vCPU and 1 GB RAM with no swap, no VPN traffic. Idle memory and CPU, disk, start time, creating 200 users through the API, and a subscription load (50 clients, 60 s, 200 users).
2. **One server**: panel, VPN core (or node) and the panel's database all on one box of 2 vCPU and 2 GB RAM with no swap, 1000 users, Hysteria2 for every panel.

## Results: panel only (1 vCPU, 1 GB)

Private memory (anon) is the headline for memory. `memory.current` is shown next to it because it is what `docker stats` and `systemctl status` show, but it includes page cache, and the cache is charged unevenly (see the notes).

| | Mistgate | 3x-ui | PasarGuard | Remnawave |
|:--|--:|--:|--:|--:|
| Disk: image or binary (MB) | 62 | 462 | 462 | 1551 |
| Disk: data after 200 users (MB) | 5.2 | 4.8 | 4.6 | 61 |
| Idle, 0 users: anon (MiB) | 16 (16-16) | 68 (68-68) | 262 (262-262) | 426 (425-430) |
| Idle, 0 users: memory.current (MiB) | 27 (17-29) | 186 (185-186) | 358 (295-358) | 599 (595-746) |
| Idle, 200 users: anon (MiB) | 20 (20-21) | 74 (72-74) | 265 (265-265) | 454 (452-458) |
| Idle, 200 users: memory.current (MiB) | 27 (26-29) | 154 (153-155) | 299 (299-303) | 644 (629-649) |
| Idle, 200 users: CPU (% of one vCPU) | 0.07 | 0.40 | 0.15 | 3.09 |
| Processes / containers | 1 / 0 | 3 / 1 | 1 / 1 | 22 / 3 |
| 200 users created through the API (s) | 0.3 | 1.9 | 2.3 | 1.2 |
| Cold start, empty data (s) | 0.33 (0.25-0.33) | 2.12 (2.07-2.12) | 11.6 (11.0-11.9) | 14.2 (14.1-15.0) |
| Cold start, 200 users on disk (s) | 0.13 | 1.51 (1.46-1.92) | 6.7 (6.5-7.0) | 12.9 (12.5-13.1) |
| Subscription load: anon during the run (MiB) | 27 (27-28) | 85 (85-86) | 280 | 870 (856-874) |
| Subscription load: peak memory.current (MiB) | 35 (34-37) | 177 | 325 | 1024 (the limit) |

Notes on the table:

- Cold start is the time from the start command to the first HTTP 200 on the panel's main port. For 3x-ui the subscription listener (port 2096) comes up a little later than the main port; in two of the three 3x-ui repetitions the check of that port right after the restart was refused, so the post-restart memory sample of those two repetitions is missing. Every other number of those repetitions is complete.
- Remnawave reaches the 1 GB limit during the load: the cgroup counted 47,000 to 131,000 `max` events (allocations that had to reclaim) in the three repetitions, and no OOM kill. In the single quota-only repetition (below) one worker was killed. Its private memory during the load, about 870 MiB, is real, not cache. Postgres, Valkey and three Node.js processes make up the group.
- 3x-ui carries its Xray core in the measured group (anon about 9-12 MiB) and fail2ban (about 12 MiB). Subtract them if you compare only the panel process: x-ui itself is about 55 MiB.
- PasarGuard and Remnawave had no VPN core running in the measured group (no node attached), and Mistgate had no node connected (the agent that was needed once to make the subscription non-empty is removed before the measurement; the node record stays).
- A second pass with the page cache dropped before every start (`drop_caches`, one repetition per panel) gave the same anon figures (Mistgate 16/20 MiB, 3x-ui 62/75, PasarGuard 260/265, Remnawave 639/616 at 0/200 users) and a larger `memory.current` for Mistgate (30/38 MiB) because its binary is then read in under the measured cgroup.

### Subscription load

50 concurrent clients, 60 s, the same User-Agent (`v2rayN/7.5.0`), 200 links, keep-alive on. Every panel returns one link per user, base64 in a text body of 152 to 360 bytes. Formats differ in what the link is (Mistgate Hysteria2, 3x-ui VLESS Reality, Remnawave and PasarGuard Shadowsocks), see the notes at the end.

| | Mistgate (as shipped) | 3x-ui | PasarGuard | Remnawave |
|:--|--:|--:|--:|--:|
| Requests per second, status 200 | 197 (limiter, see below) | 148 (147-153) | 274 (268-284) | 271 (166-307) |
| p50 / p95 / p99 latency (ms) | 2.5 / 5.6 / 7.4 | 296 / 511 / 1230 | 166 / 378 / 519 | 113 / 230 / 1765 |
| Non-200 responses | 99.4 % | 0 | 0 | 0 |
| Mean CPU (% of one vCPU) | 98 | 41 | 100 | 99 |

**Mistgate as shipped does not serve this load, by design.** Each subscription link may be fetched 60 times an hour; the 61st and later answer 429. In every repetition exactly 11,799 requests got 200 (60 per link and hour, minus one warm-up fetch per link and one sanity-check fetch before the load) and 1.6 to 2.0 million got 429. The 429 path is cheap (27,000 to 34,000 answers per second on one vCPU), so the "requests per second" of the first row is the speed of refusing, not of serving. It is not a result. The per-client-address bucket of the public listener (10 requests per second, burst 60) did not decide this run: each link had its own client address through `X-Forwarded-For` behind `--trusted-proxy`, and with the per-link cap switched off the same bucket became the limit at about 2,590 answers per second (first dry run).

3x-ui uses 41 % of one vCPU at 148 requests per second: its limit is something other than CPU (it closes the connection after every response; cause not investigated).

### Supplementary: Mistgate with the limits switched off (modified builds)

Not the shipped build. These two builds change one or two lines of the v0.1.32 source (diff: [`mistgate-bench-variants.diff`](https://github.com/Mistgate/mistgate/blob/main/tools/bench/mistgate-bench-variants.diff)) so that the request cost can be compared with the other panels. Never quote them as Mistgate's result.

| | limits off, render cache on (`nolimit`) | limits off, render cache off (`nolimit-nocache`) |
|:--|--:|--:|
| What changed | per-link cap and per-address bucket off | the same, and the 10 s render cache of a link off |
| Requests per second, status 200 | 19,580 (19,331-19,614) | 1,630 (1,612-1,633) |
| p50 / p95 / p99 (ms) | 2.4 / 5.4 / 7.2 | 4.2 / 6.6 / 1,702 |
| Non-200 | 0 | 0 |
| Mean CPU | 100 % | 100 % |

With the cache off every request reads the database (several statements, each parsed again by SQLite); with the cache on, a link is read at most every 10 s. The profile of the uncached path is in [Profiling](#profiling-mistgate-only). The 1.7 s p99 of the uncached path is a starvation of a small share of the requests (about 25 per second, constant over the run), not a pause: it is gone with `GOMAXPROCS=4` (p99 86 ms, same throughput).

## Results: one server (2 vCPU, 2 GB, no swap), Hysteria2, 1000 users

What runs inside the 2 vCPU / 2 GB group for each panel:

| Panel | In the group |
|:--|:--|
| Mistgate | panel (native process) and `mistgate-node` (container) |
| 3x-ui | one container: x-ui, the Xray core, fail2ban |
| Remnawave | backend, Postgres, Valkey and `remnawave/node` (Xray core inside) |
| PasarGuard | panel container and `pasarguard/node` (Xray core inside) |

Every server has a self-signed certificate; the client does not verify it (identical for all). Congestion control is BBR on both sides, no bandwidth declared, no obfuscation. The clients are `hyload`, a small program built on the Hysteria 2 client library (`core/v2` v2.12.3); it runs outside the group, with no CPU limit, in a separate cgroup, and its CPU was 150-253 % of one vCPU of the 16 available, while the group was held at 200 % by its quota. The official `hysteria` v2.13.0 binary was used to cross-check that the tunnel works (1.75 GB in 5 s through it).

Phases: A idle with 1000 users (60 s settle, 120 s window); B one tunnel with 1, 16 and 64 parallel downloads (30 s each) with a small request through the same tunnel every 100 ms; C 400 clients connected at once with a small request every 2 s (60 s); D "bad day" for 120 s (64 streams, a subscription storm of 300 requests per second over 1000 source addresses, and one user added and one removed per second through the admin API); E the panel (not the core) restarted 20 s into a 60 s run with 64 streams.

Medians of 3 repetitions, brackets are minimum-maximum. "Private memory" is anon plus kernel memory of the whole group. All four panels reach the 200 % CPU quota in B, so the throughput rows are comparisons of CPU efficiency.

| | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| **A** private memory, whole group (MiB) | 57 (56-57) | 84 (82-84) | 707 (683-775) | 299 (299-301) |
| A: memory.current (MiB) | 64 | 92 | 787 | 316 |
| A: CPU (% of one vCPU, of 200) | 0.24 | 0.64 | 3.70 | 0.35 |
| A: processes / containers | 2 / 1 | 3 / 1 | 36 / 4 | 3 / 2 |
| 1000 users created through the API (s) | 42 (35-56) | 18 | 23 (22-27) | 40 |
| **B** 1 stream (Gbit/s) | 2.94 (2.86-2.96) | 2.30 (2.27-2.31) | 2.15 (2.12-2.19) | 2.28 (2.25-2.29) |
| B: 16 streams (Gbit/s) | 3.43 (3.30-3.49) | 3.08 (3.06-3.09) | 3.00 (2.99-3.01) | 3.10 (3.05-3.14) |
| B: 64 streams (Gbit/s) | 3.29 (3.22-3.32) | 2.96 (2.96-2.97) | 2.81 (2.80-2.82) | 2.96 (2.95-2.98) |
| B: 64 streams, Mbit/s per CPU-second | 1662 | 1488 | 1409 | 1488 |
| B: 64 streams, private memory (MiB) | 66 | 93 | 598 | 315 |
| B: small request through the tunnel, p95 at 64 streams (ms) | 29 (2.3-32) | 1.9 | 2.2 | 2.0 |
| B: client CPU (% of one vCPU, 16 available) | 253 | 240 | 235 | 241 |
| **C** 400 clients: connected / handshake failures | 400 / 0 | 400 / 0 | 400 / 0 | 400 / 0 |
| C: handshake p95 (ms) / request p95 (ms) | 3 / 1.2 | 3 / 1.0 | 3 / 1.0 | 3 / 1.0 |
| C: CPU of the group (% of one vCPU) / private memory (MiB) | 20 / 112 | 19 / 146 | 23 / 689 | 19 / 364 |
| **D** tunnel at 64 streams (Gbit/s) | 2.68 (2.65-2.68) | 1.69 (1.68-1.69) | 1.07 (1.06-1.08) | 1.87 (1.86-1.87) |
| D: drop against B at 64 streams | 19 % | 43 % | 62 % | 37 % |
| D: streams dropped / tunnel reconnects | 0 / 0 | 512 / 5 | 0 / 0 | 0 / 0 |
| D: subscription storm served (requests/s of 300 offered), 200 only | 300 | 79 | 258 | 51 |
| D: subscription p95 (ms) | 30 | 969 | 278 | 1619 |
| D: admin add user p95 / remove user p95 (ms) | 25 / 16 | 823 / 1105 | 86 / 81 | 1928 / 1629 |
| D: admin list call p95 (ms) | 4 | 561 | 79 | 1270 |
| D: private memory (MiB) | 82 | 140 | 1116 | 354 |
| **E** panel restart: admin answers again after (s) | 0.3 | 1.4 | 13.0 | 6.7 |
| E: seconds without tunnel traffic (of 60) / streams dropped | 0 / 0 | 4 / 256 | 0 / 0 | 5 / 128 |
| E: throughput over the run (Gbit/s) | 3.32 | 2.61 | 2.54 | 2.62 |
| Core restarts when users change | no | yes | no | no |

Reading the table:

- Creating users: the harness creates them one API call at a time and waits out `429` answers. Mistgate's admin API allows 30 requests per second with a burst of 200, so the first 200 users take a fraction of a second (0.3 s in the panel-only test) and the remaining 800 are paced by the limit. The 42 s is mostly that pacing, not the cost of creating a user.
- Data plane (B): all four are limited by UDP system calls and encryption on two vCPU and land within 17 % of each other at 64 streams. Mistgate's node moves 12-18 % more data per CPU-second than the Xray-based cores. The 29 ms p95 of Mistgate's small request at 64 streams is two of three repetitions (2.3 ms in the third); the others stay under 3 ms. A request through the tunnel gets its response header in about 2 ms on all four. On the three Xray-based cores the stream stays open for about 1.0 s after the sink has closed its side (time to end of stream: 1002 ms p95 on all three, 32 ms or less on Mistgate); a client that reads until the end of the stream, not until the declared length, pays that second.
- Many clients (C): 400 simultaneous tunnels, no failures anywhere, 20 % of one vCPU or less. Not a differentiator at this size.
- Bad day (D): 3x-ui restarts its Xray core when users change (512 streams dropped and the single client reconnected 5 times in 120 s); the other three add and remove users in the running core without dropping connections. 3x-ui also serves the storm at 79 requests per second (it was offered 300) and takes 0.8-1.1 s per user change; PasarGuard serves 51 per second and takes 1.6-1.9 s. Remnawave loses 62 % of the tunnel speed while its backend (three Node.js workers) serves the storm and the churn on the same two vCPU as the Xray core (not isolated further); its memory in D reaches 1116 MiB.
- Restart (E): restarting the panel container of 3x-ui restarts its Xray (design: the core is a child of the panel process). Restarting PasarGuard's panel container also interrupted the tunnel for 5 s: the node's core is restarted when the panel comes back (observed, mechanism not investigated). Restarting Remnawave's backend left the tunnel untouched but the admin API took 13 s to answer again. Mistgate's panel restarts in 0.3 s and its node keeps serving.

### Cheapest server: 1 vCPU, 1 GB RAM, no swap (one repetition)

Same phases A, B (64 streams shown), D and E, group limited to `CPUQuota=100%` and `MemoryMax=1G`. One repetition per panel only; treat spreads as unknown.

| | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| A private memory (MiB) | 54 | 79 | 815 | 301 |
| B 64 streams (Gbit/s) | 1.68 | 1.48 | 1.37 | 1.52 |
| B: small request p95 (ms) | 67 | 2.5 | 3.3 | 2.4 |
| D tunnel (Gbit/s) / drop against B | 1.17 / 30 % | 0.84 / 43 % | 0.61 / 55 % | 0.95 / 37 % |
| D: storm served (requests/s of 300) / non-200 | 300 / 0 % | 48 / 0 % | 97 / 28.6 % | 26 / 0 % |
| D: admin add user p95 (ms) | 53 | 1407 | 165 | 3717 |
| D: streams dropped | 0 | 512 | 0 | 0 |
| E: admin answers again after (s) | 0.4 | 1.4 | 22.6 | 6.7 |
| E: seconds without tunnel traffic | 0 | 4 | 0 | 5 |

Remnawave runs out of memory on a 1 GB server during D: the kernel killed `rw-api` workers three times (OOM kill, cgroup `bench-box.slice`), 28.6 % of the subscription requests failed with refused or reset connections and 18 admin calls failed. At rest it already uses 815 MiB. It was not given more memory.

### Sensitivity: the two-vCPU group pinned to two host CPUs (one repetition)

The medians above limit the group by quota alone (200 % over 16 host CPUs). The same phases B, D and E with the group also pinned to two host CPUs (`AllowedCPUs=14-15`) and the clients kept on the other 14 give higher and closer throughput, because a quota throttles threads that burst over many CPUs. One repetition per panel.

| | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| B 1 stream (Gbit/s) | 4.15 | 2.83 | 2.72 | 2.80 |
| B 16 streams (Gbit/s) | 4.63 | 3.96 | 3.81 | 3.91 |
| B 64 streams (Gbit/s) / group CPU (% of one vCPU) | 4.55 / 165 | 4.26 / 163 | 4.16 / 166 | 4.29 / 163 |
| D tunnel (Gbit/s) / drop against B64 | 3.96 / 13 % | 2.54 / 40 % | 2.04 / 51 % | 2.18 / 49 % |
| D: streams dropped / reconnects | 0 / 0 | 512 / 7 | 0 / 0 | 0 / 0 |
| D: storm served (requests/s of 300), p95 (ms) | 300, 3 | 110, 714 | 300, 33 | 204, 496 |
| D: admin add user p95 (ms) | 6 | 640 | 19 | 590 |
| E: admin answers again after (s) / seconds without traffic | 0.2 / 0 | 1.5 / 4 | 9.1 / 0 | 6.6 / 5 |

The order of the panels in D and E does not change; at 64 streams the data plane differs by at most 9 % between the four. In this setting the group does not reach its 200 % (about 165 %): the clients, the loopback bridge and the UDP path are the next limit, so B is not a measurement of what a real two-vCPU server would carry.


## Profiling (Mistgate only)

A separate pass with `net/http/pprof` builds (added with `go build -overlay`, the repository is untouched), CPU profiles of 25-30 s, heap/allocs/goroutine/mutex/block profiles and `GODEBUG=gctrace=1`, for the idle panel, the subscription hammer (as shipped, and with limits and cache off), and the one-server phases B64, C and D for the panel and the node. These runs do not feed the numbers above. The full analysis is in [`results/profiling-summary.txt`](https://github.com/Mistgate/mistgate/blob/main/tools/bench/results/profiling-summary.txt); the raw profiles are not published. What it found:

- **The 429 path is already cheap.** About 37 µs of CPU per refused request, 49 % of it in socket system calls. It allocates 3.2 KB per request; 41 % of that is the standard library parsing the request, 19 % two copies of the request made by the panel's own middleware (`withStart`, `WithClientIP`), 25 % response header maps. Anon memory during the refusing was flat at 27 MiB (20 MiB before). The 79 MiB seen at the start of each run comes from hashing the owner password with argon2id (64 MiB) once, not from refusing requests.
- **A subscription render costs 51 µs of CPU from the cache and 614 µs without it** (62 KB and about 900 objects allocated). Half of the uncached CPU is SQLite parsing and planning the same statements again for every request (`sqlite3_prepare_v2`, 49.8 %); 16 % of the allocation is `strings.ToUpper` applied to whole SQL strings to test for a `SELECT` prefix.
- **The 1.7 s p99 of the uncached path is starvation, not a pause.** About 25 requests per second wait over 500 ms, every second of the run. With `GOMAXPROCS=4` the slow requests disappear (p99 86 ms, same throughput). The reader pool has 4 connections and `database/sql` hands a freed connection to a random waiter.
- **Node:** 52 % of CPU is UDP `sendmsg`/`recvmsg` (GSO is in use), 5 % AES-GCM, 19 % QUIC packet building. The hot allocation (37 %) and a contended mutex (`QStream.Write`) are in `apernet/quic-go` and the Hysteria core.
- **GC:** 2-6 collections per second, longest stop-the-world 0.26 ms when the group runs alone on its CPU and 46-49 ms when it is held at its quota in the one-server test.

### After the fixes

Three changes made from these findings are in `main` since v0.1.32 and ship with the next release:

- **Store reads** (`perf(store)`): statements are prepared once per connection and reused, readers are admitted to the 4-connection pool in arrival order, both pools keep all their connections open, and the `SELECT` check no longer upper-cases whole queries.
- **Public requests** (`perf(http)`): one request context instead of two copies, constant response headers from shared slices, no `strings.NewReplacer` per call, pooled gzip writers for Mihomo subscriptions.
- **User changes and passwords** (`perf(fleet,auth)`): the per-node credential digest reuses one buffer per inbound (about 5 times fewer allocations per change at 1000 users; still one pass over all users), and only one argon2id hash runs at a time, so parallel sign-ins no longer stack 64 MiB each.

Measured again on the same machine on 10 October with the same scripts. The machine was 5-25 % slower that day than during the main series, so v0.1.32 was measured again next to the new build (n=1) and the comparison is against that control:

| Mistgate, panel only (1 vCPU, 1 GB) | v0.1.32, main series | v0.1.32, control | after the fixes (n=3) |
|:--|--:|--:|--:|
| Uncached subscription, requests per second (modified build, limits and cache off) | 1,630 | 1,296 | 3,034 (3,022-3,038) |
| Uncached subscription, p50 / p95 / p99 | 4.2 ms / 6.6 ms / 1.70 s | 5.1 ms / 8.4 ms / 2.14 s | 2.1 ms / 4.1 ms / 0.92 s |
| Render cache on, requests per second (modified build) | 19,580 | 13,481 | 15,416 (n=1) |
| Refused (429) answers per second, shipped build | 33,700 | 25,305 | 25,691 (n=1) |
| Idle private memory, 200 users (MiB) | 20 | 20 | 21 |

| Mistgate, one server (2 vCPU, 2 GB) | v0.1.32, main series | v0.1.32, control | after the fixes (n=3) |
|:--|--:|--:|--:|
| D: subscription p95 during the storm (ms) | 30 | 27 | 20 (20-20) |
| D: admin add / remove user p95 (ms) | 25 / 16 | 39 / 32 | 32 / 30 |
| B: 64 streams (Gbit/s) | 3.29 | 2.62 | 2.63 |
| A: private memory (MiB) | 57 | 57 | 57 |

The uncached path is about twice as fast and its p99 halves, but the tail is not gone (0.92 s, not the hoped-for 0.1 s). The storm in D is served with a 25-30 % lower p95. Everything else (the 429 path, the node, the tunnel, memory, restart) is unchanged within the spread, as expected: the fixes did not touch it.

Open work:

1. The remaining 0.92 s tail of the uncached subscription path at `GOMAXPROCS=1`.
2. Investigate the small-request latency inside a loaded tunnel on the node. At 64 parallel streams the p95 of a small request through the tunnel was 29 ms in two of three repetitions (2.3 ms in the third), against about 2 ms for the other three panels; on a 1 vCPU / 1 GB server it was 67 ms against 2.4-3.3 ms (see the tables of phase B and "Cheapest server"). The profiling pass shows a contended mutex in `QStream.Write` (`apernet/quic-go`) and stop-the-world pauses of 46-49 ms when the group is held at its quota; whether either explains the latency is not established.

## Method

### Machine

AMD Ryzen 7 7800X3D (8 cores, 16 threads), 15.5 GB RAM, Windows 11 host, WSL2 with Ubuntu 24.04.4, Linux 6.6.87.2-microsoft-standard-WSL2, Docker 29.4.1 with Compose v5.1.3, systemd cgroup driver, cgroup v2. Other things running in the same WSL instance during the runs: a few other idle containers (load average about 0.05-0.4, recorded before each run in the raw JSON, field `background`). The load generator and the sink ran in the same WSL instance, outside the limited cgroups.

### Limits

- Panel host: one systemd slice `bench-panel.slice` with `CPUQuota=100%`, `MemoryMax=1G`, `MemorySwapMax=0`, and pinned to one host CPU (`AllowedCPUs=15`, set at runtime with `systemctl set-property` before the series; the load generator was not in the slice and not pinned). Docker containers are created with `cgroup_parent: bench-panel.slice` and Mistgate is started with `systemd-run --slice=bench-panel.slice`, so every process of one panel (all containers of Remnawave included) shares the one budget. The slice is stopped and started before every repetition so that `memory.peak` starts from zero.
- One server: `bench-box.slice`, `CPUQuota=200%`, `MemoryMax=2G`, `MemorySwapMax=0`; for the cheapest-server repeat 100 % and 1G.
- The pin was left set after a short comparison made before the series (quota only against quota plus pin); it stayed set for every panel-only run, the Mistgate variants, the cold-cache pass and the profiling of the subscription load, and was noticed only afterwards. It is closer to a one-vCPU server than a quota alone (a quota lets a multi-threaded runtime burst on several host CPUs, then throttles it), but the plan was a quota only, so one repetition without the pin was run for comparison:

| Panel-only, 1 repetition | pinned + quota (series above) | quota only |
|:--|--:|--:|
| Mistgate: anon idle 200 users (MiB) / 429 answers per second | 20 / 33,700 | 24 / 15,750 (p99 77 ms against 7.4) |
| 3x-ui: 200 responses per second / p50 (ms) | 148 / 296 | 162 / 278 |
| PasarGuard: 200 responses per second / p50 (ms) | 274 / 166 | 102 / 467 |
| Remnawave: 200 responses per second / failed requests | 271 / 0 | 251 / 75 % (a worker was killed by the 1 GB limit, `oom_kill` 1) |
| Remnawave: anon during the load (MiB) | 870 | 786 |

  Idle memory, disk, start time and the 3x-ui numbers do not depend on the pin. PasarGuard and Remnawave do, under load: with a quota only, their threads spread over many host CPUs and are throttled by the quota. A real one-vCPU virtual machine behaves like the pinned case.
- The one-server runs used a quota only (`CPUQuota=200%`, not pinned). A single repetition with the group pinned to two host CPUs (`AllowedCPUs=14-15`, clients on the other 14) is reported in the one-server section.

### Measurements

- Memory and CPU: the cgroup files of the slice sampled once a second (`memory.current`, `memory.stat` anon/file/kernel, `cpu.stat`, `pids`). Idle figures: mean of the last 30 s of a 5-minute idle (memory) and the whole idle window (CPU). Peaks are the largest 1 Hz sample inside the window, and the kernel's `memory.peak` of the slice.
- Disk: sum of the `docker images` sizes of the images used (unpacked), or the size of the binary; data directory size after install and after 200 users.
- Cold start: from the start command (`docker compose up -d` or `systemd-run`) to the first HTTP 200 on the main port (Remnawave: `/api/auth/status`, which reads the database).
- Users: created one by one through each panel's documented API (Mistgate Connect-JSON with an owner session, 3x-ui `/panel/api/clients/add`, Remnawave `/api/users`, PasarGuard `/api/user`). Mistgate and the others were given the same invented names `user1` to `user200` (1000 in the one-server test).
- Load: `loadgen` (Go, standard library), 50 workers, round-robin over the links, one warm-up pass, 60 s, latency of 200 responses only; every other status is counted as an error.
- Order: the panels were run in rotation, three times each, with the data wiped before each repetition.

### What was set up for each panel, and where it differs from the project's own instructions

- Mistgate: built from the v0.1.32 tag with the project's reproducible build (`release build`), without a release key, so it cannot update itself or install signed node bundles. Started with `serve --listen 127.0.0.1:18080 --trusted-proxy 127.0.0.1 --agent-listen <bridge address>`. A node agent had to connect once (in a container on an isolated network) because a link carries no server line until a node is active; the container is removed before measuring.
- 3x-ui: the published image instead of the compose file's `build:`; ports bound to 127.0.0.1; default `admin/admin`; one VLESS Reality inbound.
- Remnawave: the official compose file with the backend image pinned from `:3` to `3.4.5`; `X-Forwarded-For` and `X-Forwarded-Proto: https` sent on every request (the backend refuses requests that did not pass a TLS proxy); API token created from the dashboard session (`x-remnawave-client-type: browser`); the default config profile (one Shadowsocks inbound) and the default squad; one host.
- PasarGuard: the project's default compose file (host networking), image pinned to v5.4.1, `UVICORN_HOST=127.0.0.1`; default core config (one Shadowsocks inbound); one host and one group; owner created with the temporary key from `pasarguard-cli generate-temp-key`.
- Nothing was tuned. TLS is not used on any panel (plain HTTP on loopback), although a real installation puts a TLS proxy in front.

### Reproduce

The scripts, compose files, the diff of the modified builds, the load generators and the aggregated results are in [`tools/bench`](https://github.com/Mistgate/mistgate/tree/main/tools/bench) in the repository, with a README: `py/bench.py` (panel only), `py/onebox.py` (one server), `py/profile_run.py` (profiling), `py/aggregate*.py`, `loadgen/`, `hyload/`, `compose/`, `profiling/` (the pprof overlay) and `results/` (tables and per-repetition CSVs). The raw JSON and the 1 Hz samples are not published. The harness creates and removes Docker images and systemd slices: run it on a test machine and read the README first.

## What the numbers do not tell you

- One desktop CPU, one WSL2 VM, three repetitions. Spreads are small, but a different CPU, kernel or filesystem will move absolute values.
- The load is a closed loop with 50 clients: it shows the cost of a request, not the number of users a panel can serve. A real client fetches a subscription every few hours.
- The panels do different work per request and serve different links (Hysteria2, VLESS Reality, Shadowsocks). Remnawave and PasarGuard render a Shadowsocks line from a stored password; 3x-ui renders a VLESS Reality line; Mistgate renders a Hysteria2 line with a pinned certificate.
- Mistgate answers subscription requests from a 10 s cache of the user's data; the others do not (or do not say so). The cache-off variant above shows the effect.
- Databases differ: Remnawave needs PostgreSQL, the rest use SQLite. PasarGuard also supports PostgreSQL and MySQL; only its default was measured.
- Mistgate was measured with a node record whose agent is gone; the others with no node. The one-server test is the comparison that includes the core.
- Page cache: Mistgate's binary is copied by another cgroup, so its file cache is not charged to the group at start, while the other panels read their files under the group. This is why anon is the headline. A second pass with dropped caches is described above.
- Not measured: VPN data-plane speed beyond Hysteria2 on loopback-like links (the sink and the clients are on the same machine, no network), other protocols, the admin web interface, Telegram bots, upgrade and backup, many nodes, high-latency or lossy paths, TLS in front of the panel, and anything over a week.
