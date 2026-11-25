---
title: Comparison with other panels
seoTitle: "Mistgate vs 3x-ui, Marzban, Remnawave: panel comparison"
description: How Mistgate compares with 3x-ui, Marzban, PasarGuard, Remnawave, Hiddify Manager and s-ui, where the others are ahead, and the sources, as checked on 2026-10-09 and in part on 2026-11-24.
---

This page compares Mistgate with the self-hosted panels people most often run for the same job. Every cell was checked on 2026-10-09 against the project's own README, documentation and release notes; "?" means they do not say. On 2026-11-24 the cells that said "?" or "partial" in the README table, and the rows added to it, were checked again. Projects change fast: check the sources before you decide, and open an issue when a cell is out of date.

## Where Mistgate is different

- **Nodes are added from the panel.** Over SSH with a password: the panel pins the host key, checks the server, prepares an active host firewall and installs the signed agent. The other panels add a node by hand: a full panel per node, a node package or a generated compose file.
- **No Docker, no database server.** Two static Go binaries, SQLite and systemd.
- **AmneziaWG is a first-class protocol.** Versions 2.0 and 3.1, a separate key per device (a lost phone is revoked without touching the others), `vpn://` keys for AmneziaVPN. 3x-ui also ships AmneziaWG 3.1 in userspace.
- **Checks from the client's side.** The panel connects to every profile on every node the way an app does; each node runs a doctor with fixes you confirm; alerts open, close and go to Telegram.
- **Signed releases.** Reproducible builds signed with an offline key; the panel and the nodes install only signed builds.
- **Hidden by default.** A decoy site on the public address; the admin behind a secret path, host or listener.
- **A built-in MCP server** for AI agents, with plan / apply and owner approvals.

## Where the others are ahead

- **Protocols.** VMess, Trojan, Shadowsocks, TUIC and plain WireGuard are in most other panels. Mistgate has Hysteria2, VLESS (REALITY over XHTTP or TCP with Vision; TLS over XHTTP or WebSocket, direct or behind a CDN), AmneziaWG and, experimental, MTProto for the Telegram app only and TrustTunnel (a VPN over HTTPS with official apps).
- **Subscription formats.** Others serve Xray JSON, sing-box, Clash, Stash or Outline. Mistgate serves a URI list, Mihomo YAML, Xray JSON, sing-box JSON and AmneziaVPN keys; not every profile fits every format, see [what works where](../guide/subscriptions.md#what-works-where).
- **Community and maturity.** 3x-ui has about 47.7k GitHub stars, s-ui about 10.0k, Hiddify Manager about 9.3k. Mistgate is a new project in early releases.
- **Device limits.** 3x-ui, PasarGuard and Remnawave limit devices by HWID. Mistgate does too, and gives each device its own keys, online status and traffic, so one device can be removed or blocked without touching the others.
- **Admin roles.** PasarGuard and Hiddify Manager have several admins with roles; the Remnawave documentation says it has no multi-admin support. Mistgate has one admin today; the three roles (owner, helper, read-only) apply to API tokens, see [Security](../operations/security.md#admin-roles).
- **Telegram.** 3x-ui, Marzban, PasarGuard and Hiddify Manager have full Telegram bots. Mistgate sends alerts; a full bot is **Planned**.
- **Installation.** Mistgate's one-line installer verifies the signed panel manifest before installing the panel.

## The full table

| | Mistgate | 3x-ui | Marzban | PasarGuard | Remnawave | Hiddify Manager | s-ui |
|:--|:--|:--|:--|:--|:--|:--|:--|
| Backend | Go | Go | Python, React | Python, React | NestJS (TypeScript) | Python | Go |
| Database | SQLite | SQLite or PostgreSQL | SQLite, MySQL or MariaDB | SQLite, MySQL, MariaDB, PostgreSQL or TimescaleDB | PostgreSQL, Redis | MySQL, Redis | SQLite |
| Licence | AGPL-3.0 | GPL-3.0 | AGPL-3.0 | AGPL-3.0 | AGPL-3.0 | GPL-3.0 | GPL-3.0 |
| Docker | not used: systemd, static binaries | optional | partial: the installer uses Compose; a manual install is documented | partial: Compose | required for the panel and the node | optional | optional |
| One-line installer | yes | yes | yes | yes | no: Compose, community scripts | yes | yes |
| Memory | panel: 39-44 MiB idle, measured¹; node: the SSH installer needs 256 MB | 73-76 MiB idle, measured¹ | ? | 273-277 MiB idle, measured¹; documented: 1 GB minimum, 2 GB recommended | 436-464 MiB idle, measured¹; documented: panel 2 GB minimum, 4 GB recommended; node 1 GB minimum | ? | ? |
| Core | Hysteria2; xray-core for VLESS; AmneziaWG in userspace or the kernel | Xray, TUIC in process, AmneziaWG | Xray | Xray, a WireGuard core; a sing-box node announced | Xray | Xray and sing-box | sing-box |
| VLESS REALITY / XHTTP | yes / yes | yes / yes | yes / ? | yes / ? | yes / yes | yes / yes | yes / ? |
| VMess, Trojan, Shadowsocks | no | yes | yes | yes | partial: Trojan and Shadowsocks; VMess not listed | yes | yes |
| Hysteria2 | yes | yes | ? | yes | partial: in Xray JSON client configs only | yes | yes |
| TUIC | no | yes (v5) | ? | partial: a sing-box core, in the source only | no (not in the source) | yes | yes |
| WireGuard | no | yes | ? | yes | partial: core-level setup only | yes | yes |
| AmneziaWG | yes: 2.0 and 3.1, a key per device | yes: 3.1 in userspace | ? | no (not in the source) | no (not in the source) | no (not in the source) | no (not in the source) |
| MTProto (Telegram proxy) | experimental: Fake TLS, a key per person, for the Telegram app only | yes: a bundled `mtg` process, a FakeTLS secret per client | ? | yes (in the source) | no (not in the source) | yes: MTProxy FakeTLS and a web proxy | no (not in the source) |
| TrustTunnel | experimental: a VPN over HTTPS, `tt://` keys for the official apps | no (not in the source) | ? | no (not in the source) | no (not in the source) | no (not in the source) | no (not in the source) |
| Several nodes | yes: an agent over mutual TLS | yes: a master panel manages other 3x-ui panels | yes: marzban-node | yes | yes | yes: parent and child panels | ? |
| How a node is added | from the panel over SSH, or a one-time command | by hand: a full 3x-ui per node | by hand | by hand | by hand: a generated compose file | by hand: a child panel | ? |
| Relay through an entry node | yes: a forward from an entry node to an exit node, set up in the panel, checked before it is switched on and every 5 minutes | partial: outbound chaining, by hand | ? | ? | partial: a documented bridge, by hand | yes: relay domains; the relay server is set up by script | ? |
| Subscription formats | URI list, Mihomo YAML, AmneziaVPN `vpn://` keys, Xray JSON, sing-box JSON, a web page | base64, Xray JSON, Clash/Mihomo | V2ray, Clash, ClashMeta | V2ray, Clash, ClashMeta, Xray JSON, sing-box, Outline | Xray JSON, base64, Mihomo, Stash, Clash, sing-box | sub, sub64, Xray, sing-box, Clash, ClashMeta, WireGuard | links, sing-box, Clash.Meta |
| Traffic and term per user | yes | yes | yes | yes | yes | yes | yes |
| Device limit | yes: HWID, a place and keys per device | yes: HWID and IP | ? | yes: HWID | yes: HWID | ? | ? |
| Several admins with roles | no: one admin; roles apply to API tokens | ? | partial: multi-admin, work in progress | yes: roles with scopes (RBAC) | no | yes: super admin, admin, agent | ? |
| Telegram bot | partial: alerts; a full bot **Planned** | yes (and Discord) | yes | yes | partial: notifications, Telegram login | yes | partial: a third-party bot only |
| API | yes: Connect, readonly / operator / admin tokens | yes: REST, scoped tokens | yes: REST, webhooks | yes: REST, webhooks | yes: OpenAPI, scoped tokens, webhooks | yes | yes |
| AI agents (MCP) | yes: built in, plan / apply, owner approvals | no (not in the source) | ? | ? | no first-party server; community ones exist | ? | ? |
| WARP exit | yes: per profile, fails closed | yes | ? | partial: a guide | partial: community | yes | yes (in the source) |
| Health and alerts | yes: client-eye checks, node doctor with fixes, alert lifecycle, Telegram | yes: Telegram alerts, an opt-in tunnel monitor | partial | partial | yes: status, Prometheus, traffic alerts | partial | partial: status only |
| Torrent blocking | yes: in the kernel; an event keeps what matched and the destination port, no addresses | ? | ? | ? | yes: a plugin | ? | ? |
| Decoy site | yes | partial: Hysteria2 masquerade only | yes: a home page template | ? | ? | yes: a Decoy Site setting | ? |
| Signed releases | yes: offline key, reproducible, nodes install only signed builds | partial: `.sha256` files | ? | ? | ? | ? | ? |
| Edition on Cloudflare Workers | preview: [not yet run on a real Cloudflare account](../getting-started/install-edge.md) | no | no | no | no | no | no |
| GitHub stars | 1 | about 47.7k | about 7.4k | about 2.7k | about 5.2k | about 9.3k | about 10.0k |
| Latest release | v0.2.1 | v3.11.0 (2026-10-25) | v0.8.4 (2025-01-09) | v5.6.0 (2026-11-03) | 3.4.11 (2026-11-14) | v13.0.3 (2026-09-26) | v1.6.4 (2026-10-07) |

¹ Measured by this project on one test machine: private memory (anon) of the whole panel group, idle, with 0 to 200 users and no node connected, on a 1 vCPU / 1 GB server (3x-ui includes its Xray core and fail2ban). Marzban, Hiddify Manager and s-ui were not measured.

On a 2 vCPU / 2 GB server with the VPN core, the database and Hysteria2 traffic, Mistgate (panel and node) held 92 MiB idle and 123 MiB on a "bad day" (64 tunnel streams, 300 subscription requests per second and one user added and one removed per second); the same figures were 81 and 118 MiB for 3x-ui, 312 and 374 MiB for PasarGuard, 815 and 1,159 MiB for Remnawave. On that server 3x-ui uses less memory than Mistgate; the panel alone is smaller in Mistgate (39-44 MiB against 73-76). The method, the versions and where Mistgate is slower are on the [Benchmarks](benchmarks.md) page. The end-to-end test checks the panel's idle memory against 80 MB and the node agent's against 100 MB.

Marzban gets maintenance only (its last release is from January 2025), and its users are moving to PasarGuard, a successor developed in a separate repository rather than a GitHub fork. Marzneshin is dormant.

## Sources

Checked on 2026-10-09. Each project's README, documentation and release notes; the stars and the latest releases from the GitHub API on that day. On 2026-11-24 the latest releases of Mistgate, 3x-ui, PasarGuard and Remnawave were checked again (GitHub releases, for the [benchmark](benchmarks.md)), and so were the cells that said "?" or "partial" in the README table and the rows added to it (TrustTunnel, Telegram proxy, WireGuard, TUIC, relay, decoy site, device limit, admins), against the pages listed under [the 2026-11-24 answers](#the-2026-11-24-answers); the rest of the table was not.

| Project | Repository | Documentation |
|:--|:--|:--|
| Mistgate | [Mistgate/mistgate](https://github.com/Mistgate/mistgate) | this documentation |
| 3x-ui | [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui) | [docs.sanaei.dev](https://docs.sanaei.dev/) |
| Marzban | [Gozargah/Marzban](https://github.com/Gozargah/Marzban) | [gozargah.github.io/marzban](https://gozargah.github.io/marzban/) |
| PasarGuard | [PasarGuard/panel](https://github.com/PasarGuard/panel) | [docs.pasarguard.org](https://docs.pasarguard.org) |
| Remnawave | [remnawave/panel](https://github.com/remnawave/panel) | [docs.rw](https://docs.rw) |
| Hiddify Manager | [hiddify/Hiddify-Manager](https://github.com/hiddify/Hiddify-Manager) | [hiddify.com](https://hiddify.com) |
| s-ui | [alireza0/s-ui](https://github.com/alireza0/s-ui) | [the wiki](https://github.com/alireza0/s-ui/wiki) |

## The 2026-11-24 answers

Pages the answers of 2026-11-24 rest on. A cell says "yes", "no" or "partial" when one of them says so. "no (not in the source)" and "yes (in the source)" come from a search in the project's source code (below). Everything else is "?".

- **3x-ui:** the [README](https://github.com/MHSanaei/3x-ui#readme) (the protocol list, TUIC, WireGuard, HWID device limits, outbound proxy chaining); [Inbounds](https://docs.sanaei.dev/docs/config/inbounds) (MTProto served by a bundled `mtg` process); [Clients](https://docs.sanaei.dev/docs/config/clients) (HWID devices); [Transports](https://docs.sanaei.dev/docs/config/transports) (the Hysteria2 `masquerade`, `dialerProxy`); [TUIC](https://docs.sanaei.dev/docs/config/tuic).
- **Marzban:** the [README](https://github.com/Gozargah/Marzban#readme) ("Multi-admin support (WIP)", `HOME_PAGE_TEMPLATE`, "Decoy page template").
- **PasarGuard:** the [README](https://github.com/PasarGuard/panel#readme) (the protocol list, HWID device limits, multi-admin with RBAC); [Admin roles](https://docs.pasarguard.org/en/panel/admin_roles); [Settings](https://docs.pasarguard.org/en/panel/settings) (subscription formats, `xray` among them).
- **Remnawave:** [Comparison of functions](https://docs.rw/overview/comparison-of-functions) ("Multi-admin support: No", WireGuard hosts only through the core); [Server-side routing](https://docs.rw/learn-en/server-routing) (a "bridge" set up by hand); [HWID device limit](https://docs.rw/features/hwid-device-limit); [Awesome Remnawave](https://docs.rw/awesome-remnawave) (projects of the community).
- **Hiddify Manager:** the [README](https://github.com/hiddify/Hiddify-Manager#readme) (the protocol list, a Telegram proxy, three admin roles); [All settings](https://hiddify.com/manager/configuration-and-advanced-settings/Settings-reference/) ("Decoy Site"); [Telegram proxy](https://hiddify.com/manager/configuration-and-advanced-settings/How-to-set-up-Telegram-proxy-on-Hiddify/); [My account and admin security](https://hiddify.com/manager/configuration-and-advanced-settings/My-account-and-admin-security-in-HiddifyManager/); [Relay domains](https://hiddify.com/manager/domain-worker-cdn-and-tunneling/How-to-add-relay-domain-to-Hiddify-manager/).
- **s-ui:** the [README](https://github.com/alireza0/s-ui#readme) (the protocol list, "Client & Traffic & System Status", a Telegram bot by a third party) and the wiki page [Configuration objects](https://github.com/alireza0/s-ui/wiki/Configuration-Objects) (`reality` in the TLS object).

**How "not in the source" was checked.** On 2026-11-24 the words `trusttunnel`, `mtproto`, `amnezia`, `tuic`, `wireguard` and, for 3x-ui, `modelcontextprotocol` were searched for in the source code of [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui), [remnawave/panel](https://github.com/remnawave/panel), [remnawave/node](https://github.com/remnawave/node), [hiddify/Hiddify-Manager](https://github.com/hiddify/Hiddify-Manager), [hiddify/HiddifyPanel](https://github.com/hiddify/HiddifyPanel), [PasarGuard/panel](https://github.com/PasarGuard/panel), [PasarGuard/node](https://github.com/PasarGuard/node) and [alireza0/s-ui](https://github.com/alireza0/s-ui). No hit gives "no (not in the source)", a hit gives "yes (in the source)" or "partial". A search can miss a feature that goes under another name, so this is weaker than a statement in the documentation. Marzban was not searched. Three findings: the PasarGuard panel has a core type `mtproto` (`app/db/models.py`) and TUIC in its sing-box subscription code (`app/subscription/singbox.py`), neither of which its README or documentation mentions; the Hiddify client apps (hiddify-core, hiddify-app) mention TrustTunnel, the Hiddify Manager panel does not; the only `modelcontextprotocol` hit in 3x-ui is a dependency of its documentation site, not of the panel.
