---
title: Status and roadmap
description: What Mistgate releases already do, what is being tested now, what comes next and what 1.0 needs, and the known limits of the current release.
---

Mistgate is in **beta**; the current release is `v0.1.36`. It runs in production, but the API, the stored settings and the node protocol may still change before 1.0. The rest of these docs describe only what the code does today; the plans are on this page. The plans have no dates: "Next" and "Later" give the order, not a schedule.

```roadmap
- status: released
  title: Released
  meta: v0.1.0 – v0.1.36
  lead: Everything below is in a release and in the docs. First what the current release brought, then the rest by admin section.
  cards:
    - title: New in the current release
      icon: sparkle
      release: v0.1.36
      current: true
      points:
        - "**VLESS over TLS**, direct or behind a CDN: XHTTP and WebSocket over TLS next to REALITY. See [VLESS behind a CDN](../guide/vless.md#vless-behind-a-cdn)."
        - "**A decoy site per node**: the built-in one, the panel's site or an archive of its own, for one node or several at once. See [Your own decoy site](../guide/decoy-site.md#nodes-three-options)."
        - "**The Cloudflare edition in a release for the first time**, as a preview: the signed `mistgate-edge.tar.gz` bundle, installed with `mistgate edge deploy`, and SSH installation of nodes from the edition itself. It has not run on a real Cloudflare account yet. See [Install the Cloudflare edition](../getting-started/install-edge.md)."
    - title: Profiles
      section: profiles
      points:
        - "[Hysteria2](../guide/hysteria2.md) on the official core with Salamander."
        - "[AmneziaWG](../guide/amneziawg.md) 2.0 and 3.1 in userspace or as a kernel module, several profiles per node."
        - "[VLESS](../guide/vless.md) on xray-core: REALITY over XHTTP or TCP (RAW + Vision), with the node's own site or another site as the target; TLS over XHTTP or WebSocket, direct or behind a CDN."
        - "[VLESS and Hysteria2 on one node](../guide/vless-and-hysteria2.md), TCP 443 included."
    - title: Nodes
      section: nodes
      points:
        - "[SSH installation](../getting-started/ssh-install.md) from the admin, and through MCP with the owner's approval: a pinned host key, checks before any change, jobs that can be cancelled, retried and resumed after an interruption."
        - "Saved SSH access, encrypted, with a verified password change."
        - "[WARP egress](../guide/warp.md) that fails closed when the WARP link drops, and [torrent protection](../guide/torrent-protection.md) on every node."
        - "[Check ports](../guide/nodes.md#ports): UDP delivery and a TCP connection to every port of the node from another node of the fleet."
        - "[Measuring the network capacity](../guide/nodes.md#measuring-the-network-capacity) of a node's link."
    - title: Users
      section: users
      points:
        - "[Users and groups](../guide/users-and-groups.md): traffic quotas with periods, terms, device limits."
        - "The People tab and bulk actions on several users at once."
    - title: Subscriptions
      section: subscriptions
      points:
        - "One [subscription link](../guide/subscriptions.md) per person: a link list for Happ and similar apps, Mihomo YAML for apps on the mihomo core."
        - "The [user page](../guide/user-page.md): per-platform instructions, QR codes, the person's own devices, `vpn://` keys for AmneziaVPN."
        - "[DNS presets](../guide/dns.md) and a DNS choice per server on the user page."
    - title: Health
      section: health
      points:
        - "[Client-eye checks](../operations/health.md#client-eye-checks): every 5 minutes the panel connects to the servers the way an app does."
        - "The [node doctor](../operations/health.md#node-doctor): host checks and safe fixes."
        - "[Alerts](../operations/health.md#alerts) in the admin and in Telegram."
        - "The [panel watcher](../operations/panel-watcher.md): a Cloudflare Worker checks the panel every minute and writes to Telegram when it does not answer."
    - title: Updates
      section: updates
      points:
        - "[Signed node agent updates](../operations/updates.md), one node at a time, now or on a schedule, with a health gate and automatic rollback."
        - "Panel self-update from signed GitHub releases, with a rollback when the new panel does not stay up."
        - "[A one-line panel install](../getting-started/install-panel.md#one-line-install) that verifies the release signature."
        - "[Reproducible releases](../operations/releases.md) signed offline from the tag."
    - title: Integrations
      section: integrations
      points:
        - "[API tokens](../reference/api.md) with the readonly, operator and admin profiles."
        - "The [MCP server](../reference/mcp.md) with plan / apply and owner approvals; ready prompts in the [AI agent guide](../getting-started/ai-agents.md)."
        - "[Telegram alerts](../operations/telegram-alerts.md) to the admins who link their chat: health, releases, failed backups, plans waiting for approval, sign-in security."
    - title: Settings
      section: settings
      points:
        - "The admin in English, Russian and Simplified Chinese; sign-in with a passkey or a password with an authenticator code, optional Cloudflare Turnstile, an audit log. See [Security](../operations/security.md)."
        - "A [decoy site](../guide/decoy-site.md) at the panel's address hides the admin from strangers."
        - "[Encrypted backups](../operations/backups.md) to Cloudflare R2 on a schedule, with retention, and an offline restore."
- status: now
  title: Now
  meta: v0.1.34 – v0.1.36
  lead: What the latest releases added is being tested in production; fixes and corrections to the docs come out of it.
  cards:
    - title: VLESS in production
      section: profiles
      text: "REALITY since v0.1.34, the node's own site as the target since v0.1.35, TLS and a CDN since v0.1.36. VLESS has not been tried behind a real CDN yet: in the end-to-end test the node itself stands in for the CDN. See [VLESS behind a CDN](../guide/vless.md#vless-behind-a-cdn)."
    - title: A decoy site on every node
      section: nodes
      text: "The built-in site, the panel's site or an archive of its own for each node. Nodes created before v0.1.36 stay on the built-in page until you pick another option. See [Your own decoy site](../guide/decoy-site.md#nodes-three-options)."
- status: next
  title: Next
  lead: The next items in the queue.
  cards:
    - title: The Cloudflare edition on a real account
      icon: globe
      tag: Preview
      text: "The first run of `mistgate edge deploy` on a real Cloudflare account. So far the command and the panel have been tested against a simulated Cloudflare only. See [Install the Cloudflare edition](../getting-started/install-edge.md)."
    - title: The Cloudflare edition on par with the VPS
      icon: layers
      text: "Bring over what it does not have yet:"
      points:
        - "encrypted backups and restore;"
        - "Telegram alerts;"
        - "MCP and new API tokens;"
        - "client-eye tunnel checks;"
        - "node and panel updates from the admin;"
        - "live node logs, an Own site decoy for a node, and WARP registration."
    - title: Xray JSON and sing-box subscriptions
      section: subscriptions
      text: "Ready configs for apps on the Xray and sing-box cores, next to the link list and Mihomo YAML. See [Subscriptions](../guide/subscriptions.md#formats)."
- status: later
  title: Later
  lead: Planned, but not next in the queue.
  cards:
    - title: A Telegram bot beyond alerts
      section: integrations
      text: "Today the bot only sends alerts and links an admin's chat. Later: running the fleet from Telegram, and a bot for users. See [Telegram alerts](../operations/telegram-alerts.md)."
    - title: Subscription mirrors
      section: subscriptions
      text: "Copies of the subscription at other addresses, so that apps still get it when the panel's address is unreachable."
    - title: Moving between the VPS and the Cloudflare edition
      icon: refresh
      text: "Moving users, nodes and subscription links either way, with the links still working. Today a fleet is set up again for the other edition. See [Moving](../getting-started/install-edge.md#moving-between-a-vps-panel-and-the-edge)."
- status: goal
  title: Road to 1.0
  lead: The conditions for 1.0. Until then, breaking changes are possible.
  cards:
    - title: The node protocol and the stored settings are fixed
      text: "A panel and agents of different 1.x versions understand each other, and the panel's data moves from version to version without manual steps."
    - title: Upgrades from older versions are tested
      text: "Every release passes an automated upgrade test from earlier versions: a panel with its data and the nodes connected to it."
    - title: A stable API
      text: "The API, API tokens and MCP tools without breaking changes within 1.x. See [API](../reference/api.md) and [MCP](../reference/mcp.md)."
```

## Known limits

- Client-eye checks skip VLESS when the node agent does not report the required capability. Agents before v0.1.34 have no VLESS engine; TLS profiles also require `vless-tls/1` (v0.1.36 or later).
- **Check ports** looks from one vantage point: another node of the fleet or the panel host. On VLESS ports it runs a TLS handshake with the expected name, not a REALITY handshake.
- The SSH installation logs in with a password only (keyboard-interactive included); key login is not supported. Use the [manual install](../getting-started/add-node.md) for such servers.
- API tokens and MCP cannot create profiles or groups, or put a profile on a node: the owner does that in the admin.
- Backups go to Cloudflare R2 only, and a restore is the offline `mistgate backup restore` command into a new data directory.
- The panel does not create a Cloudflare account, a bucket or a token: you supply a bucket-scoped token with read, write and delete access.
- The Cloudflare edition is a preview: some features are not available in it yet (see [the list](../getting-started/install-edge.md#not-available-in-this-edition-yet)), and a fleet cannot be moved between it and a VPS panel.
- Some defaults lean towards users in Russia (Yandex DNS for nodes in Russia, the doctor's control domains, split-DNS presets); all of them are settings.
