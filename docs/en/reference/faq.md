---
title: FAQ
description: "Short answers to common questions about Mistgate: Docker, memory, apps, size, IPv6, Cloudflare, migration, the license and where to report problems."
---

## Why is there no Docker image?

Mistgate is two static Go binaries: the panel with an embedded SQLite database and admin UI, and the node agent. The panel needs nothing else to run. The agent manages the host itself: nftables tables, sysctl settings, tunnel interfaces, the journald cap, its own hardened systemd unit and its self-update. In a container it would need the host's network and broad privileges anyway, so it runs directly under systemd with a small set of capabilities instead. See [Requirements](../getting-started/requirements.md).

## How much memory does it use?

- **Panel:** 16-20 MiB of private memory idle on a 1 vCPU / 1 GB test server, 0 to 200 users (see [Benchmarks](benchmarks.md); a long-running install with traffic, logs and caches uses more). A password sign-in briefly takes 64 MiB more for the password hash.
- **Node agent:** its systemd unit sizes the limits from the server's RAM: the Go runtime aims at about 60% of RAM, systemd throttles the agent above about 70% and stops it above about 85%. The doctor's **Memory** check warns when the host runs short (see [Health](../operations/health.md)).

## Which apps work?

Each person gets one subscription link, and each app gets the format it reads:

- **Hysteria2**: a subscription app such as Happ (a list of URIs), or an app on the mihomo core, such as kl!ck, Clash Verge or FlClash (a Mihomo YAML profile). A browser that opens the link gets the person's page with instructions.
- **AmneziaWG**: the AmneziaVPN app, with a key per device; kl!ck and the other apps on the mihomo core get it through the subscription.

A Hysteria2 profile with a self-signed certificate is verified with the official Hysteria2 client and with Mihomo; Happ is not verified with it yet. More subscription formats (Xray JSON, sing-box) are **Planned**. See [Subscriptions](../guide/subscriptions.md) and [Client apps](../guide/client-apps.md): kl!ck is the app Mistgate recommends on Windows and macOS.

## How many users and nodes can it handle?

There is no built-in limit on users or nodes. Mistgate is built for a small fleet run by one person: one panel with one SQLite database, and nodes that each hold one connection to it.

## Does it support IPv6?

- Nodes do not need IPv6. The doctor's **IPv6** check warns only when a node has an IPv6 address that cannot connect out, because clients that get AAAA answers then stall.
- WARP uses its IPv4 endpoint, so a node without IPv6 is fine for WARP.
- The panel listens on whatever `--listen` names; `:443` covers IPv4 and IPv6.
- Rate limits and sign-in lockouts treat one IPv6 /64 as one client.

## Can the panel run behind Cloudflare?

The decoy site, the admin and the subscriptions can sit behind Cloudflare's proxy, with three things to set up:

1. **Certificates.** Let's Encrypt through `--acme-domain` expects the panel to be reached directly. Behind the proxy, give the panel your own certificate with `--tls-cert` and `--tls-key`, for example a Cloudflare origin certificate.
2. **Client addresses.** List Cloudflare's address ranges with `--trusted-proxy`, so that rate limits, sessions and the audit log see the real client addresses.
3. **Node agents.** Agents need a direct TLS connection: they use mutual TLS with the panel's own CA, selected by a secret TLS name, which a proxy that ends TLS breaks. Give them a separate listener with `--agent-listen` and put an address that is not proxied (an IP, or a DNS-only name) into `--agent-addr`. Nodes enrolled earlier keep dialing the address of their install command: enroll them again with a new install command.

Cloudflare then sees the admin's traffic, including its secret address. The Cloudflare Turnstile check at sign-in is separate from this and works either way (see [Security](../operations/security.md)). See [Configuration](configuration.md) for the flags and [Install the panel](../getting-started/install-panel.md) for running the panel behind a reverse proxy.

## Can I migrate from another panel?

There is no migration tool. Set up the panel, add the servers as nodes and create the users again. On a server that ran another VPN stack, the doctor's **Leftovers of other VPNs** and **Foreign firewall rules** checks show what is still there (x-ui, Xray, old WireGuard interfaces, foreign nftables rules); removing it is your decision.

## Is there a release I can download?

Yes. Every release on [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest) carries Linux binaries of the panel and the node agent for amd64 and arm64, with the signed manifests that let a panel fetch node agents and update itself; see [Install the panel](../getting-started/install-panel.md). For anything else build from source with `make build`. Mistgate is still before 1.0: the API, the stored settings and the node protocol may change.

## What does the AGPL mean for me?

Mistgate is licensed under the GNU Affero General Public License v3.0 only. Running it, unmodified, for yourself and your users needs nothing special. If you modify the panel and let others use your version over a network, section 13 of the license asks you to offer them the source code of your version.

The admin shows a **Source code** link next to the version. By default it points to the project's repository; a fork points it at its own with `serve --source-url <url>` (or `MISTGATE_SOURCE_URL`). An empty value hides the link. This is a summary, not legal advice: the `LICENSE` file in the repository is what counts.

## Where do I report bugs and vulnerabilities?

- **Bugs and questions**: open an issue in the GitHub repository. Include the panel version (shown in the admin and printed by `mistgate version`) and the node's agent version.
- **Vulnerabilities**: report them privately, never in a public issue: open the repository's **Security** tab and choose **Report a vulnerability**. See [Security](../operations/security.md).
