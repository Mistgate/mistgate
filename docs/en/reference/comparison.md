---
title: Comparison with other panels
description: How Mistgate compares with 3x-ui, Marzban, PasarGuard, Remnawave, Hiddify Manager and s-ui, where the others are ahead, and the sources, as checked on 2026-10-09.
---

This page compares Mistgate with the self-hosted panels people most often run for the same job. Every cell was checked on 2026-10-09 against the project's own README, documentation and release notes; "?" means they do not say. Projects change fast: check the sources before you decide, and open an issue when a cell is out of date.

## Where Mistgate is different

- **Nodes are added from the panel.** Over SSH with a password: the panel pins the host key, checks the server, prepares an active host firewall and installs the signed agent. The other panels add a node by hand: a full panel per node, a node package or a generated compose file.
- **No Docker, no database server.** Two static Go binaries, SQLite and systemd.
- **AmneziaWG is a first-class protocol.** Versions 2.0 and 3.1, a separate key per device (a lost phone is revoked without touching the others), `vpn://` keys for AmneziaVPN. 3x-ui also ships AmneziaWG 3.1 in userspace.
- **Checks from the client's side.** The panel connects to every profile on every node the way an app does; each node runs a doctor with fixes you confirm; alerts open, close and go to Telegram.
- **Signed releases.** Reproducible builds signed with an offline key; the panel and the nodes install only signed builds.
- **Hidden by default.** A decoy site on the public address; the admin behind a secret path, host or listener.
- **A built-in MCP server** for AI agents, with plan / apply and owner approvals.

## Where the others are ahead

- **Protocols.** VMess, Trojan, Shadowsocks, TUIC and plain WireGuard are in most other panels. Mistgate has Hysteria2 and AmneziaWG; VLESS REALITY is not in a release yet.
- **Subscription formats.** Others serve Xray JSON, sing-box, Clash, Stash or Outline. Mistgate serves a URI list, Mihomo YAML and AmneziaVPN keys; Xray JSON and sing-box are **Planned**.
- **Community and maturity.** 3x-ui has about 47.7k GitHub stars, s-ui about 10.0k, Hiddify Manager about 9.3k. Mistgate is a new project in early releases.
- **Device limits.** 3x-ui, PasarGuard and Remnawave limit devices by HWID. Mistgate counts devices.
- **Telegram.** 3x-ui, Marzban, PasarGuard and Hiddify Manager have full Telegram bots. Mistgate sends alerts; a full bot is **Planned**.
- **Installation.** Most others install with one command. Mistgate's one-line installer is **Planned**.

## The full table

| | Mistgate | 3x-ui | Marzban | PasarGuard | Remnawave | Hiddify Manager | s-ui |
|:--|:--|:--|:--|:--|:--|:--|:--|
| Backend | Go | Go | Python, React | Python, React | NestJS (TypeScript) | Python | Go |
| Database | SQLite | SQLite or PostgreSQL | SQLite, MySQL or MariaDB | SQLite, MySQL, MariaDB, PostgreSQL or TimescaleDB | PostgreSQL, Redis | MySQL, Redis | SQLite |
| Licence | AGPL-3.0 | GPL-3.0 | AGPL-3.0 | AGPL-3.0 | AGPL-3.0 | GPL-3.0 | GPL-3.0 |
| Docker | not used: systemd, static binaries | optional | partial: the installer uses Compose; a manual install is documented | partial: Compose | required for the panel and the node | optional | optional |
| One-line installer | **Planned** | yes | yes | yes | no: Compose, community scripts | yes | yes |
| Memory | panel: 16-20 MiB idle, measured¹; node: the SSH installer needs 256 MB | 68-74 MiB idle, measured¹ | ? | 262-265 MiB idle, measured¹; documented: 1 GB minimum, 2 GB recommended | 426-454 MiB idle, measured¹; documented: panel 2 GB minimum, 4 GB recommended; node 1 GB minimum | ? | ? |
| Core | Hysteria2; AmneziaWG in userspace or the kernel; xray-core for VLESS (**Planned**) | Xray, TUIC in process, AmneziaWG | Xray | Xray, a WireGuard core; a sing-box node announced | Xray | Xray and sing-box | sing-box |
| VLESS REALITY / XHTTP | **Planned** (the node engine is merged) | yes / yes | yes / ? | yes / ? | yes / yes | yes / yes | ? (VLESS is in the source) |
| VMess, Trojan, Shadowsocks | no | yes | yes | yes | partial: Trojan and Shadowsocks; VMess not listed | yes | yes (in the source) |
| Hysteria2 | yes | yes | ? | yes | partial: in Xray JSON client configs only | yes | yes (in the source) |
| TUIC | no | yes (v5) | ? | ? | ? | yes | yes (in the source) |
| WireGuard | no | yes | ? | yes | partial: passthrough | yes | ? |
| AmneziaWG | yes: 2.0 and 3.1, a key per device | yes: 3.1 in userspace | ? | ? | ? | ? | ? |
| Several nodes | yes: an agent over mutual TLS | yes: a master panel manages other 3x-ui panels | yes: marzban-node | yes | yes | yes: parent and child panels | ? |
| How a node is added | from the panel over SSH, or a one-time command | by hand: a full 3x-ui per node | by hand | by hand | by hand: a generated compose file | by hand: a child panel | ? |
| Subscription formats | URI list, Mihomo YAML, AmneziaVPN `vpn://` keys, a web page; Xray JSON and sing-box **Planned** | base64, Xray JSON, Clash/Mihomo | V2ray, Clash, ClashMeta | V2ray, Clash, ClashMeta, sing-box, Outline | Xray JSON, base64, Mihomo, Stash, Clash, sing-box | sub, sub64, Xray, sing-box, Clash, ClashMeta, WireGuard | links, sing-box, Clash.Meta |
| Traffic and term per user | yes | yes | yes | yes | yes | yes | yes |
| Device limit | partial: a device count, not HWID | yes: HWID and IP | ? | yes: HWID | yes: HWID | ? | ? |
| Telegram bot | partial: alerts; a full bot **Planned** | yes (and Discord) | yes | yes | partial: notifications, Telegram login | yes | ? |
| API | yes: Connect, readonly / operator / admin tokens | yes: REST, scoped tokens | yes: REST, webhooks | yes: REST, webhooks | yes: OpenAPI, scoped tokens, webhooks | yes | yes |
| AI agents (MCP) | yes: built in, plan / apply, owner approvals | ? | ? | ? | no first-party server; community ones exist | ? | ? |
| WARP exit | yes: per profile, fails closed | yes | ? | partial: a guide | partial: community | yes | yes (in the source) |
| Health and alerts | yes: client-eye checks, node doctor with fixes, alert lifecycle, Telegram | yes: Telegram alerts, an opt-in tunnel monitor | partial | partial | yes: status, Prometheus, traffic alerts | partial | ? |
| Torrent blocking | yes: in the kernel; an event keeps what matched and the destination port, no addresses | ? | ? | ? | yes: a plugin | ? | ? |
| Decoy site | yes | ? | ? | ? | ? | ? | ? |
| Signed releases | yes: offline key, reproducible, nodes install only signed builds | partial: `.sha256` files | ? | ? | ? | ? | ? |
| Edition on Cloudflare Workers | in development | no | no | no | no | no | no |
| GitHub stars | 1 | about 47.7k | about 7.4k | about 2.7k | about 5.2k | about 9.3k | about 10.0k |
| Latest release | v0.1.32 | v3.9.0 (2026-10-03) | v0.8.4 (2025-01-09) | v5.4.1 (2026-09-12) | 3.4.5 (2026-10-06) | v13.0.3 (2026-09-26) | v1.6.4 (2026-10-07) |

¹ Measured by this project on one test machine: private memory (anon) of the whole panel group, idle, with 0 to 200 users and no node connected, on a 1 vCPU / 1 GB server (3x-ui includes its Xray core and fail2ban). Marzban, Hiddify Manager and s-ui were not measured.

On a 2 vCPU / 2 GB server with the VPN core, the database and Hysteria2 traffic, Mistgate (panel and node) held 57 MiB idle and 66 MiB at 64 parallel streams; the same figures were 84 and 93 MiB for 3x-ui, 299 and 315 MiB for PasarGuard, 707 and 598 MiB for Remnawave. The method, the versions and where Mistgate is slower are on the [Benchmarks](benchmarks.md) page. The end-to-end test checks the panel's idle memory against 80 MB and the node agent's against 100 MB.

Marzban gets maintenance only (its last release is from January 2025), and its users are moving to PasarGuard, a successor developed in a separate repository rather than a GitHub fork. Marzneshin is dormant.

## Sources

Checked on 2026-10-09. Each project's README, documentation and release notes; the stars and the latest releases from the GitHub API on that day.

| Project | Repository | Documentation |
|:--|:--|:--|
| Mistgate | [Mistgate/mistgate](https://github.com/Mistgate/mistgate) | this documentation |
| 3x-ui | [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui) | [docs.sanaei.dev](https://docs.sanaei.dev/) |
| Marzban | [Gozargah/Marzban](https://github.com/Gozargah/Marzban) | [gozargah.github.io/marzban](https://gozargah.github.io/marzban/) |
| PasarGuard | [PasarGuard/panel](https://github.com/PasarGuard/panel) | [docs.pasarguard.org](https://docs.pasarguard.org) |
| Remnawave | [remnawave/panel](https://github.com/remnawave/panel) | [docs.rw](https://docs.rw) |
| Hiddify Manager | [hiddify/Hiddify-Manager](https://github.com/hiddify/Hiddify-Manager) | [hiddify.com](https://hiddify.com) |
| s-ui | [alireza0/s-ui](https://github.com/alireza0/s-ui) | [the wiki](https://github.com/alireza0/s-ui/wiki) |
