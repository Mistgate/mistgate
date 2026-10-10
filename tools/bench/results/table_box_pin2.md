| Metric | 3xui (n=1) | mistgate (n=1) | pasarguard (n=1) | remnawave (n=1) |
|---|---|---|---|---|
| A: private memory, anon+kernel (MiB) | n/a | n/a | n/a | n/a |
| A: anon only (MiB) | n/a | n/a | n/a | n/a |
| A: memory.current (MiB, with page cache) | n/a | n/a | n/a | n/a |
| A: CPU of the box (% of 1 vCPU) | n/a | n/a | n/a | n/a |
| A: processes | n/a | n/a | n/a | n/a |
| A: containers | n/a | n/a | n/a | n/a |
| Setup of 1000 users (s) | 18 (18-18) | 54 (54-54) | 40 (40-40) | 27 (27-27) |
| B1: throughput (Gbit/s) | 2.83 (2.83-2.83) | 4.15 (4.15-4.15) | 2.80 (2.80-2.80) | 2.72 (2.72-2.72) |
| B1: box CPU (% of 1 vCPU) | 131 (131-131) | 142 (142-142) | 130 (130-130) | 134 (134-134) |
| B1: Mbit/s per CPU-second | 2156 (2156-2156) | 2918 (2918-2918) | 2151 (2151-2151) | 2026 (2026-2026) |
| B1: anon+kernel during run (MiB) | 85 (85-85) | 49 (49-49) | 298 (298-298) | 992 (992-992) |
| B1: small request p95 through the tunnel (ms) | 4.0 (4.0-4.0) | 4.9 (4.9-4.9) | 4.5 (4.5-4.5) | 4.5 (4.5-4.5) |
| B1: client CPU (% of 1 vCPU) | 169 (169-169) | 195 (195-195) | 168 (168-168) | 168 (168-168) |
| B16: throughput (Gbit/s) | 3.96 (3.96-3.96) | 4.63 (4.63-4.63) | 3.91 (3.91-3.91) | 3.81 (3.81-3.81) |
| B16: box CPU (% of 1 vCPU) | 158 (158-158) | 158 (158-158) | 155 (155-155) | 158 (158-158) |
| B16: Mbit/s per CPU-second | 2510 (2510-2510) | 2935 (2935-2935) | 2532 (2532-2532) | 2411 (2411-2411) |
| B16: anon+kernel during run (MiB) | 78 (78-78) | 55 (55-55) | 303 (303-303) | 998 (998-998) |
| B16: small request p95 through the tunnel (ms) | 4.1 (4.1-4.1) | 6.5 (6.5-6.5) | 5.0 (5.0-5.0) | 5.2 (5.2-5.2) |
| B16: client CPU (% of 1 vCPU) | 253 (253-253) | 272 (272-272) | 249 (249-249) | 248 (248-248) |
| B64: throughput (Gbit/s) | 4.26 (4.26-4.26) | 4.55 (4.55-4.55) | 4.29 (4.29-4.29) | 4.16 (4.16-4.16) |
| B64: box CPU (% of 1 vCPU) | 163 (163-163) | 165 (165-165) | 163 (163-163) | 166 (166-166) |
| B64: Mbit/s per CPU-second | 2613 (2613-2613) | 2756 (2756-2756) | 2633 (2633-2633) | 2514 (2514-2514) |
| B64: anon+kernel during run (MiB) | 86 (86-86) | 63 (63-63) | 311 (311-311) | 995 (995-995) |
| B64: small request p95 through the tunnel (ms) | 5.6 (5.6-5.6) | 7.3 (7.3-7.3) | 5.5 (5.5-5.5) | 6.3 (6.3-6.3) |
| B64: client CPU (% of 1 vCPU) | 309 (309-309) | 315 (315-315) | 308 (308-308) | 304 (304-304) |
| C: clients connected | n/a | n/a | n/a | n/a |
| C: handshake failures | n/a | n/a | n/a | n/a |
| C: handshake p50 (ms) | n/a | n/a | n/a | n/a |
| C: handshake p95 (ms) | n/a | n/a | n/a | n/a |
| C: request failures | n/a | n/a | n/a | n/a |
| C: request p95 (ms) | n/a | n/a | n/a | n/a |
| C: box CPU (% of 1 vCPU) | n/a | n/a | n/a | n/a |
| C: anon+kernel (MiB) | 0 (0-0) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: tunnel (Gbit/s, 64 streams) | 2.54 (2.54-2.54) | 3.96 (3.96-3.96) | 2.18 (2.18-2.18) | 2.04 (2.04-2.04) |
| D: tunnel drop vs B64 (%) | 40 (40-40) | 13 (13-13) | 49 (49-49) | 51 (51-51) |
| D: tunnel streams dropped | 512 (512-512) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: tunnel reconnects | 7 (7-7) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: small request p95 (ms) | 8.7 (8.7-8.7) | 7.2 (7.2-7.2) | 7.9 (7.9-7.9) | 7.3 (7.3-7.3) |
| D: subscription storm (requests/s, all responses) | 110 (110-110) | 300 (300-300) | 204 (204-204) | 300 (300-300) |
| D: subscription storm 200 responses/s | 110 (110-110) | 300 (300-300) | 204 (204-204) | 300 (300-300) |
| D: subscription p95 (ms, 200 only) | 714 (714-714) | 3 (3-3) | 496 (496-496) | 33 (33-33) |
| D: subscription non-200 (%) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) |
| D: admin add-user p95 (ms) | 640 (640-640) | 6 (6-6) | 590 (590-590) | 19 (19-19) |
| D: admin remove-user p95 (ms) | 784 (784-784) | 6 (6-6) | 491 (491-491) | 23 (23-23) |
| D: admin list call p95 (ms) | 471 (471-471) | 5 (5-5) | 463 (463-463) | 19 (19-19) |
| D: admin API errors | 0 (0-0) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: box CPU (% of 1 vCPU) | 170 (170-170) | 167 (167-167) | 197 (197-197) | 196 (196-196) |
| D: anon+kernel (MiB) | 127 (127-127) | 66 (66-66) | 330 (330-330) | 1183 (1183-1183) |
| E: admin HTTP answers again after (s) | 1.5 (1.5-1.5) | 0.2 (0.2-0.2) | 6.6 (6.6-6.6) | 9.1 (9.1-9.1) |
| E: admin API answers again after (s) | 1.5 (1.5-1.5) | 0.2 (0.2-0.2) | 6.9 (6.9-6.9) | 9.1 (9.1-9.1) |
| E: seconds with zero tunnel traffic (of 60) | 4 (4-4) | 0 (0-0) | 5 (5-5) | 0 (0-0) |
| E: streams dropped | 256 (256-256) | 0 (0-0) | 128 (128-128) | 0 (0-0) |
| E: tunnel reconnects | 3 (3-3) | 0 (0-0) | 1 (1-1) | 0 (0-0) |
| E: throughput over the run (Gbit/s) | 3.81 (3.81-3.81) | 4.41 (4.41-4.41) | 3.86 (3.86-3.86) | 3.88 (3.88-3.88) |
