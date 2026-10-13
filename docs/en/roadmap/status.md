---
title: Status and roadmap
description: What Mistgate does in the current release, what is being tested now, what comes next, and the known limits.
---

Mistgate is in **beta**; the current release is `v0.1.35`. It runs in production, but the API, the stored settings and the node protocol may still change before 1.0. The rest of these docs describe only what the code does today.

## Done

### The fleet

- A panel and a node agent that talk over mutual TLS; a node enrolls with a one-time token and needs no inbound management port. See [Architecture](../reference/architecture.md).
- [Hysteria2](../guide/hysteria2.md) on the official core with Salamander, and [AmneziaWG](../guide/amneziawg.md) 2.0 and 3.1 in userspace or as a kernel module, several profiles per node.
- [VLESS REALITY](../guide/vless.md) on xray-core, over XHTTP or TCP (RAW + Vision), with the node's own site (its own Let's Encrypt certificate, renewed over TCP 443) or another site as the REALITY target. VLESS servers come in the link list (Happ and similar apps) and in the Mihomo YAML. Hysteria2 and VLESS can share a node, TCP 443 included: see [VLESS and Hysteria2 on one node](../guide/vless-and-hysteria2.md).
- [WARP egress](../guide/warp.md) that fails closed when the WARP link drops.
- One [subscription](../guide/subscriptions.md) link per person with the format each app reads, the person's [user page](../guide/user-page.md), and [DNS presets](../guide/dns.md).
- [Health](../operations/health.md): client-eye checks, the node doctor with safe fixes, and alerts.
- Per-node [torrent protection](../guide/torrent-protection.md).
- **Check ports** on the node page: a real TCP connection from another node to each TCP port of the node (and to port 80 when it has an ACME name), which tells whether it reached Mistgate itself. See [Nodes](../guide/nodes.md).

### Installing and recovering

- [SSH installation](../getting-started/ssh-install.md) from the admin, and through MCP with the owner's approval: root or `sudo -n`, a pinned host key, checks before any change, the host firewall prepared.
- Install jobs that can be cancelled, retried and resumed after an interruption.
- Saved SSH access, encrypted, with a verified password change; the access outlives a retired node until the owner forgets it.
- The one-line panel installer verifies the signed release before setup and systemd installation.
- [Encrypted backups](../operations/backups.md) to Cloudflare R2 on a schedule, with retention and back-off, and an offline restore.
- [Telegram alerts](../operations/telegram-alerts.md): health alerts, releases, failed backups, plans waiting for approval and sign-in security, sent to the admins who link their chat.

### Updates

- [Signed node agent updates](../operations/updates.md), one node at a time, now or on a schedule, with a health gate and automatic rollback; signed bundles downloaded from GitHub by themselves.
- Panel self-update from signed GitHub releases, with a rollback when the new panel does not stay up.
- Reproducible releases signed offline from the tag: see [Releases and signing](../operations/releases.md).

### Access

- The admin in English and Russian; sign-in with a passkey or a password with an authenticator code, optional Cloudflare Turnstile, an audit log. See [Security](../operations/security.md).
- [API tokens](../reference/api.md) with the readonly, operator and admin profiles, and the [MCP server](../reference/mcp.md) with plan / apply and owner approvals; ready prompts in the [AI agent guide](../getting-started/ai-agents.md).

## Now

- Field testing of the current releases in production: torrent protection, the SSH installation and recovery, signed panel and node releases.
- VLESS in the field: the first releases with it are v0.1.34 and v0.1.35.
- The edition of the panel that runs on Cloudflare Workers: a preview, not in a release. `mistgate edge deploy` installs it and is tested against a simulated Cloudflare; the first run on a real account is still ahead. Backups, Telegram alerts, MCP, tunnel checks and node and panel updates are not available in it yet. See [Install the Cloudflare edition](../getting-started/install-edge.md).

## Next

- A Telegram bot beyond alerts: for the fleet and for users.
- VLESS behind a CDN or a TLS front: XHTTP and WebSocket over TLS (in progress).
- More subscription formats (Xray JSON, sing-box) and subscription mirrors.

## Known limits

- The client-eye check of the panel does not test VLESS servers yet, so a VLESS server is not marked **No traffic** by it. **Check ports** shows only whether a TCP connection reaches the node; it does not run a REALITY handshake.
- The SSH installation logs in with a password only; key login is not supported. Use the [manual install](../getting-started/add-node.md) for such servers.
- API tokens and MCP cannot create profiles or groups, or put a profile on a node: the owner does that in the admin.
- Backups go to Cloudflare R2 only, and a restore is a command-line step into a new data directory.
- The panel does not create a Cloudflare account, a bucket or a token: you supply a bucket-scoped token with read, write and delete access.
- Some defaults lean towards users in Russia (Yandex DNS for nodes in Russia, the doctor's control domains, split-DNS presets); all of them are settings.
