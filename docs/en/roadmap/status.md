---
title: Status and roadmap
description: What Mistgate does in the current release, what is being tested now, what comes next, and the known limits.
---

Mistgate is in early releases; the current one is `v0.1.15`. It runs in production for its author, but the API, the stored settings and the node protocol may still change before 1.0. The rest of these docs describe only what the code does today.

## Done

### The fleet

- A panel and a node agent that talk over mutual TLS; a node enrolls with a one-time token and needs no inbound management port. See [Architecture](../reference/architecture.md).
- [Hysteria2](../guide/hysteria2.md) on the official core with Salamander, and [AmneziaWG](../guide/amneziawg.md) 2.0 and 3.1 in userspace or as a kernel module, several profiles per node.
- [WARP egress](../guide/warp.md) that fails closed when the WARP link drops.
- One [subscription](../guide/subscriptions.md) link per person with the format each app reads, the person's [user page](../guide/user-page.md), and [DNS presets](../guide/dns.md).
- [Health](../operations/health.md): client-eye checks, the node doctor with safe fixes, and alerts.
- Per-node [torrent protection](../guide/torrent-protection.md).

### Installing and recovering

- [SSH installation](../getting-started/ssh-install.md) from the admin, and through MCP with the owner's approval: root or `sudo -n`, a pinned host key, checks before any change, the host firewall prepared.
- Install jobs that can be cancelled, retried and resumed after an interruption.
- Saved SSH access, encrypted, with a verified password change; the access outlives a retired node until the owner forgets it.
- [Encrypted backups](../operations/backups.md) to Cloudflare R2 on a schedule, with retention and back-off, and an offline restore.

### Updates

- [Signed node agent updates](../operations/updates.md), one node at a time, now or on a schedule, with a health gate and automatic rollback; signed bundles downloaded from GitHub by themselves.
- Panel self-update from signed GitHub releases, with a rollback when the new panel does not stay up.
- Reproducible releases signed offline from the tag: see [Releases and signing](../operations/releases.md).

### Access

- The admin in English and Russian; sign-in with a passkey or a password with an authenticator code, optional Cloudflare Turnstile, an audit log. See [Security](../operations/security.md).
- [API tokens](../reference/api.md) with the readonly, operator and admin profiles, and the [MCP server](../reference/mcp.md) with plan / apply and owner approvals; ready prompts in the [AI agent guide](../getting-started/ai-agents.md).

## Now

- Field testing of `v0.1.15` in production: torrent protection, the SSH installation and recovery, signed panel and node releases.

## Next

- A Telegram bot for the whole fleet.
- More subscription formats (Xray JSON, sing-box) and subscription mirrors.
- A one-line installer.

## Later

- VLESS REALITY as the first external protocol plugin.

## Known limits

- The SSH installation logs in with a password only; key login is not supported. Use the [manual install](../getting-started/add-node.md) for such servers.
- API tokens and MCP cannot create profiles or groups, or put a profile on a node: the owner does that in the admin.
- Backups go to Cloudflare R2 only, and a restore is a command-line step into a new data directory.
- The panel does not create a Cloudflare account, a bucket or a token: you supply a bucket-scoped token with read, write and delete access.
- Some defaults lean towards users in Russia (Yandex DNS for nodes in Russia, the doctor's control domains, split-DNS presets); all of them are settings.
