---
title: Mistgate documentation
description: What is in the documentation, where to find it, and the order to read it in.
---

Mistgate is a self-hosted panel for your own VPN fleet: one binary for the panel, one for the node agent, Hysteria2 and AmneziaWG in one subscription. This documentation describes what the code does today; features that are still being built are marked **Planned**.

## What is where

| Section | For |
|:--|:--|
| Getting started | The concepts, what you need, and the path from an empty server to the first user with a working subscription. |
| Guide | Day-to-day work in the admin: nodes, profiles, protocols, WARP, users, subscriptions, client apps, the user page, DNS. |
| Operations | Keeping the fleet healthy, updated and safe, and what to do when something breaks. |
| Roadmap | What is done, what is being tested, what comes next, and the known limits. |
| Reference | Exact commands, flags, environment variables, the API, the MCP server and how the parts fit together. |

## Reading order for a new admin

1. [Overview](getting-started/overview.md) and [Requirements](getting-started/requirements.md): what a panel, a node and a profile are, and what servers you need.
2. [Install the panel](getting-started/install-panel.md) from the official release, by hand or with the install prompt of the [AI agent guide](getting-started/ai-agents.md).
3. [Install a node over SSH](getting-started/ssh-install.md), or [add a node](getting-started/add-node.md) with a command you run yourself.
4. [First users](getting-started/first-users.md): a profile on the node, a group, a user, a subscription link.
5. [Health](operations/health.md), [Security](operations/security.md) and [encrypted backups](operations/backups.md) before you give links to other people.
6. The Guide pages as you need them; the Reference when you script or automate.

## All pages

### Getting started

- [Overview](getting-started/overview.md): what Mistgate is and its concepts: panel, node, profile, server on a node, user, group, device, subscription, user page.
- [Requirements](getting-started/requirements.md): what the panel and the nodes need, and what you need to build from source.
- [Install the panel](getting-started/install-panel.md): from a fresh Linux server to the owner account in the admin.
- [Add a node](getting-started/add-node.md): the two ways to add a node, the manual install command, and removing a node.
- [Install a node over SSH](getting-started/ssh-install.md): the panel installs the agent itself; install jobs and the saved SSH access.
- [AI agent guide](getting-started/ai-agents.md): ready prompts to install the panel, run the fleet through MCP and contribute to the code.
- [First users](getting-started/first-users.md): put a profile on a node, give it to a group, create a user and send the link.

### Guide

- [Nodes](guide/nodes.md): node settings, status and what the agent does on the host.
- [Profiles](guide/profiles.md): what a profile is and how it becomes a server on a node.
- [Hysteria2](guide/hysteria2.md): Hysteria2 profile settings, certificates and obfuscation.
- [AmneziaWG](guide/amneziawg.md): AmneziaWG 2.0 and 3.1 profiles, userspace or the kernel module, devices and keys.
- [WARP](guide/warp.md): sending a profile's traffic out through Cloudflare WARP.
- [Users and groups](guide/users-and-groups.md): users, groups, devices, traffic limits and terms.
- [Subscriptions](guide/subscriptions.md): one link per person and which format each app gets.
- [Client apps](guide/client-apps.md): which app on which platform, what each receives, one-tap add links, kl!ck, and setting the apps up in the admin and through MCP.
- [User page](guide/user-page.md): the person's own page with instructions, a QR code and traffic.
- [DNS](guide/dns.md): DNS presets for users and groups, and the DNS of the nodes.
- [Torrent protection](guide/torrent-protection.md): the per-node BitTorrent blocker, what it catches and what its events show.

### Operations

- [Health](operations/health.md): client-eye checks, the node doctor and alerts.
- [Updates](operations/updates.md): node self-update, updating a node now or on a schedule, rollbacks, and updating the panel.
- [Releases and signing](operations/releases.md): the release key, reproducible builds, signing and publishing, a bundle of your own.
- [Encrypted backups](operations/backups.md): configure R2, protect the offline recovery identity and restore a panel.
- [Security](operations/security.md): admin sign-in, roles, step-up, sessions, audit, the decoy site and the hidden admin, the data directory and backups, recovering access.
- [Troubleshooting](operations/troubleshooting.md): common problems and how to find their cause.

### Roadmap

- [Status and roadmap](roadmap/status.md): what the current release does, what comes next, and the known limits.

### Reference

- [CLI](reference/cli.md): every `mistgate` and `mistgate-node` command and flag.
- [Configuration](reference/configuration.md): every `serve` and `setup` flag, every `MISTGATE_*` environment variable, and the data directory layout.
- [API](reference/api.md): the Connect API, API tokens and their profiles.
- [MCP](reference/mcp.md): the MCP server, the stdio proxy, the tools, plan / apply and approvals.
- [Architecture](reference/architecture.md): how the panel, the agents and the clients talk to each other.
- [FAQ](reference/faq.md): short answers to common questions.
