---
title: Benchmarks
description: Memory, CPU, disk, start time and subscription load of Mistgate, 3x-ui, Remnawave and PasarGuard on the same small server, and a one-server test with the VPN core and Hysteria2 traffic. Method, versions and limits included.
---

## Summary

- **Memory is the clear difference.** With 0 to 200 users the Mistgate panel alone holds 16-20 MiB of private memory; 3x-ui holds 68-74, PasarGuard 262-265 and Remnawave 426-454. With the VPN core and the database on one server: 57 MiB against 74, 297 and 761.
- **Users and restarts do not touch the tunnel.** Adding and removing users and restarting the panel under a running Hysteria2 tunnel dropped no connection in Mistgate. 3x-ui restarts its Xray core when users change.
- **Tunnel speed is not claimed either way.** The first full run with the corrected harness was disturbed by other load on the machine (the repetitions of one panel differ by up to a factor of three), so phase B and the tunnel part of phase D are not published and are being measured again on dedicated cores (see [Results: one server](#results-one-server-2-vcpu-2-gb-no-swap-hysteria2-1000-users)). The earlier figures (all four within 17 % of each other) were taken with the harness fault described in [After the fixes](#after-the-fixes) and are withdrawn.
- **Weak spots of Mistgate.** The 29 ms p95 of a small request inside a loaded tunnel (67 ms on 1 vCPU), against about 2 ms for the others, was traced to the test setup (see [After the fixes](#after-the-fixes)); whether any gap remains with the corrected harness is part of the re-measurement of the tunnel, so the page makes no latency claim yet. A subscription render that missed the cache was slow (1,630 requests per second on one vCPU in a modified build, p99 1.7 s). Three fixes found by profiling, shipped in v0.1.33, roughly double the uncached path; see [After the fixes](#after-the-fixes). Creating 1000 users through the API took 49 s against 24 s for 3x-ui and Remnawave, mostly because of the admin API rate limit (see the note under the one-server table).
- **Subscription throughput of the shipped build is not measured:** it allows 60 fetches per link and hour, so the load test only measured how fast it refuses.
- One desktop CPU, one WSL2 machine, three repetitions: read [What the numbers do not tell you](#what-the-numbers-do-not-tell-you) before quoting any figure.

Panel-only tables: measured on 9 and 10 October 2026 (Mistgate v0.1.32). One-server tables: measured again on 12 October 2026 (Mistgate v0.1.33, corrected harness). Numbers are medians of 3 repetitions with the minimum and maximum in brackets, unless a cell says n=1. Everything ran on one desktop machine inside WSL2. The scripts, compose files and aggregated results are in [`tools/bench`](https://github.com/Mistgate/mistgate/tree/main/tools/bench) in the repository.

## What was measured

Four panels, each installed as its own documentation describes, with its defaults:

| Panel | Version | How it was installed | Database | Processes in the measured group |
|:--|:--|:--|:--|:--|
| Mistgate | v0.1.32 for the panel-only tables, v0.1.33 for the one-server tables (built from the tag, no release key) | one static binary, `mistgate setup`, `mistgate serve` | SQLite | 1 |
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

Every server has a self-signed certificate; the client does not verify it (identical for all). Congestion control is BBR on both sides, no bandwidth declared, no obfuscation. The clients are `hyload`, a small program built on the Hysteria 2 client library (`core/v2` v2.12.3); it runs outside the group, with no CPU limit, in a separate cgroup, and its CPU was 150-340 % of one vCPU of the 16 available, while the group was held at 200 % by its quota. Every core container (Mistgate's node, the Xray cores of the other three) runs with `GOMAXPROCS` equal to the group's vCPUs, which is what the Go runtime picks on a real server with that many vCPUs; without it a container sizes `GOMAXPROCS` by the host's 16 CPUs (see [After the fixes](#after-the-fixes)). The official `hysteria` v2.13.0 binary was used to cross-check that the tunnel works (1.75 GB in 5 s through it).

Phases: A idle with 1000 users (60 s settle, 120 s window); B one tunnel with 1, 16 and 64 parallel downloads (30 s each) with a small request through the same tunnel every 100 ms (measured, not published, see below); C 400 clients connected at once with a small request every 2 s (60 s); D "bad day" for 120 s (64 streams, a subscription storm of 300 requests per second over 1000 source addresses, and one user added and one removed per second through the admin API); E the panel (not the core) restarted 20 s into a 60 s run with 64 streams.

Medians of 3 repetitions, brackets are minimum-maximum. "Private memory" is anon plus kernel memory of the whole group.

**What is not in the table.** Phase B (one tunnel with 1, 16 and 64 streams), the tunnel half of phase D (throughput, the drop against B, the small request through the tunnel) and the throughput of phase E are left out on purpose. Run on the corrected harness, the same panel gave 1.5 to 4.2 Gbit/s at 64 streams in three consecutive repetitions, and the slow runs coincide with a client that got only 177-185 % of a CPU instead of about 320 %: other work on the same machine took CPU from the load generator. That is interference, not a property of the panels, so no ranking of the data plane is published until it is measured again with the group pinned to two dedicated physical cores and the clients on other cores. The figures of the first series (all four within 17 %) were taken before the harness fix and are withdrawn. Everything below does not depend on how fast the tunnel runs.

| | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| **A** private memory, whole group (MiB) | 57 (56-58) | 74 (72-77) | 761 (710-772) | 297 (297-299) |
| A: memory.current (MiB) | 64 (62-64) | 81 (79-84) | 841 (790-852) | 312 (312-315) |
| A: CPU (% of one vCPU, of 200) | 0.26 | 0.68 | 4.34 | 0.39 |
| A: processes / containers | 2 / 1 | 3 / 1 | 36 / 4 | 3 / 2 |
| 1000 users created through the API (s) | 49 (33-49) | 24 (19-27) | 24 (23-26) | 45 (44-45) |
| **C** 400 clients: connected / handshake failures | 400 / 0 | 400 / 0 | 400 / 0 | 400 / 0 |
| C: handshake p95 (ms) / request p95 (ms) | 4 / 1.3 | 4 / 1.2 | 3 / 1.2 | 6 / 1.6 |
| C: CPU of the group (% of one vCPU) / private memory (MiB) | 18 / 113 | 18 / 137 | 21 / 691 | 21 / 361 |
| **D** streams dropped / tunnel reconnects | 0 / 0 | 512 (384-523) / 6 (4-9) | 0 / 0 | 0 / 0 |
| D: subscription storm served (requests/s of 300 offered), 200 only | 300 | 94 (92-96) | 250 (213-282) | 51 (49-57) |
| D: subscription p95 (ms) | 1 | 792 (791-816) | 288 (211-308) | 1814 (1522-1954) |
| D: admin add user p95 / remove user p95 (ms) | 4 / 3 | 801 / 1138 | 51 / 83 | 2039 / 2216 |
| D: admin list call p95 (ms) | 4 | 502 | 79 | 1609 |
| D: private memory (MiB) | 80 | 129 | 1114 (1060-1130) | 347 |
| **E** panel restart: admin answers again after (s) | 0.2 | 1.6 (1.5-1.6) | 14.5 (11.1-15.4) | 8.3 (7.4-11.7) |
| E: seconds without tunnel traffic (of 60) / streams dropped | 0 / 0 | 5 (4-8) / 256 (192-256) | 0 / 0 | 8 (5-9) / 128 (128-192) |
| Core restarts when users change | no | yes | no | no |

Reading the table:

- Memory (A): 57 MiB for Mistgate's panel and node against 74, 297 and 761 MiB. Remnawave is 36 processes in 4 containers (backend with three Node.js workers, Postgres, Valkey, the node).
- Creating users: the harness creates them one API call at a time and waits out `429` answers. Mistgate's admin API allows 30 requests per second with a burst of 200, so the first 200 users take a fraction of a second (0.3 s in the panel-only test) and the remaining 800 are paced by the limit. The 49 s is mostly that pacing, not the cost of creating a user.
- Many clients (C): 400 simultaneous tunnels, no failures anywhere, 21 % of one vCPU or less. Not a differentiator at this size. PasarGuard's handshake p95 of 4-20 ms is the only visible spread.
- Bad day (D): the tunnel carries 64 streams while 300 subscription requests per second arrive from 1000 addresses and one user is added and one removed per second. 3x-ui restarts its Xray core when users change (512 streams dropped in the median and the single client reconnected 6 times in 120 s); the other three add and remove users in the running core without dropping connections. 3x-ui also serves the storm at 94 requests per second (it was offered 300) and takes 0.8-1.1 s per user change; PasarGuard serves 51 per second and takes 2.0-2.2 s; Remnawave serves 250 per second at a 288 ms p95 and its memory in D reaches 1114 MiB (three Node.js workers on the same two vCPU as the Xray core). Mistgate serves all 300 per second at a 1 ms p95 and changes a user in 3-4 ms.
- Restart (E): restarting the panel container of 3x-ui restarts its Xray (design: the core is a child of the panel process). Restarting PasarGuard's panel container also interrupted the tunnel for 5-9 s: the node's core is restarted when the panel comes back (observed, mechanism not investigated). Restarting Remnawave's backend left the tunnel untouched but the admin API took 11-15 s to answer again. Mistgate's panel restarts in 0.2 s and its node keeps serving.
- The group is held at its quota in D (183-197 % of one vCPU for all four), so the control-plane rows compare how much of a saturated box each panel still has for its own work.

### Cheapest server: 1 vCPU, 1 GB RAM, no swap (one repetition)

Same phases A, B, D and E, group limited to `CPUQuota=100%` and `MemoryMax=1G`, Mistgate v0.1.33 and the corrected harness (`GOMAXPROCS=1` in the cores), run on 12 October. One repetition per panel only; treat spreads as unknown. As in the table above, the tunnel rows of B and D (throughput, the small request through the tunnel) are not published: this run shared the machine with other load too, and a single repetition cannot be told from interference.

| | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| A private memory (MiB) | 73 | 75 | 820 | 302 |
| D: streams dropped | 0 | 384 | 0 | 0 |
| D: storm served (requests/s of 300) / non-200 | 300 / 0 % | 74 / 0 % | 100 / 31.2 % | 39 / 0 % |
| D: subscription p95 (ms) | 6 | 1002 | 496 | 2603 |
| D: admin add user p95 (ms) | 9 | 1340 | 97 | 2921 |
| E: admin answers again after (s) | 0.3 | 1.5 | 15.2 | 7.3 |
| E: seconds without tunnel traffic | 0 | 4 | 2 | 5 |

Remnawave runs out of memory on a 1 GB server during D: the kernel killed `rw-api` workers five times and an `rw-processor` once (OOM kill, cgroup `bench-box.slice`, counted in the kernel log for this repetition), 31.2 % of the subscription requests failed with refused or reset connections and 23 admin calls failed. At rest it already uses 820 MiB. It was not given more memory.

Mistgate's idle memory on this server, 73 MiB, is higher than the 54 MiB of the first series and than its 57 MiB on the 2 vCPU server. The panel's private memory keeps rising for the first minutes after the 1000 users are created (in the 2 vCPU run from 45 to 63 MiB over four minutes, in this run from 61 to 78 MiB), so the idle figure depends on where the window falls; this run settled for 30 s, the 2 vCPU runs for 60 s. The reason for the rise was not investigated.

## Profiling (Mistgate only)

A separate pass with `net/http/pprof` builds (added with `go build -overlay`, the repository is untouched), CPU profiles of 25-30 s, heap/allocs/goroutine/mutex/block profiles and `GODEBUG=gctrace=1`, for the idle panel, the subscription hammer (as shipped, and with limits and cache off), and the one-server phases B64, C and D for the panel and the node. These runs do not feed the numbers above. The full analysis is in [`results/profiling-summary.txt`](https://github.com/Mistgate/mistgate/blob/main/tools/bench/results/profiling-summary.txt); the raw profiles are not published. What it found:

- **The 429 path is already cheap.** About 37 µs of CPU per refused request, 49 % of it in socket system calls. It allocates 3.2 KB per request; 41 % of that is the standard library parsing the request, 19 % two copies of the request made by the panel's own middleware (`withStart`, `WithClientIP`), 25 % response header maps. Anon memory during the refusing was flat at 27 MiB (20 MiB before). The 79 MiB seen at the start of each run comes from hashing the owner password with argon2id (64 MiB) once, not from refusing requests.
- **A subscription render costs 51 µs of CPU from the cache and 614 µs without it** (62 KB and about 900 objects allocated). Half of the uncached CPU is SQLite parsing and planning the same statements again for every request (`sqlite3_prepare_v2`, 49.8 %); 16 % of the allocation is `strings.ToUpper` applied to whole SQL strings to test for a `SELECT` prefix.
- **The 1.7 s p99 of the uncached path is starvation, not a pause.** About 25 requests per second wait over 500 ms, every second of the run. With `GOMAXPROCS=4` the slow requests disappear (p99 86 ms, same throughput). The reader pool has 4 connections and `database/sql` hands a freed connection to a random waiter.
- **Node:** 52 % of CPU is UDP `sendmsg`/`recvmsg` (GSO is in use), 5 % AES-GCM, 19 % QUIC packet building. The hot allocation (37 %) and a contended mutex (`QStream.Write`) are in `apernet/quic-go` and the Hysteria core.
- **GC:** 2-6 collections per second, longest stop-the-world 0.26 ms when the group runs alone on its CPU and 46-49 ms when it is held at its quota in the one-server test.

### After the fixes

Three changes made from these findings are in v0.1.33 (and the fourth one below, too):

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

| Mistgate, one server (2 vCPU, 2 GB) | v0.1.32, main series | v0.1.32, control | after the first three fixes (n=3) | v0.1.33, corrected harness (n=3, 12 October) |
|:--|--:|--:|--:|--:|
| D: subscription p95 during the storm (ms) | 30 | 27 | 20 (20-20) | 1 (1-1) |
| D: admin add / remove user p95 (ms) | 25 / 16 | 39 / 32 | 32 / 30 | 4 / 3 |
| A: private memory (MiB) | 57 | 57 | 57 | 57 (56-58) |

The first three columns were measured with the harness that left Go inside the containers to size `GOMAXPROCS` by the host's CPUs (see below), the last one with the corrected harness and the fourth fix, so the step from 20 ms to 1 ms is the sum of both changes and was not separated. The other three panels moved little between the two series (for example the storm p95 of 3x-ui 969 ms before, 792 ms now; PasarGuard 1619 and 1814 ms), so the harness change does not explain the difference between panels. The throughput row of this table is dropped: it was taken with the old harness.

The uncached path is about twice as fast and its p99 halves, but the tail is not gone (0.92 s, not the hoped-for 0.1 s; see below). The storm in D is served with a 25-30 % lower p95. Everything else (the 429 path, memory, restart) is unchanged within the spread, as expected: the fixes did not touch it.

The remaining tail had another cause. A `database/sql` transaction starts a goroutine per transaction and per query, and the SQLite driver one per statement, to watch a cancellable context: about 25 per subscription fetch. On one CPU that overflowed the Go scheduler's local run queue into the global one, which a busy processor drains once in 61 rounds, so a connection whose HTTP read goroutine landed there waited up to a second. A fourth change (`perf(store)`, read batches on their own connections without context watchers) removes them. Same machine, same session, uncached variant, 3 repetitions:

| Mistgate, panel only (1 vCPU, 1 GB) | main before the change (control) | with the change |
|:--|--:|--:|
| Uncached subscription, requests per second | 4,066 | 4,580 |
| p50 / p95 / p99 | 1.6 / 3.1 / 682 ms | 11.0 / 20.8 / 23.5 ms |
| Slowest request | 780 ms | 32 ms |

The machine was faster that day, so compare the columns with each other, not with the tables above. The higher p50 is the fair share of 50 clients that never pause on a saturated server (50 / 4,580 per second = 10.9 ms); before, a few stalled clients left the others an idle server.

The small-request latency inside a loaded tunnel (phase B: 29 ms at p95 in two of three repetitions, 2.3 ms in the third; 67 ms on 1 vCPU) turned out to come from the test setup, not from the node. The quota of the one-server group is set on the systemd slice, and a container with its own cgroup namespace does not see it, so the node's Go runtime sized `GOMAXPROCS` by the host's CPUs, used up the 2-CPU quota about 80 ms into every 100 ms period and stood still for the rest of it (19 % of the time in a runtime trace). A probe every 100 ms always met the same phase of that period, either beside the pause or in it, hence the two levels. With `GOMAXPROCS` equal to the group's vCPUs, which is what the runtime picks on a real server, the node never stalls. Same binary and session, 64 streams, a probe every 97 ms so the phase drifts: p95 14.7-18.2 ms before, 3.3-3.6 ms after, and 4.2 Gbit/s became 5.4. The harness in `tools/bench` now gives every core container (Mistgate's node and the Xray cores of the other panels) `GOMAXPROCS` = vCPUs. The one-server tables were measured again on 12 October on v0.1.33 with this harness, all four panels, three repetitions. The memory, many-clients, bad-day control-plane and restart rows are in the table above. The tunnel rows are not: that run shared the machine with other load, one panel's 64-stream result swung between 1.5 and 4.2 Gbit/s from one repetition to the next, and the two slowest repetitions are the two where the load generator got only 177-185 % of a CPU. Mistgate's small-request p95 at 64 streams in that run was 7.9, 5.1 and 53 ms (medians of the others: 3x-ui 4.2, Remnawave 4.3, PasarGuard 13), which is neither the stable 29 ms of the first series nor a clean result. A second, shorter pass with a probe every 97 ms (phase B only, one repetition per panel) gave 5.1 ms for Mistgate at 64 streams against 2.9-3.2 ms for the other three, but Mistgate's 1- and 16-stream runs in that pass were disturbed (the client had 149 % of a CPU, p95 20 and 16 ms), so one repetition each settles nothing. No claim about tunnel latency or speed is made until the quiet re-run listed under open work.

Open work:

1. Measure phase B and the tunnel part of phase D again for all four panels on a quiet machine: the group pinned to two dedicated physical cores (`AllowedCPUs=0,1,8,9`, `CPUQuota=200%`, `GOMAXPROCS=2`), the clients and the sink on the other cores, three repetitions, a probe every 97 ms. This is also the replacement for the pinned-sensitivity table of the first series, which was measured with the old harness and has been removed.

## Method

### Machine

AMD Ryzen 7 7800X3D (8 cores, 16 threads), 15.5 GB RAM, Windows 11 host, WSL2 with Ubuntu 24.04.4, Linux 6.6.87.2-microsoft-standard-WSL2, Docker 29.4.1 with Compose v5.1.3, systemd cgroup driver, cgroup v2. Other things running in the same WSL instance during the runs: a few other idle containers (load average about 0.05-0.4, recorded before each run in the raw JSON, field `background`). The load generator and the sink ran in the same WSL instance, outside the limited cgroups.

### Limits

- Panel host: one systemd slice `bench-panel.slice` with `CPUQuota=100%`, `MemoryMax=1G`, `MemorySwapMax=0`, and pinned to one host CPU (`AllowedCPUs=15`, set at runtime with `systemctl set-property` before the series; the load generator was not in the slice and not pinned). Docker containers are created with `cgroup_parent: bench-panel.slice` and Mistgate is started with `systemd-run --slice=bench-panel.slice`, so every process of one panel (all containers of Remnawave included) shares the one budget. The slice is stopped and started before every repetition so that `memory.peak` starts from zero.
- One server: `bench-box.slice`, `CPUQuota=200%`, `MemoryMax=2G`, `MemorySwapMax=0`; for the cheapest-server repeat 100 % and 1G. Every core container (Mistgate's node, the Xray cores of the other panels) gets `GOMAXPROCS` equal to the vCPUs of the slice, because a container does not see the slice's quota and would otherwise size it by the host's 16 CPUs (see [After the fixes](#after-the-fixes)).
- The pin was left set after a short comparison made before the series (quota only against quota plus pin); it stayed set for every panel-only run, the Mistgate variants, the cold-cache pass and the profiling of the subscription load, and was noticed only afterwards. It is closer to a one-vCPU server than a quota alone (a quota lets a multi-threaded runtime burst on several host CPUs, then throttles it), but the plan was a quota only, so one repetition without the pin was run for comparison:

| Panel-only, 1 repetition | pinned + quota (series above) | quota only |
|:--|--:|--:|
| Mistgate: anon idle 200 users (MiB) / 429 answers per second | 20 / 33,700 | 24 / 15,750 (p99 77 ms against 7.4) |
| 3x-ui: 200 responses per second / p50 (ms) | 148 / 296 | 162 / 278 |
| PasarGuard: 200 responses per second / p50 (ms) | 274 / 166 | 102 / 467 |
| Remnawave: 200 responses per second / failed requests | 271 / 0 | 251 / 75 % (a worker was killed by the 1 GB limit, `oom_kill` 1) |
| Remnawave: anon during the load (MiB) | 870 | 786 |

  Idle memory, disk, start time and the 3x-ui numbers do not depend on the pin. PasarGuard and Remnawave do, under load: with a quota only, their threads spread over many host CPUs and are throttled by the quota. A real one-vCPU virtual machine behaves like the pinned case.
- The one-server runs used a quota only (`CPUQuota=200%`, not pinned). A run with the group pinned to two physical cores and the clients on the others is open work (see the end of [After the fixes](#after-the-fixes)); the single pinned repetition of the first series, taken with the old harness, was removed.

### Measurements

- Memory and CPU: the cgroup files of the slice sampled once a second (`memory.current`, `memory.stat` anon/file/kernel, `cpu.stat`, `pids`). Idle figures: mean of the last 30 s of a 5-minute idle (memory) and the whole idle window (CPU). Peaks are the largest 1 Hz sample inside the window, and the kernel's `memory.peak` of the slice.
- Disk: sum of the `docker images` sizes of the images used (unpacked), or the size of the binary; data directory size after install and after 200 users.
- Cold start: from the start command (`docker compose up -d` or `systemd-run`) to the first HTTP 200 on the main port (Remnawave: `/api/auth/status`, which reads the database).
- Users: created one by one through each panel's documented API (Mistgate Connect-JSON with an owner session, 3x-ui `/panel/api/clients/add`, Remnawave `/api/users`, PasarGuard `/api/user`). Mistgate and the others were given the same invented names `user1` to `user200` (1000 in the one-server test).
- Load: `loadgen` (Go, standard library), 50 workers, round-robin over the links, one warm-up pass, 60 s, latency of 200 responses only; every other status is counted as an error.
- Order: the panels were run in rotation, three times each, with the data wiped before each repetition.

### What was set up for each panel, and where it differs from the project's own instructions

- Mistgate: built from the v0.1.32 tag (panel-only tables) or the v0.1.33 tag (one-server tables) with the project's reproducible build (`release build`), without a release key, so it cannot update itself or install signed node bundles. Started with `serve --listen 127.0.0.1:18080 --trusted-proxy 127.0.0.1 --agent-listen <bridge address>`. A node agent had to connect once (in a container on an isolated network) because a link carries no server line until a node is active; the container is removed before measuring.
- 3x-ui: the published image instead of the compose file's `build:`; ports bound to 127.0.0.1; default `admin/admin`; one VLESS Reality inbound.
- Remnawave: the official compose file with the backend image pinned from `:3` to `3.4.5`; `X-Forwarded-For` and `X-Forwarded-Proto: https` sent on every request (the backend refuses requests that did not pass a TLS proxy); API token created from the dashboard session (`x-remnawave-client-type: browser`); the default config profile (one Shadowsocks inbound) and the default squad; one host.
- PasarGuard: the project's default compose file (host networking), image pinned to v5.4.1, `UVICORN_HOST=127.0.0.1`; default core config (one Shadowsocks inbound); one host and one group; owner created with the temporary key from `pasarguard-cli generate-temp-key`.
- Nothing was tuned. TLS is not used on any panel (plain HTTP on loopback), although a real installation puts a TLS proxy in front.

### Reproduce

The scripts, compose files, the diff of the modified builds, the load generators and the aggregated results are in [`tools/bench`](https://github.com/Mistgate/mistgate/tree/main/tools/bench) in the repository, with a README: `py/bench.py` (panel only), `py/onebox.py` (one server), `py/profile_run.py` (profiling), `py/aggregate*.py`, `loadgen/`, `hyload/`, `compose/`, `profiling/` (the pprof overlay) and `results/` (tables and per-repetition CSVs). The raw JSON and the 1 Hz samples are not published. The harness creates and removes Docker images and systemd slices: run it on a test machine and read the README first.

## What the numbers do not tell you

- One desktop CPU, one WSL2 VM, three repetitions. Spreads are small in the panel-only tables and in the control-plane rows of the one-server table, but a different CPU, kernel or filesystem will move absolute values. The machine was not dedicated: during the one-server series other work ran on it, which is why its tunnel throughput rows are withheld (see the note in that section).
- The load is a closed loop with 50 clients: it shows the cost of a request, not the number of users a panel can serve. A real client fetches a subscription every few hours.
- The panels do different work per request and serve different links (Hysteria2, VLESS Reality, Shadowsocks). Remnawave and PasarGuard render a Shadowsocks line from a stored password; 3x-ui renders a VLESS Reality line; Mistgate renders a Hysteria2 line with a pinned certificate.
- Mistgate answers subscription requests from a 10 s cache of the user's data; the others do not (or do not say so). The cache-off variant above shows the effect.
- Databases differ: Remnawave needs PostgreSQL, the rest use SQLite. PasarGuard also supports PostgreSQL and MySQL; only its default was measured.
- Mistgate was measured with a node record whose agent is gone; the others with no node. The one-server test is the comparison that includes the core.
- Page cache: Mistgate's binary is copied by another cgroup, so its file cache is not charged to the group at start, while the other panels read their files under the group. This is why anon is the headline. A second pass with dropped caches is described above.
- Not measured: VPN data-plane speed beyond Hysteria2 on loopback-like links (the sink and the clients are on the same machine, no network), other protocols, the admin web interface, Telegram bots, upgrade and backup, many nodes, high-latency or lossy paths, TLS in front of the panel, and anything over a week.
