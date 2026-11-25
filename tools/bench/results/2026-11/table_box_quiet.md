| Metric | 3xui (n=3) | mistgate (n=3) | pasarguard (n=3) | remnawave (n=3) |
|---|---|---|---|---|
| A: private memory, anon+kernel (MiB) | n/a | n/a | n/a | n/a |
| A: anon only (MiB) | n/a | n/a | n/a | n/a |
| A: memory.current (MiB, with page cache) | n/a | n/a | n/a | n/a |
| A: CPU of the box (% of 1 vCPU) | n/a | n/a | n/a | n/a |
| A: processes | n/a | n/a | n/a | n/a |
| A: containers | n/a | n/a | n/a | n/a |
| Setup of 1000 users (s) | 18 (17-19) | 41 (37-53) | 40 (40-41) | 24 (23-26) |
| B1: throughput (Gbit/s) | 3.83 (3.81-3.95) | 5.63 (5.26-5.67) | 3.90 (3.89-3.96) | 3.73 (3.66-3.85) |
| B1: box CPU (% of 1 vCPU) | 180 (177-184) | 176 (172-178) | 178 (178-179) | 185 (179-189) |
| B1: Mbit/s per CPU-second | 2149 (2079-2196) | 3183 (3048-3196) | 2183 (2183-2212) | 2041 (2014-2047) |
| B1: anon+kernel during run (MiB) | 95 (92-97) | 88 (85-89) | 310 (309-310) | 992 (989-1028) |
| B1: small request p95 through the tunnel (ms) | 1.2 (1.1-1.2) | 1.9 (1.8-2.0) | 1.2 (1.1-1.3) | 1.1 (1.0-1.4) |
| B1: client CPU (% of 1 vCPU) | 182 (179-182) | 210 (201-212) | 180 (180-181) | 180 (176-182) |
| B16: throughput (Gbit/s) | 5.65 (5.56-5.67) | 6.14 (6.10-6.20) | 5.70 (5.70-5.76) | 5.57 (5.44-5.57) |
| B16: box CPU (% of 1 vCPU) | 185 (184-185) | 184 (184-185) | 185 (184-185) | 187 (185-192) |
| B16: Mbit/s per CPU-second | 3057 (3000-3063) | 3331 (3316-3358) | 3099 (3087-3120) | 2938 (2896-2985) |
| B16: anon+kernel during run (MiB) | 81 (81-82) | 91 (88-92) | 315 (314-315) | 998 (996-1036) |
| B16: small request p95 through the tunnel (ms) | 1.7 (1.7-2.1) | 2.6 (2.6-2.7) | 2.0 (1.8-2.2) | 2.0 (1.9-2.4) |
| B16: client CPU (% of 1 vCPU) | 289 (288-289) | 301 (300-301) | 289 (289-289) | 284 (282-284) |
| B64: throughput (Gbit/s) | 5.76 (5.73-5.78) | 5.98 (5.93-6.01) | 5.77 (5.63-5.87) | 5.53 (5.47-5.54) |
| B64: box CPU (% of 1 vCPU) | 189 (188-190) | 187 (186-188) | 189 (187-189) | 190 (189-190) |
| B64: Mbit/s per CPU-second | 3040 (3029-3060) | 3189 (3185-3207) | 3053 (3012-3105) | 2912 (2897-2919) |
| B64: anon+kernel during run (MiB) | 89 (89-91) | 99 (98-101) | 323 (322-323) | 984 (974-990) |
| B64: small request p95 through the tunnel (ms) | 2.2 (2.2-2.5) | 2.9 (2.8-3.0) | 2.2 (2.1-2.4) | 2.8 (2.2-2.9) |
| B64: client CPU (% of 1 vCPU) | 340 (338-341) | 334 (331-336) | 340 (333-340) | 329 (325-329) |
| C: clients connected | n/a | n/a | n/a | n/a |
| C: handshake failures | n/a | n/a | n/a | n/a |
| C: handshake p50 (ms) | n/a | n/a | n/a | n/a |
| C: handshake p95 (ms) | n/a | n/a | n/a | n/a |
| C: request failures | n/a | n/a | n/a | n/a |
| C: request p95 (ms) | n/a | n/a | n/a | n/a |
| C: box CPU (% of 1 vCPU) | n/a | n/a | n/a | n/a |
| C: anon+kernel (MiB) | 0 (0-0) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: tunnel (Gbit/s, 64 streams) | 3.82 (3.79-3.85) | 5.31 (5.23-5.32) | 3.50 (3.47-3.51) | 2.60 (2.59-2.63) |
| D: tunnel drop vs B64 (%) | 34 (33-34) | 12 (10-12) | 39 (38-41) | 53 (52-53) |
| D: tunnel streams dropped | 512 (512-512) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: tunnel reconnects | 6 (5-6) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: subscription storm (requests/s, all responses) | 124 (124-125) | 300 (300-300) | 109 (107-109) | 300 (300-300) |
| D: subscription storm 200 responses/s | 124 (124-125) | 300 (300-300) | 109 (107-109) | 300 (300-300) |
| D: subscription p95 (ms, 200 only) | 597 (590-601) | 1 (1-1) | 1010 (998-1021) | 47 (46-47) |
| D: subscription non-200 (%) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) |
| D: admin add-user p95 (ms) | 771 (755-1096) | 3 (3-4) | 1562 (1111-1577) | 52 (30-60) |
| D: admin remove-user p95 (ms) | 658 (557-875) | 2 (2-2) | 1056 (1014-1313) | 52 (15-53) |
| D: admin list call p95 (ms) | 407 (393-476) | 13 (12-13) | 999 (829-1147) | 11 (9-49) |
| D: admin API errors | 0 (0-0) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: box CPU (% of 1 vCPU) | 180 (179-181) | 193 (193-194) | 194 (193-194) | 195 (194-197) |
| D: anon+kernel (MiB) | 112 (112-113) | 113 (112-115) | 343 (342-343) | 1145 (1133-1147) |
| E: admin HTTP answers again after (s) | n/a | n/a | n/a | n/a |
| E: admin API answers again after (s) | n/a | n/a | n/a | n/a |
| E: seconds with zero tunnel traffic (of 60) | n/a | n/a | n/a | n/a |
| E: streams dropped | n/a | n/a | n/a | n/a |
| E: tunnel reconnects | n/a | n/a | n/a | n/a |
