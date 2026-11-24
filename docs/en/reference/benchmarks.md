---
title: Benchmarks
description: Memory, CPU, disk, start time and subscription load of Mistgate, 3x-ui, Remnawave and PasarGuard on the same small server, and a one-server test with the VPN core and Hysteria2 traffic. Method, versions and limits included.
---

## Summary

- **Memory.** Alone on a 1 vCPU / 1 GB panel host, with 0 to 200 users, the Mistgate panel holds 39-44 MiB of private memory, 3x-ui 73-76, PasarGuard 273-277 and Remnawave 436-464. Mistgate is still the smallest, but it is 23 MiB heavier than in October (16-20) and its lead over 3x-ui shrank from 3.7 to 1.8 times. With the VPN core and the database on one server the first two change places: 92 MiB for Mistgate, 81 for 3x-ui, 312 for PasarGuard and 815 for Remnawave (October: 57, 74, 297, 761). The panel binary grew from 62 to 112 MB.
- **Users and restarts do not touch the tunnel.** Adding and removing users and restarting the panel under a running Hysteria2 tunnel dropped no connection in Mistgate or in Remnawave. 3x-ui restarts its Xray core when users change (512 streams dropped on the bad day) and when its container restarts (128); PasarGuard dropped 128 streams when its panel container restarted.
- **Tunnel speed is measured this time, on a quiet machine, with and without the core pinned to two physical cores.** With 16 and 64 parallel downloads all four panels reach 5.5 to 6.1 Gbit/s on the 2 vCPU box and Mistgate is 3 to 10 % ahead; with one stream Mistgate carries 5.7 Gbit/s against 3.7 to 4.0 for the others. On the bad day (64 streams while 300 subscription requests per second and one user change per second hit the panel) the tunnel keeps 87-89 % of its speed with Mistgate, 65-66 % with 3x-ui, 53-61 % with PasarGuard and 30-47 % with Remnawave. The repetitions of one panel are within 10 % of each other in every throughput cell, so the numbers are published (see [The tunnel: phases B and D](#the-tunnel-phases-b-and-d)).
- **Weak spots of Mistgate.** The growth in memory and disk above; where it comes from, and what is already fixed in the main branch, is in [its own section](#where-the-growth-between-v0132-and-v021-comes-from). A small request inside a loaded tunnel waits 0.6-0.9 ms longer at p95 than with the others (1.9-3.3 ms against 1.1-2.8; the 29 ms of October was a fault of the test setup, see [After the fixes](#after-the-fixes)). Cold start doubled (0.33 to 0.66 s; still the fastest). Creating 1000 users through the API took 38 s, twice the 18 s of 3x-ui (Remnawave 26, PasarGuard 40), mostly because of the admin API rate limit (see the note under the one-server table). The uncached subscription path, which was slow in October, was not measured again on v0.2.1.
- **Subscription throughput of the shipped build is not measured:** it allows 60 fetches per link and hour, so the load test only measured how fast it refuses.
- One desktop CPU, one WSL2 machine, three repetitions: read [What the numbers do not tell you](#what-the-numbers-do-not-tell-you) before quoting any figure.

All tables of this page were measured on 24 November 2026 with the latest stable release of each panel (Mistgate v0.2.1, 3x-ui v3.11.0, Remnawave 3.4.11, PasarGuard v5.6.0); the sections that were not repeated say so and give their October date. Numbers are medians of 3 repetitions with the minimum and maximum in brackets, unless a cell says n=1. Everything ran on one desktop machine inside WSL2. The scripts, compose files and aggregated results are in [`tools/bench`](https://github.com/Mistgate/mistgate/tree/main/tools/bench) in the repository.

## What was measured

Four panels, each installed as its own documentation describes, with its defaults. Each is the latest stable release of its project on 24 November 2026; the last column is the version of the October run.

| Panel | Version | How it was installed | Database | Processes in the measured group | October |
|:--|:--|:--|:--|:--|:--|
| Mistgate | v0.2.1 (built from the tag, no release key) | one static binary, `mistgate setup`, `mistgate serve` | SQLite | 1 | v0.1.32 (panel only), v0.1.33 (one server) |
| 3x-ui | v3.11.0 (Xray 26.10.21) | published image `ghcr.io/mhsanaei/3x-ui:v3.11.0`, project compose file | SQLite | 3: x-ui, the Xray core it supervises, fail2ban | v3.9.0 |
| Remnawave | panel 3.4.11, node 3.4.6 | official `docker-compose-prod.yml` (backend, Postgres 18.4, Valkey 9), image pinned | PostgreSQL | 21 to 23 in 3 containers | 3.4.5, node 3.4.2 |
| PasarGuard | v5.6.0, node v0.5.7 | the project's default (SQLite) compose file | SQLite | 1 (Python) | v5.4.1, node v0.5.4 |

Two scenarios:

1. **Panel only**: a small panel host, 1 vCPU and 1 GB RAM with no swap, no VPN traffic. Idle memory and CPU, disk, start time, creating 200 users through the API, and a subscription load (50 clients, 60 s, 200 users).
2. **One server**: panel, VPN core (or node) and the panel's database all on one box of 2 vCPU and 2 GB RAM with no swap, 1000 users, Hysteria2 for every panel. Run twice: with a CPU quota only (phases A to E), and with the box pinned to two physical cores and the clients on the others (phases B and D, the open work of the October page).

## What changed since the October run

- **Versions.** All four panels are newer (table above). Mistgate went from v0.1.32 / v0.1.33 to v0.2.1; the supplementary builds with the limits switched off, the profiling pass and the cold-cache pass were built on v0.1.32 and were **not** repeated. Their sections below are the October ones and say so.
- **The machine was quiet.** Before the October series other work on the same computer disturbed the tunnel runs, and their throughput rows were withdrawn. This time nothing else ran (the whole machine was 22 to 33 % busy in the tunnel runs, which is the group, the clients and nothing else), so the tunnel rows are published, with their spread.
- **Pinned run.** The open work of the October page is done: phase B and the tunnel part of phase D with the group on two dedicated physical cores (as far as WSL shows them, see [What the numbers do not tell you](#what-the-numbers-do-not-tell-you)), the clients and the sink on the other cores, three repetitions, a probe every 97 ms.
- **Harness.** Three changes were forced by the new versions: 3x-ui no longer has `admin/admin` (a new database gets a random first user that is printed once to the log, and the harness reads it from there); the Mistgate harness waits until the node has the last user before it measures (the subscription shows the server line a few seconds before the node accepts every user), and refreshes the owner's step-up, which deleting users now needs every 5 minutes. The method of measuring did not change.
- **What moved, in one table** (medians; October on the left of each pair):

| | Mistgate | 3x-ui | PasarGuard | Remnawave |
|:--|--:|--:|--:|--:|
| Panel only, idle, 0 / 200 users, private memory (MiB) | 16 / 20 → **39 / 44** | 68 / 74 → 73 / 76 | 262 / 265 → 273 / 277 | 426 / 454 → 436 / 464 |
| One server, idle, private memory (MiB) | 57 → **92** | 74 → 81 | 297 → 312 | 761 → 815 |
| Panel binary or images on disk (MB) | 62 → **112** | 462 → 456 | 462 → 487 | 1551 → 1537 |
| Cold start, empty data (s) | 0.33 → **0.66** | 2.12 → 2.27 | 11.6 → 11.4 | 14.2 → 13.9 |

  **Mistgate's panel got bigger, in memory and on disk.** Alone it is still the smallest (39-44 MiB against 73-76 for 3x-ui), but the lead over 3x-ui shrank from 3.7 times to 1.8 times, and on one server 3x-ui is now 11 MiB below it. The memory did not grow because of the Xray core or the SDK of the AI assistant: builds without them differ from the full one by 1-2 MiB, which is within the noise of that test. More than half of the growth is the tool schemas of the panel's MCP servers, which the panel built at start (716 tool registrations); the rest is spread over the Go heap, the runtime and SQLite. The binary grew from 62 to 112 MB, and there the cause is code: 23 MB of the 50 come from a single import in the HTTP outbound of the Xray-core fork, the AI assistant's SDK, the larger web interface and the embedded site templates add the rest. The other three panels moved by 3 to 7 %. The breakdown with the figures is in [Where the growth between v0.1.32 and v0.2.1 comes from](#where-the-growth-between-v0132-and-v021-comes-from).

## Where the growth between v0.1.32 and v0.2.1 comes from

A separate analysis of 24 November 2026, on the same machine and with the same protocol as the tables of this page: the panel alone in a slice of 1 vCPU / 1 GB, 300 s of idle, a window of 60 s, 3 repetitions, medians with the minimum and maximum in brackets. v0.1.32 and v0.2.1 were measured side by side. The tables of this page are not changed by it: they are the measurements of the released v0.2.1.

### Panel memory

| Private memory, panel only (MiB) | 0 users | 200 users |
|:--|--:|--:|
| v0.1.32 | 16.3 (16.1-16.6) | 20.5 (20.1-20.8) |
| v0.2.1 | 36.9 (36.0-37.1) | 41.5 (40.7-41.7) |
| v0.2.1 with the MCP servers built on first use (test build) | 25.3 (25.2-25.4) | 30.1 (30.0-30.2) |

The same v0.2.1 binary showed 39 / 44 MiB in the main series above, 2.5 MiB more than here: that is the spread between two series, not a difference between builds. The growth is 20.6 MiB with no users and 21.0 with 200. Where the 21.0 MiB at 200 users goes:

| Part | Growth (MiB) |
|:--|--:|
| Live heap (what the Go program really holds) | +11.2 |
| Heap above the live part (garbage until the next collection, fragmentation) | +6.9 |
| Free pages of the heap | -0.3 |
| Stacks and runtime bookkeeping | +1.4 |
| Outside the Go heap (SQLite, libc) | +2.2 |

The rows add up to 21.4 against 21.0 measured: the estimates carry an error of about 1 MiB. The live heap, +11.2 MiB, by owner:

| Owner of the live heap | Growth (MiB) |
|:--|--:|
| MCP servers (tool schemas) | +5.6 |
| `init` of all other packages | +1.8 |
| Protobuf descriptions and connect handlers | +2.0 |
| Log ring | +0.62 |
| Release list | +0.46 |
| Template of the subscription page | +0.28 |
| Everything else | +0.4 |

**The main cause is the MCP servers.** At start the panel built two sets of them, one for the HTTP endpoint and one for the built-in assistant, three profiles each: 716 tool registrations, every one with its schema, whether or not anyone ever called it. Building them on first use, in the same protocol, takes 11.6 MiB off with no users and 11.4 MiB with 200 (the last row of the first table).

**The Xray core and the assistant's SDK are not the cause of the memory growth.** Builds without them, measured at the 100th second on an empty database, 3 repetitions: without the Xray core -1.0 MiB, without the SDK -2.1 MiB, without both -1.0 MiB, with a noise of about 2 MiB. They matter for the binary, not for memory.

With 1000 users another 5 MiB or so of the panel were held by the gzip writers of the stream from the panel to the node.

### One server, by process

Panel and node on one server, 1000 users, the node started natively (not in a container as in the one-server tests above). The older build here is v0.1.33; 3 repetitions, medians.

| Private memory (MiB) | v0.1.33 | v0.2.1 |
|:--|--:|--:|
| Slice, no traffic | 49.7 | 78.5 |
| Panel, no traffic | 37.0 | 67.0 |
| Node, no traffic | 10.9 (10.0-11.2) | 10.7 (10.0-11.0) |
| Slice, after 5 s of traffic | 56.0 | 84.6 |
| Panel, after 5 s of traffic | 36.0 | 62.2 |
| Node, after 5 s of traffic | 16.9 | 19.4 |

The growth is in the panel (+30 MiB at rest); the node did not grow at rest. The panel's share includes the gzip writers mentioned above.

### Binary size

61.94 MB in v0.1.32, 111.66 MB in v0.2.1 (+49.7 MB).

| Part | Size (MB) |
|:--|--:|
| One `text/template` import in the HTTP outbound of the Xray-core fork: with it the linker keeps every exported method of every type of the program | 23.4 |
| The AI assistant's SDK | 5.4-9.2 |
| The Xray code itself | about 4 |
| Embedded web interface | +5.8 |
| Embedded site templates | +1.9 |
| Mistgate's own code (the text of the binary, MiB) | +6.1 |

### Cold start

Setup and serve until the panel is ready, 9 repetitions.

| Cold start, empty data (s) | v0.1.32 | v0.2.1 |
|:--|--:|--:|
| Started directly, no `systemd-run` | 0.134 | 0.417 |
| Through `systemd-run` | 0.229 | 0.516 |
| In the table above (3 repetitions, `systemd-run`) | 0.33 | 0.66 |

The absolute figures differ between the three rows, the ratio is the same, about 2.2 times. Of the 0.28 s of growth:

| Part | Cost |
|:--|:--|
| Applying migrations to an empty database: 145 against 47 | 266 ms against 62 ms (+0.20 s) |
| Building the panel (mostly the MCP servers) | +64 ms |
| `init` of the packages | +9 ms |

### What did not add up

- 2.5 MiB between the series: this analysis gives 36.9 / 41.5 MiB for v0.2.1, the main series above 39 / 44, same binary.
- 6.4 MiB on one server: the one-server pair of the page (57 to 92) grew by about 6.4 MiB more than the pair above (the page's figures are rounded). The set-ups differ (there the node ran in a container, here natively), and the difference was not explained.
- Counted by cause (MCP servers, garbage between collections, SQLite, the runtime), about 0.9 MiB of the memory growth is not attributed; the estimates have an error of about 1 MiB.

### Fixed after v0.2.1

These changes are in the main branch, were **not** in the v0.2.1 release, and the tables of this page do not include them. The summary table will be measured again on the next release.

- The MCP servers are built on first use, one set for the endpoint and the assistant together. The panel on an empty database at the 100th second: 42.5 to 29.4 MiB (3 repetitions: 42.6 / 42.5 / 42.0 before, 30.5 / 29.4 / 29.0 after); readiness 0.15 to 0.09 s.
- Messages from the panel to the node shorter than 16 KiB are not compressed. The effect at 1000 users in this form was not measured.
- The Xray-core fork no longer imports `text/template`: the panel binary without the embedded interface is 80.0 MB instead of 103.4 MB.

## Results: panel only (1 vCPU, 1 GB)

Measured on 24 November 2026. Private memory (anon) is the headline for memory. `memory.current` is shown next to it because it is what `docker stats` and `systemctl status` show, but it includes page cache, and the cache is charged unevenly (see the notes).

| | Mistgate | 3x-ui | PasarGuard | Remnawave |
|:--|--:|--:|--:|--:|
| Disk: image or binary (MB) | 112 | 456 | 487 | 1537 |
| Disk: data after 200 users (MB) | 5.7 | 4.8 | 4.6 | 61 |
| Idle, 0 users: anon (MiB) | 39 (38-39) | 73 (73-73) | 273 (273-274) | 436 (435-436) |
| Idle, 0 users: memory.current (MiB) | 321 (224-321) | 193 (192-198) | 309 (309-316) | 621 (611-755) |
| Idle, 200 users: anon (MiB) | 44 (42-44) | 76 (75-78) | 277 (277-278) | 464 (462-466) |
| Idle, 200 users: memory.current (MiB) | 58 (56-58) | 154 (154-157) | 318 (318-325) | 659 (637-674) |
| Idle, 200 users: CPU (% of one vCPU) | 0.05 | 0.35 | 0.11 | 2.70 |
| Processes / containers | 1 / 0 | 3 / 1 | 1 / 1 | 23 / 3 |
| 200 users created through the API (s) | 0.3 | 1.8 | 2.1 (2.1-3.0) | 1.1 (1.1-1.3) |
| Cold start, empty data (s) | 0.66 (0.62-0.73) | 2.27 (2.17-2.97) | 11.4 (11.2-11.9) | 13.9 (13.5-14.4) |
| Cold start, 200 users on disk (s) | 0.18 (0.18-0.19) | 1.66 (1.61-2.07) | 6.6 (6.6-6.6) | 11.9 (11.8-12.7) |
| Subscription load: anon during the run (MiB) | 61 (60-61) | 88 (88-88) | 292 (292-293) | 884 (874-898) |
| Subscription load: peak memory.current (MiB) | 82 (81-82) | 179 (178-180) | 343 (342-349) | 1024 (the limit) |

Notes on the table:

- Cold start is the time from the start command to the first HTTP 200 on the panel's main port. For 3x-ui the subscription listener (port 2096) comes up a little later than the main port; the harness waits for it before the post-restart memory sample, and all three repetitions have it.
- Remnawave reaches the 1 GB limit during the load: the cgroup counted 169,000 to 228,000 `max` events (allocations that had to reclaim) in the three repetitions, and no OOM kill in any of them. Its private memory during the load, about 880 MiB, is real, not cache. Postgres, Valkey and three Node.js processes make up the group (about 130-146 MiB each for `rw-api`, `rw-processor` and `rw-scheduler` at idle).
- 3x-ui carries its Xray core (anon about 9 MiB) and fail2ban (about 12 MiB) in the measured group. Subtract them if you compare only the panel process: x-ui itself is about 42 MiB, which is the size of Mistgate's panel (43 MiB in the same measurement).
- PasarGuard and Remnawave had no VPN core running in the measured group (no node attached), and Mistgate had no node connected (the agent that was needed once to make the subscription non-empty is removed before the measurement; the node record stays).
- Mistgate's `memory.current` at 0 users is page cache, not private memory: 224 to 321 MiB in the first window against 56-58 MiB in the 200-user window five minutes later. The kernel charges the group for the file reads of the setup (the binary is now 112 MB); it is reclaimable and does not count in anon. October's 17-29 MiB for the same cell came with a 62 MB binary. The cold-cache pass of October (drop the page cache before every start) was not repeated.
- The 429 count of the Mistgate subscription load below depends on the 60-per-hour link cap and is described there. Remnawave's p99 under that load was 4.1 to 11.7 s in this series (1.1 to 3.0 s in October); its requests per second ran from 132 to 243 between repetitions.

### Subscription load

50 concurrent clients, 60 s, the same User-Agent (`v2rayN/7.5.0`), 200 links, keep-alive on. Every panel returns one link per user, base64 in a text body of 152 to 360 bytes. Formats differ in what the link is (Mistgate Hysteria2, 3x-ui VLESS Reality, Remnawave and PasarGuard Shadowsocks), see the notes at the end.

| | Mistgate (as shipped) | 3x-ui | PasarGuard | Remnawave |
|:--|--:|--:|--:|--:|
| Requests per second, status 200 | 197 (limiter, see below) | 163 (159-165) | 306 (303-309) | 135 (132-243) |
| p50 / p95 / p99 latency (ms) | 4.7 / 6.6 / 7.9 | 273 / 470 / 1218 | 151 / 270 / 378 | 90 / 151 / 9478 |
| Non-200 responses | 99.5 % | 0 | 0 | 0 (0.01 % in one repetition) |
| Mean CPU (% of one vCPU) | 98 | 34 | 98 | 97 |

**Mistgate as shipped does not serve this load, by design.** Each subscription link may be fetched 60 times an hour; the 61st and later answer 429. In every repetition exactly 11,799 requests got 200 (60 per link and hour, minus one warm-up fetch per link and one sanity-check fetch before the load) and 2.5 to 2.6 million got 429. The 429 path is cheap (42,600 to 43,600 answers per second on one vCPU, 27,000 to 34,000 in October), so the "requests per second" of the first row is the speed of refusing, not of serving. It is not a result. The per-client-address bucket of the public listener (10 requests per second, burst 60) did not decide this run: each link had its own client address through `X-Forwarded-For` behind `--trusted-proxy`.

3x-ui uses 34 % of one vCPU at 163 requests per second: its limit is something other than CPU (in October it closed the connection after every response; not checked again).

### Supplementary: Mistgate with the limits switched off (modified builds, October, v0.1.32)

**Not repeated in November**: the two builds need a patched v0.1.32 source (the diff is in the repository), and nothing in v0.2.1 was changed to make them comparable (diff: [`mistgate-bench-variants.diff`](https://github.com/Mistgate/mistgate/blob/main/tools/bench/mistgate-bench-variants.diff)). The figures below are the October ones and say nothing about v0.2.1. They are not the shipped build; never quote them as Mistgate's result.

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

Every server has a self-signed certificate; the client does not verify it (identical for all). Congestion control is BBR on both sides, no bandwidth declared, no obfuscation. The clients are `hyload`, a small program built on the Hysteria 2 client library (`core/v2` v2.12.3); it runs outside the group, with no CPU limit, in a separate cgroup, and its CPU was 140-340 % of one vCPU of the 16 available, while the group was held at 200 % by its quota. Every core container (Mistgate's node, the Xray cores of the other three) runs with `GOMAXPROCS` equal to the group's vCPUs, which is what the Go runtime picks on a real server with that many vCPUs; without it a container sizes `GOMAXPROCS` by the host's 16 CPUs (see [After the fixes](#after-the-fixes)).

Phases: A idle with 1000 users (60 s settle, 120 s window); B one tunnel with 1, 16 and 64 parallel downloads (30 s each) with a small request through the same tunnel every 100 ms; C 400 clients connected at once with a small request every 2 s (60 s); D "bad day" for 120 s (64 streams, a subscription storm of 300 requests per second over 1000 source addresses, and one user added and one removed per second through the admin API); E the panel (not the core) restarted 20 s into a 60 s run with 64 streams.

Measured on 24 November 2026: the first series with a quota only, phases A to E (A, C and E and the control-plane rows of D below), the second with the group pinned to two physical cores, phases B and D, a probe every 97 ms. Medians of 3 repetitions, brackets are minimum-maximum. "Private memory" is anon plus kernel memory of the whole group.

| | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| **A** private memory, whole group (MiB) | 92 (90-93) | 81 (78-81) | 815 (700-864) | 312 (311-313) |
| A: memory.current (MiB) | 99 (97-100) | 89 (86-89) | 896 (781-946) | 329 (328-329) |
| A: CPU (% of one vCPU, of 200) | 0.21 | 0.55 | 3.53 | 0.29 |
| A: processes / containers | 2 / 1 | 3 / 1 | 36 / 4 | 3 / 2 |
| 1000 users created through the API (s) | 38 (38-50) | 18 (18-18) | 26 (25-29) | 40 (40-41) |
| **C** 400 clients: connected / handshake failures | 400 / 0 | 400 / 0 | 400 / 0 | 400 / 0 |
| C: handshake p95 (ms) / request p95 (ms) | 3 / 1.1 | 2 / 1.0 | 2 / 1.2 | 3 / 1.1 |
| C: CPU of the group (% of one vCPU) / private memory (MiB) | 15 / 149 | 14 / 142 | 18 / 685 | 15 / 378 |
| **D** streams dropped / tunnel reconnects | 0 / 0 | 512 (512-512) / 7 (5-7) | 0 / 0 | 0 / 0 |
| D: subscription storm served (requests/s of 300 offered), 200 only | 300 | 129 (126-129) | 299 | 78 (77-79) |
| D: subscription p95 (ms) | 1 | 575 (569-586) | 193 (192-196) | 1302 (1287-1331) |
| D: admin add user p95 / remove user p95 (ms) | 3 / 2 | 653 / 743 | 75 / 75 | 1750 / 1532 |
| D: admin list call p95 (ms) | 13 | 339 | 71 | 1207 |
| D: private memory (MiB) | 123 | 118 | 1159 (1147-1160) | 374 |
| **E** panel restart: admin answers again after (s) | 0.3 | 1.6 | 9.7 (9.6-10.8) | 7.1 (6.9-7.1) |
| E: seconds without tunnel traffic (of 60) / streams dropped | 0 / 0 | 2 / 128 | 0 / 0 | 5 / 128 |
| Core restarts when users change | no | yes | no | no |

Reading the table:

- Memory (A): 92 MiB for Mistgate's panel and node (panel 75 MiB, node agent 13 MiB) against 81, 312 and 815 MiB. In October Mistgate was 57 and 3x-ui 74; **3x-ui now holds 11 MiB less than Mistgate on one server** (x-ui 58 MiB, its Xray core 10, fail2ban 12). Remnawave is 36 processes in 4 containers (backend with three Node.js workers, Postgres, Valkey, the node); its idle memory ranged from 700 to 864 MiB between repetitions (not investigated).
- Creating users: the harness creates them one API call at a time and waits out `429` answers. Mistgate's admin API allows 30 requests per second with a burst of 200, so the first 200 users take a fraction of a second (0.3 s in the panel-only test) and the remaining 800 are paced by the limit. The 38 s (38 to 50 in the three repetitions) is mostly that pacing, not the cost of creating a user.
- Many clients (C): 400 simultaneous tunnels, no failures anywhere, 18 % of one vCPU or less. Not a differentiator at this size.
- Bad day (D): the tunnel carries 64 streams while 300 subscription requests per second arrive from 1000 addresses and one user is added and one removed per second. 3x-ui restarts its Xray core when users change (512 streams dropped in each repetition and the single client reconnected 5 to 7 times in 120 s); the other three add and remove users in the running core without dropping connections. 3x-ui serves the storm at 129 requests per second (it was offered 300) and takes 0.7 s per user change; PasarGuard serves 78 per second and takes 1.5-1.8 s; Remnawave serves 299 per second at a 193 ms p95 and its memory in D reaches 1159 MiB (three Node.js workers on the same two vCPU as the Xray core). Mistgate serves all 300 per second at a 1 ms p95 and changes a user in 2-3 ms.
- Restart (E): restarting the panel container of 3x-ui restarts its Xray (design: the core is a child of the panel process; 128 streams dropped, 2 seconds without traffic). Restarting PasarGuard's panel container also interrupted the tunnel (128 streams dropped, 5 seconds without traffic): the node's core is restarted when the panel comes back (observed, mechanism not investigated). Restarting Remnawave's backend left the tunnel untouched but the admin API took 9.6-10.8 s to answer again. Mistgate's panel restarts in 0.3 s and its node keeps serving.
- The group is held at its quota in D (178-196 % of one vCPU for all four), so the control-plane rows compare how much of a saturated box each panel still has for its own work.

### The tunnel: phases B and D

The tunnel rows are published this time: the machine was quiet, and in both series the three repetitions of one panel are within 10 % of each other in every throughput cell (most within 5 %). Throughput is the download through one Hysteria2 tunnel to the sink on the bridge; the group is at 178-196 % of its 200 % quota in every run, so what is compared is the work the two vCPU can do. Medians of 3 repetitions, brackets are minimum-maximum.

| Gbit/s | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| **Quota only.** B: 1 stream | 5.68 (5.50-5.88) | 3.99 (3.94-4.01) | 3.69 (3.54-3.70) | 3.91 (3.75-3.92) |
| B: 16 streams | 6.06 (5.77-6.08) | 5.73 (5.68-5.76) | 5.57 (5.44-5.57) | 5.72 (5.63-5.86) |
| B: 64 streams | 5.94 (5.43-6.01) | 5.78 (5.72-5.79) | 5.48 (5.45-5.58) | 5.71 (5.55-5.72) |
| D: 64 streams on the bad day | 5.15 (5.13-5.18) | 3.75 (3.66-3.76) | 1.67 (1.64-1.71) | 2.98 (2.93-3.01) |
| D against B64 | -13 % | -35 % | -70 % | -47 % |
| **Two physical cores, clients elsewhere.** B: 1 stream | 5.63 (5.26-5.67) | 3.83 (3.81-3.95) | 3.73 (3.66-3.85) | 3.90 (3.89-3.96) |
| B: 16 streams | 6.14 (6.10-6.20) | 5.65 (5.56-5.67) | 5.57 (5.44-5.57) | 5.70 (5.70-5.76) |
| B: 64 streams | 5.98 (5.93-6.01) | 5.76 (5.73-5.78) | 5.53 (5.47-5.54) | 5.77 (5.63-5.87) |
| D: 64 streams on the bad day | 5.31 (5.23-5.32) | 3.82 (3.79-3.85) | 2.60 (2.59-2.63) | 3.50 (3.47-3.51) |
| D against B64 | -11 % | -34 % | -53 % | -39 % |

What the table says, and what it does not:

- **One stream: Mistgate carries about 5.7 Gbit/s, the others 3.7 to 4.0.** The same gap, 42-54 %, shows in both series and in every repetition. With 16 and 64 streams all four reach 5.5 to 6.1 Gbit/s and Mistgate is 3 to 10 % ahead: the two vCPU are used up (the group sits at 184-194 %), and the clients too (330-340 % of a CPU), so the ceiling is the CPU, not a panel. Mistgate's node in October ran the same engine, and its profile showed 52 % of its CPU in UDP `sendmsg` and `recvmsg` with GSO in use; the November run does not explain the one-stream gap and no cause is claimed.
- **On the bad day Mistgate keeps 87-89 % of its speed, 3x-ui 65-66 %, PasarGuard 53-61 %, Remnawave 30-47 %.** The order is the same in both series. What eats the rest is the control plane that shares the two vCPU with the core: 3x-ui restarts Xray on every user change (the tunnel reconnects), Remnawave's Node.js workers and PasarGuard's Python process take CPU from the Xray core. The pinned series gave PasarGuard and Remnawave more (3.50 against 2.98, 2.60 against 1.67): with the group on two cores the threads that are not part of the core queue differently.
- **Small request through the loaded tunnel, p95.** Phase B, medians in ms, quota only: Mistgate 2.0 / 2.9 / 3.3 at 1 / 16 / 64 streams; the other three 1.2-1.3 / 2.0-2.2 / 2.4-2.5; pinned series: Mistgate 1.9 / 2.6 / 2.9, the others 1.1-1.2 / 1.7-2.0 / 2.2-2.8. **Mistgate is above the others by 0.6-0.9 ms at 1 and 16 streams and by 0.1-0.9 ms at 64, in both series**; everybody is between 1 and 3.6 ms, far from the 29 ms of the October first series, which was a harness fault (see [After the fixes](#after-the-fixes)). The relative spread of several cells is above 15 % (the others' cells move by 0.1-0.7 ms), so read it as "about 1-3 ms for all, Mistgate slightly higher", not as a ranking.
- **Not published: the small request in phase D** (its p95 differs by 19-50 % between the repetitions of three of the four panels in each series) and the throughput of phase E.

### Cheapest server: 1 vCPU, 1 GB RAM, no swap (one repetition)

Same phases A, B, D and E, group limited to `CPUQuota=100%` and `MemoryMax=1G`, `GOMAXPROCS=1` in the cores, run on 24 November 2026 on the quiet machine. One repetition per panel only, so the spread is unknown; for that reason the tunnel rows of B and D (throughput, the small request through the tunnel) are not published here.

| | Mistgate | 3x-ui | Remnawave | PasarGuard |
|:--|--:|--:|--:|--:|
| A private memory (MiB) | 109 | 86 | 850 | 317 |
| D: streams dropped | 0 | 512 | 256 | 0 |
| D: storm served (requests/s of 300) / non-200 | 300 / 0 % | 109 / 0 % | 100 / 45.4 % | 55 / 0 % |
| D: subscription p95 (ms) | 8 | 680 | 404 | 1805 |
| D: admin add user p95 (ms) | 19 | 703 | 310 | 2497 |
| E: admin answers again after (s) | 0.5 | 1.5 | 13.1 | 7.7 |
| E: seconds without tunnel traffic / streams dropped | 0 / 0 | 2 / 128 | 2 / 128 | 5 / 128 |

Remnawave runs out of memory on a 1 GB server during D: the kernel killed `rw-api` eight times, `rw-processor` five times and `rw-scheduler` once (OOM kill, cgroup `bench-box.slice`, counted in the kernel log since the WSL instance started, where nothing else was killed), 45.4 % of the subscription requests failed with refused or reset connections and 44 admin calls failed; its tunnel also dropped 256 streams in D and 128 in E, where on 2 vCPU / 2 GB it dropped none (cause not investigated). At rest it already uses 850 MiB. It was not given more memory.

Mistgate's idle memory on this server, 109 MiB, is higher than its 92 MiB on the 2 vCPU server (October: 73 and 57). In October the difference was in the node agent, which runs in the same measured group; it was not looked at again. The group's memory also rises for the first minutes after the 1000 users are created, so the idle figure depends on where the window falls: this run settled for 30 s, the 2 vCPU runs for 60 s.

## Profiling (Mistgate only)

**October, v0.1.32 and v0.1.33; not repeated in November.** A separate pass with `net/http/pprof` builds (added with `go build -overlay`, the repository is untouched), CPU profiles of 25-30 s, heap/allocs/goroutine/mutex/block profiles and `GODEBUG=gctrace=1`, for the idle panel, the subscription hammer (as shipped, and with limits and cache off), and the one-server phases B64, C and D for the panel and the node. These runs do not feed the numbers above. The full analysis is in [`results/profiling-summary.txt`](https://github.com/Mistgate/mistgate/blob/main/tools/bench/results/profiling-summary.txt); the raw profiles are not published. What it found:

- **The 429 path is already cheap.** About 37 µs of CPU per refused request, 49 % of it in socket system calls. It allocates 3.2 KB per request; 41 % of that is the standard library parsing the request, 19 % two copies of the request made by the panel's own middleware (`withStart`, `WithClientIP`), 25 % response header maps. Anon memory during the refusing was flat at 27 MiB (20 MiB before). The 79 MiB seen at the start of each run comes from hashing the owner password with argon2id (64 MiB) once, not from refusing requests.
- **A subscription render costs 51 µs of CPU from the cache and 614 µs without it** (62 KB and about 900 objects allocated). Half of the uncached CPU is SQLite parsing and planning the same statements again for every request (`sqlite3_prepare_v2`, 49.8 %); 16 % of the allocation is `strings.ToUpper` applied to whole SQL strings to test for a `SELECT` prefix.
- **The 1.7 s p99 of the uncached path is starvation, not a pause.** About 25 requests per second wait over 500 ms, every second of the run. With `GOMAXPROCS=4` the slow requests disappear (p99 86 ms, same throughput). The reader pool has 4 connections and `database/sql` hands a freed connection to a random waiter.
- **Node:** 52 % of CPU is UDP `sendmsg`/`recvmsg` (GSO is in use), 5 % AES-GCM, 19 % QUIC packet building. The hot allocation (37 %) and a contended mutex (`QStream.Write`) are in `apernet/quic-go` and the Hysteria core.
- **GC:** 2-6 collections per second, longest stop-the-world 0.26 ms when the group runs alone on its CPU and 46-49 ms when it is held at its quota in the one-server test.

### After the fixes

The tables of this section are from October (v0.1.32 and v0.1.33, as the column titles say) and were not repeated in November, apart from the tunnel paragraph at its end.

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

The small-request latency inside a loaded tunnel (phase B: 29 ms at p95 in two of three repetitions, 2.3 ms in the third; 67 ms on 1 vCPU) turned out to come from the test setup, not from the node. The quota of the one-server group is set on the systemd slice, and a container with its own cgroup namespace does not see it, so the node's Go runtime sized `GOMAXPROCS` by the host's CPUs, used up the 2-CPU quota about 80 ms into every 100 ms period and stood still for the rest of it (19 % of the time in a runtime trace). A probe every 100 ms always met the same phase of that period, either beside the pause or in it, hence the two levels. With `GOMAXPROCS` equal to the group's vCPUs, which is what the runtime picks on a real server, the node never stalls. Same binary and session, 64 streams, a probe every 97 ms so the phase drifts: p95 14.7-18.2 ms before, 3.3-3.6 ms after, and 4.2 Gbit/s became 5.4. The harness in `tools/bench` now gives every core container (Mistgate's node and the Xray cores of the other panels) `GOMAXPROCS` = vCPUs. The October re-run on v0.1.33 with this harness was disturbed by other load on the machine (one panel's 64-stream result swung between 1.5 and 4.2 Gbit/s from one repetition to the next, and the slow repetitions are the ones where the load generator got 177-185 % of a CPU instead of 320 %), so its tunnel rows were withdrawn. The November run on a quiet machine settles it: the small request of Mistgate is 1.9-3.3 ms at p95 in phase B, 0.6-0.9 ms above the others, not 29 ms, and the repetitions agree (see [The tunnel: phases B and D](#the-tunnel-phases-b-and-d)).

Open work:

1. Measure the whole protocol of this page again on the next release (the fixes of the main branch are not in v0.2.1); explain the 6.4 MiB by which the one-server growth on this page exceeds the growth in the analysis (the node runs in a container here, natively there); fold the 145 migrations into a shorter chain, which is most of the slower cold start. See [Where the growth between v0.1.32 and v0.2.1 comes from](#where-the-growth-between-v0132-and-v021-comes-from).
2. Measure the uncached subscription path and the supplementary builds again on v0.2.1, and the cold-cache pass.
3. Look at the one-stream gap of the tunnel (5.7 Gbit/s against 3.7-4.0): it is in Mistgate's favour, but no cause has been found.

## Method

### Machine

AMD Ryzen 7 7800X3D (8 cores, 16 threads), 15.5 GB RAM, Windows 11 host, WSL2 with Ubuntu 24.04.4, Linux 6.6.87.2-microsoft-standard-WSL2, Docker 29.4.1 with Compose v5.1.3, systemd cgroup driver, cgroup v2. The November runs were made on 24 November 2026 on a computer with nothing else running: the Windows side was 1-8 % busy before the series, and three unrelated containers sat idle in the same WSL instance. The one-minute load average at the start of each repetition (field `background` in the raw JSON) was 0.1-2.2 in the panel-only series and 2.1-6.0 in the one-server series; those are the teardown of the previous repetition, not other work, and every tunnel run also records how busy the whole machine was (22-33 % of its 16 threads in phase B, which is the group and the clients). The harness, the binaries and the panels' data were on the WSL ext4 disk, as in October (the results went to the Windows drive); a 9p mount would have changed start times and SQLite. The load generator and the sink ran in the same WSL instance, outside the limited cgroups.

### Limits

- Panel host: one systemd slice `bench-panel.slice` with `CPUQuota=100%`, `MemoryMax=1G`, `MemorySwapMax=0`, and pinned to one host CPU (`AllowedCPUs=15`, set at runtime with `systemctl set-property` before the series; the load generator was not in the slice and not pinned). Docker containers are created with `cgroup_parent: bench-panel.slice` and Mistgate is started with `systemd-run --slice=bench-panel.slice`, so every process of one panel (all containers of Remnawave included) shares the one budget. The slice is stopped and started before every repetition so that `memory.peak` starts from zero.
- One server: `bench-box.slice`, `CPUQuota=200%`, `MemoryMax=2G`, `MemorySwapMax=0`; for the cheapest-server repeat 100 % and 1G. Every core container (Mistgate's node, the Xray cores of the other panels) gets `GOMAXPROCS` equal to the vCPUs of the slice, because a container does not see the slice's quota and would otherwise size it by the host's 16 CPUs (see [After the fixes](#after-the-fixes)).
- Panel only: the slice is pinned to one host CPU (`AllowedCPUs=15`, set at runtime with `systemctl set-property` before the series), as in October. It is closer to a one-vCPU server than a quota alone (a quota lets a multi-threaded runtime burst on several host CPUs, then throttles it). In October one repetition without the pin was run for comparison (v0.1.32; not repeated):

| Panel-only, 1 repetition | pinned + quota (the October series) | quota only |
|:--|--:|--:|
| Mistgate: anon idle 200 users (MiB) / 429 answers per second | 20 / 33,700 | 24 / 15,750 (p99 77 ms against 7.4) |
| 3x-ui: 200 responses per second / p50 (ms) | 148 / 296 | 162 / 278 |
| PasarGuard: 200 responses per second / p50 (ms) | 274 / 166 | 102 / 467 |
| Remnawave: 200 responses per second / failed requests | 271 / 0 | 251 / 75 % (a worker was killed by the 1 GB limit, `oom_kill` 1) |
| Remnawave: anon during the load (MiB) | 870 | 786 |

  Idle memory, disk, start time and the 3x-ui numbers do not depend on the pin. PasarGuard and Remnawave do, under load: with a quota only, their threads spread over many host CPUs and are throttled by the quota. A real one-vCPU virtual machine behaves like the pinned case.
- One server: two series. The first used a quota only (`CPUQuota=200%`, not pinned), phases A to E. The second pinned the group to two physical cores and the clients to the others: `AllowedCPUs=0,1,8,9` (threads 0 and 8 are one core, 1 and 9 the next, as `lscpu -e` shows in WSL), `CPUQuota=200%`, `GOMAXPROCS=2`, the clients and the sink on CPUs 2-7 and 10-15, phases B and D, a probe every 97 ms so that its phase drifts against the 100 ms CFS period. WSL's CPU topology is virtual, see [What the numbers do not tell you](#what-the-numbers-do-not-tell-you).

### Measurements

- Memory and CPU: the cgroup files of the slice sampled once a second (`memory.current`, `memory.stat` anon/file/kernel, `cpu.stat`, `pids`). Idle figures: mean of the last 30 s of a 5-minute idle (memory) and the whole idle window (CPU). Peaks are the largest 1 Hz sample inside the window, and the kernel's `memory.peak` of the slice.
- Disk: sum of the `docker images` sizes of the images used (unpacked), or the size of the binary; data directory size after install and after 200 users.
- Cold start: from the start command (`docker compose up -d` or `systemd-run`) to the first HTTP 200 on the main port (Remnawave: `/api/auth/status`, which reads the database).
- Users: created one by one through each panel's documented API (Mistgate Connect-JSON with an owner session, 3x-ui `/panel/api/clients/add`, Remnawave `/api/users`, PasarGuard `/api/user`). Mistgate and the others were given the same invented names `user1` to `user200` (1000 in the one-server test).
- Load: `loadgen` (Go, standard library), 50 workers, round-robin over the links, one warm-up pass, 60 s, latency of 200 responses only; every other status is counted as an error.
- Order: the panel-only series ran the panels in rotation, three times each; the one-server series shuffled the order in every repetition (seeded by the repetition number). The data was wiped before each repetition.

### What was set up for each panel, and where it differs from the project's own instructions

- Mistgate: built from the v0.2.1 tag with the project's reproducible build (`release build`), without a release key, so it cannot update itself or install signed node bundles. Started with `serve --listen 127.0.0.1:18080 --trusted-proxy 127.0.0.1 --agent-listen <bridge address>`. A node agent had to connect once (in a container on an isolated network) because a link carries no server line until a node is active; the container is removed before measuring.
- 3x-ui: the published image instead of the compose file's `build:`; ports bound to 127.0.0.1; the first panel user, which since v3.10 is generated at random and printed once to the container log (October: `admin/admin`); one VLESS Reality inbound.
- Remnawave: the official compose file with the backend image pinned from `:3` to `3.4.11` (node 3.4.6); `X-Forwarded-For` and `X-Forwarded-Proto: https` sent on every request (the backend refuses requests that did not pass a TLS proxy); API token created from the dashboard session (`x-remnawave-client-type: browser`); the default config profile (one Shadowsocks inbound) and the default squad; one host.
- PasarGuard: the project's default compose file (host networking), image pinned to v5.6.0 (node v0.5.7), `UVICORN_HOST=127.0.0.1`; default core config (one Shadowsocks inbound); one host and one group; owner created with the temporary key from `pasarguard-cli generate-temp-key`.
- Nothing was tuned. TLS is not used on any panel (plain HTTP on loopback), although a real installation puts a TLS proxy in front.

### Reproduce

The scripts, compose files, the diff of the modified builds, the load generators and the aggregated results are in [`tools/bench`](https://github.com/Mistgate/mistgate/tree/main/tools/bench) in the repository, with a README: `py/bench.py` (panel only), `py/onebox.py` (one server), `py/profile_run.py` (profiling), `py/aggregate*.py`, `loadgen/`, `hyload/`, `compose/`, `profiling/` (the pprof overlay) and `results/` (tables and per-repetition CSVs). The raw JSON and the 1 Hz samples are not published. The harness creates and removes Docker images and systemd slices: run it on a test machine and read the README first.

## Engine benchmark suite

The tables above compare panels. This suite measures the engines inside the node (Hysteria2, VLESS and AmneziaWG) against their own earlier numbers, so a change that makes one of them slower is seen before a release. It is a regression check, not a comparison with other products.

**What it measures.** For each engine, between two network namespaces joined by a veth pair, with a third namespace as the "internet" behind the node:

- throughput of 1 and of 8 parallel streams (Mbit/s, download through the tunnel);
- CPU seconds per gigabyte the engine process used while it moved both phases (`/proc` user plus system time);
- p50 and p99 latency of a small request (64 bytes, every 20 ms) while the 8 streams load the link;
- the engine process's peak resident memory (`VmHWM`);
- handshakes per second: new Hysteria2 sessions (QUIC handshake plus authentication), new VLESS REALITY connections (each one a REALITY handshake and a VLESS session, no multiplexing), and for AmneziaWG a re-handshake of one peer with one ping, paced at WireGuard's own limit of one initiation per 20 ms, so it tops out near 30 and mainly shows that each one succeeds.

The engines are the real ones from this repository, started as the node starts them: Hysteria2 (self-signed certificate, pinned by the client), VLESS with REALITY over TCP (Vision) and over XHTTP, and AmneziaWG on the userspace backend with a real amneziawg-go client. The clients are the real client libraries (the Hysteria client, an in-process Xray client). The engine runs in its own process, so its CPU and memory are its own.

**Network variants.** `clean` (no shaping), `rtt50-loss1` (25 ms and 0.5 % each way on the client link: a 50 ms round trip with about 1 % lost) and `mtu1280` (the client link at an MTU of 1280). An engine that cannot carry traffic at all over a variant has no case for it: Hysteria2 over a 1280-byte link is skipped, because QUIC sends 1280-byte datagrams with the don't-fragment flag and the link drops them.

**Run it.** It needs Linux, root, `ip` (iproute2), `tc` for the netem variants, `nft` for AmneziaWG and Go, and it changes network state, so use WSL or a throwaway machine, never a fleet host. Every namespace is named `mg3-awg-b-*`; the host's own interfaces, routes and nft tables are not touched.

```sh
scripts/bench.sh                                  # all engines and variants; from Windows it runs itself in WSL as root
scripts/bench.sh --engines hysteria2,awg --variants clean --seconds 6
scripts/bench.sh --repeat 3 --update              # make a new baseline from the median of three runs
MISTGATE_BENCH=1 go test -tags bench ./internal/node/bench/ -run '^TestBench$' -count=1 -v -timeout 0   # as root on Linux, the same thing
```

Without `MISTGATE_BENCH=1`, without root, or without network namespaces the test skips with a message; without `tc` the netem variants are skipped, without `nft` AmneziaWG is. The report is JSON (`bench-report.json`, or `--out`): one object per `<engine>/<variant>`.

**The baseline.** `internal/node/bench/baseline.json` holds the numbers of a reference run, with the kernel, CPU count and Go version it was taken on. A run is compared with it metric by metric and **fails when a metric is more than 15 % worse** (lower throughput and handshakes, higher CPU per gigabyte, latency and memory). The threshold is `--threshold` (or `MISTGATE_BENCH_THRESHOLD`: `15`, `15%` or `0.15`). A change smaller than a minimum amount does not count however large it is in percent (5 Mbit/s, 0.3 CPU seconds per GB, 2 ms at p50 and 5 ms at p99, 8 MB, 2 handshakes per second), a case with no baseline is only noted, and so is a baseline case that was not run. Absolute numbers depend on the machine: a baseline taken on one machine says little about another. Make it on the machine that will check it, from the median of three runs (`--repeat 3 --update`), and read a failure on a shared machine as "measure again on a quiet one" before reading it as a regression.

**Behind the shared-port front.** `vless-tcp-front` and `vless-xhttp-front` are the same engines with their port shared by the agent's front (see [One 443 for everyone](../guide/shared-port.md)): the engine takes its connections from the front's route instead of from a socket of its own. Each is held to the direct case of the same run, at the same threshold (15 % by default, with the same minimum amounts), whatever the baseline says, and a case in which the front handed the engine no connection fails. `MISTGATE_BENCH_LOOPBACK=1` runs the `clean` variant without namespaces and without root, on 127.0.0.1 to 127.0.0.3: its figures say how the front compares with the direct case on that machine, and are not comparable with the baseline's.

The GitHub workflow `bench` (**Actions → bench → Run workflow**) runs the same thing on a hosted runner. It starts by hand only, never on a pull request or a schedule.

**The reference run** (WSL2, kernel 6.6, 16 threads, the median of three runs of 4 s per phase; in-memory veth links, so the clean throughput is far above any real link and measures the engine's CPU, not a network):

| Case | 1 stream, Mbit/s | 8 streams, Mbit/s | CPU s per GB | p50 ms | p99 ms | RSS MB | Handshakes per s |
|---|--:|--:|--:|--:|--:|--:|--:|
| `awg/clean` | 4683 | 4307 | 4.8 | 9.1 | 37.2 | 394 | 32.8 |
| `awg/mtu1280` | 4926 | 3607 | 4.6 | 7.7 | 34.1 | 362 | 33.6 |
| `awg/rtt50-loss1` | 307 | 1785 | 8.0 | 59.7 | 102.8 | 351 | 3.9 |
| `hysteria2/clean` | 1753 | 2137 | 7.2 | 1.6 | 16.9 | 33 | 721.5 |
| `hysteria2/rtt50-loss1` | 612 | 1365 | 5.1 | 81.9 | 134.7 | 83 | 36.9 |
| `vless-tcp/clean` | 2894 | 16985 | 2.4 | 7.5 | 36.5 | 32 | 1106.8 |
| `vless-tcp/mtu1280` | 3454 | 17214 | 2.2 | 8.1 | 27.3 | 32 | 1092.8 |
| `vless-tcp/rtt50-loss1` | 370 | 2778 | 1.8 | 50.8 | 90.7 | 30 | 24.0 |
| `vless-xhttp/clean` | 931 | 1031 | 13.9 | 1.3 | 6.1 | 33 | 2633.3 |
| `vless-xhttp/mtu1280` | 711 | 869 | 16.3 | 9.1 | 105.9 | 33 | 1042.3 |
| `vless-xhttp/rtt50-loss1` | 255 | 347 | 7.5 | 116.9 | 184.0 | 31 | 69.3 |

CPU per gigabyte is the node process's own time, and the figures are for one 4-second phase on one machine: use them to see a change, not to rank the protocols. AmneziaWG ran on its userspace backend, whose process holds hundreds of megabytes of packet buffers; a kernel backend does its work outside the process, so its CPU and memory do not show in these two columns.

## What the numbers do not tell you

- One desktop CPU, one WSL2 VM, three repetitions. Spreads are small in every table (the largest are Remnawave's idle memory on one server, 700 to 864 MiB, and its p99 under the subscription load), but a different CPU, kernel or filesystem will move absolute values. In November nothing else ran on the machine; in October other work did, and the tunnel rows of that series were withdrawn.
- **WSL2's CPU topology is virtual.** `lscpu -e` shows 16 threads, with n and n+8 as the two threads of one core, and the pinned series uses threads 0, 1, 8 and 9 on that basis. Hyper-V decides which host threads run the guest's CPUs and Windows can move them, so "two dedicated physical cores" means two cores as the guest sees them with nothing else of the guest on them, not a promise about the silicon. That the pinned and the quota-only series agree on phase B and on the order in phase D suggests the effect is small; it does not prove it is zero.
- The load is a closed loop with 50 clients: it shows the cost of a request, not the number of users a panel can serve. A real client fetches a subscription every few hours.
- The panels do different work per request and serve different links (Hysteria2, VLESS Reality, Shadowsocks). Remnawave and PasarGuard render a Shadowsocks line from a stored password; 3x-ui renders a VLESS Reality line; Mistgate renders a Hysteria2 line with a pinned certificate.
- Mistgate answers subscription requests from a 10 s cache of the user's data; the others do not (or do not say so). The cache-off variant above shows the effect.
- Databases differ: Remnawave needs PostgreSQL, the rest use SQLite. PasarGuard also supports PostgreSQL and MySQL; only its default was measured.
- Mistgate was measured with a node record whose agent is gone; the others with no node. The one-server test is the comparison that includes the core.
- Page cache is charged unevenly between panels and between windows: in October Mistgate's binary was copied by another cgroup and its file cache was small; in November the first window after setup held 224-321 MiB of it (the 112 MB binary and the files read during setup), while the other panels read their files under the group. This is why anon is the headline. The cold-cache pass of October (page cache dropped before every start) was not repeated.
- Not measured: VPN data-plane speed beyond Hysteria2 on loopback-like links (the sink and the clients are on the same machine, no network), other protocols, the admin web interface, Telegram bots, upgrade and backup, many nodes, high-latency or lossy paths, TLS in front of the panel, and anything over a week.
