| Metric | 3xui (n=1) | mistgate (n=1) | pasarguard (n=1) | remnawave (n=1) |
|---|---|---|---|---|
| A: private memory, anon+kernel (MiB) | 86 (86-86) | 109 (109-109) | 317 (317-317) | 850 (850-850) |
| A: anon only (MiB) | 83 (83-83) | 107 (107-107) | 307 (307-307) | 809 (809-809) |
| A: memory.current (MiB, with page cache) | 93 (93-93) | 116 (116-116) | 333 (333-333) | 877 (877-877) |
| A: CPU of the box (% of 1 vCPU) | 0.49 (0.49-0.49) | 0.19 (0.19-0.19) | 0.29 (0.29-0.29) | 3.68 (3.68-3.68) |
| A: processes | 3 (3-3) | 2 (2-2) | 3 (3-3) | 36 (36-36) |
| A: containers | 1 (1-1) | 1 (1-1) | 2 (2-2) | 4 (4-4) |
| Setup of 1000 users (s) | 19 (19-19) | 55 (55-55) | 41 (41-41) | 30 (30-30) |
| C: clients connected | n/a | n/a | n/a | n/a |
| C: handshake failures | n/a | n/a | n/a | n/a |
| C: handshake p50 (ms) | n/a | n/a | n/a | n/a |
| C: handshake p95 (ms) | n/a | n/a | n/a | n/a |
| C: request failures | n/a | n/a | n/a | n/a |
| C: request p95 (ms) | n/a | n/a | n/a | n/a |
| C: box CPU (% of 1 vCPU) | n/a | n/a | n/a | n/a |
| C: anon+kernel (MiB) | 0 (0-0) | 0 (0-0) | 0 (0-0) | 0 (0-0) |
| D: tunnel streams dropped | 512 (512-512) | 0 (0-0) | 0 (0-0) | 256 (256-256) |
| D: tunnel reconnects | 6 (6-6) | 0 (0-0) | 0 (0-0) | 3 (3-3) |
| D: subscription storm (requests/s, all responses) | 109 (109-109) | 300 (300-300) | 55 (55-55) | 183 (183-183) |
| D: subscription storm 200 responses/s | 109 (109-109) | 300 (300-300) | 55 (55-55) | 100 (100-100) |
| D: subscription p95 (ms, 200 only) | 680 (680-680) | 8 (8-8) | 1805 (1805-1805) | 404 (404-404) |
| D: subscription non-200 (%) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) | 0.0 (0.0-0.0) | 45.4 (45.4-45.4) |
| D: admin add-user p95 (ms) | 703 (703-703) | 19 (19-19) | 2497 (2497-2497) | 310 (310-310) |
| D: admin remove-user p95 (ms) | 1032 (1032-1032) | 2 (2-2) | 2089 (2089-2089) | 99 (99-99) |
| D: admin list call p95 (ms) | 324 (324-324) | 12 (12-12) | 1751 (1751-1751) | 91 (91-91) |
| D: admin API errors | 0 (0-0) | 0 (0-0) | 0 (0-0) | 44 (44-44) |
| D: box CPU (% of 1 vCPU) | 91 (91-91) | 99 (99-99) | 98 (98-98) | 98 (98-98) |
| D: anon+kernel (MiB) | 120 (120-120) | 135 (135-135) | 353 (353-353) | 814 (814-814) |
| E: admin HTTP answers again after (s) | 1.5 (1.5-1.5) | 0.4 (0.4-0.4) | 7.3 (7.3-7.3) | 13.0 (13.0-13.0) |
| E: admin API answers again after (s) | 1.5 (1.5-1.5) | 0.5 (0.5-0.5) | 7.7 (7.7-7.7) | 13.1 (13.1-13.1) |
| E: seconds with zero tunnel traffic (of 60) | 2 (2-2) | 0 (0-0) | 5 (5-5) | 2 (2-2) |
| E: streams dropped | 128 (128-128) | 0 (0-0) | 128 (128-128) | 128 (128-128) |
| E: tunnel reconnects | 2 (2-2) | 0 (0-0) | 1 (1-1) | 2 (2-2) |
