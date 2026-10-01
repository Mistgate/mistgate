---
title: Overview
description: What Mistgate is and is not, who it is for, and how its parts fit together.
---

Mistgate is a self-hosted panel for a small VPN fleet that you run on your own servers. This page explains what it does, what it does not do, and the concepts the rest of the documentation is built on.

## What Mistgate is

- **Two programs, no Docker.** `mistgate` is the panel. `mistgate-node` is the agent that runs on every VPN server. Both are single Go binaries for Linux (amd64 and arm64).
- **One admin for the whole fleet.** The panel keeps people, protocol setups and servers in one SQLite database and shows them in a web admin in English and Russian.
- **Agents do the work on the servers.** The agent runs the protocols and keeps the host in the state the panel asks for. It dials the panel; the panel never logs in to your servers and needs no management port on them.
- **Protocols.** Hysteria2 (the official core, built into the agent) and AmneziaWG 2.0 and 3.1 (userspace by default, the kernel module per node on request). Cloudflare WARP can be the exit for both.
- **One link per person.** A subscription app that opens the link gets the server list in a format it reads (a list of `hysteria2://` links or a Mihomo YAML profile, chosen by User-Agent rules). A browser gets the person's own page with instructions. AmneziaVPN users get a key per device.
- **Hidden by default.** A stranger who opens the panel's address sees a decoy site. The admin lives behind a secret path, a secret host name or a separate listener, and an unknown subscription link gets the same decoy.
- **Health and updates.** The panel checks every server as a real client would, each node runs a doctor of host checks with a few safe fixes, and alerts say what is wrong in plain words. Node agents update themselves only from bundles signed with your own release key.
- **Automation.** Scripts use the same Connect API as the admin with scoped API tokens; AI agents use the built-in MCP server, and risky changes wait for the owner's approval.

## What it is not

- **Not a VPN service.** You bring the servers and the domain names and you run everything. Mistgate does not rent servers or sell access.
- **Not a billing system.** There are no payments, tariffs or invoices. Traffic quotas, terms and device limits are limits you set yourself.
- **Not a client app.** People connect with existing apps: a subscription app such as Happ, or an app on the mihomo core such as Clash Verge or FlClash, and AmneziaVPN for AmneziaWG keys.
- **Not finished.** There are no binary releases yet, so you build from source, and the API, the stored settings and the node protocol may still change before 1.0. The data model is sized for about 5000 users and 50 nodes.
- **Not a one-click installer.** You install the panel by hand and enroll each node with one command. A one-line installer and node provisioning over SSH from the admin are **Planned**.

## Who it is for

Mistgate is for a person or a small team that runs a few VPN servers for themselves, family, friends or a small community. You should be comfortable with Linux, SSH, systemd and DNS records. Some defaults lean towards users in Russia (Yandex DNS for nodes in Russia, the doctor's control domains, split-DNS presets); all of them are settings.

## Concepts

| Concept | What it is |
|:--|:--|
| Panel | The `mistgate serve` process, one per installation. It holds the database, the master key that encrypts stored secrets, and the panel's own certificate authority (CA). It serves the decoy site, subscriptions and user pages on its public address, the admin on a secret address, and the endpoint the node agents connect to. |
| Node | A Linux server that carries VPN traffic. It has a name (`de1`), the address clients connect to (a domain or an IP), and optionally a country. |
| Agent | The `mistgate-node` service on a node. It holds a client certificate from the panel CA, keeps one connection to the panel, applies what the panel wants on this node and reports traffic, sessions, health and events. |
| Profile | A reusable protocol setup: a protocol plus its settings (port, certificate, obfuscation and so on). A profile does nothing until you put it on a node. |
| Server on a node | A profile put on a node. It is what really listens on the node, and what a person sees as one server in their app. Its port and Domain (SNI) can differ per node. The API calls it an inbound. |
| Group | A set of profiles. Every user is in one group and gets the profiles of that group. A new panel starts with an empty group named "Everyone". |
| User | A person with access: a group, the apps they use (the subscription link, AmneziaVPN or both), the nodes they get (all or some), a traffic quota, a term and a device limit. Status: Active, Disabled, Expired or Over quota. |
| Device | A device of a user that holds credentials. For subscription apps a device appears by itself when an app fetches the link. For AmneziaWG a device is one key bound to one profile. The device limit counts devices. |
| Subscription link | One secret URL per user. An app that fetches it gets the user's servers; a browser gets the user page. A new link replaces the old one at once. |
| User page | What the link shows in a browser: instructions for the person's platform, a QR code, traffic and term, and, if you allow it, AmneziaWG keys the person makes for their own devices. It can ask for a page password. |

How they relate:

- A **user** gets a **server on a node** when the profile is in the user's **group**, the node is one of the user's nodes, the server is enabled, the user is Active, and one of the user's apps can use the protocol (Hysteria2 for the subscription apps, AmneziaWG for AmneziaVPN).
- Every change to any of these is pushed to the affected nodes at once. Nobody has to restart anything.

## How a person's app reaches a node

```text
  person's app                    panel (mistgate)                     node de1 (mistgate-node)
  ------------                    ----------------                     ------------------------
  1. fetch the link
     https://panel.example.com/<secret>/<token>
     ------------------------------------>  public listener, TCP 443
     <------------------------------------  list of servers (one per profile on a node)

  2. connect to a server, for example de1.example.com, UDP 443 (Hysteria2)
     ---------------------------------------------------------------->  engine  ---->  internet
                                                                                 (direct or WARP)

  3. management, always started by the node:
                                    <---------------------------------  agent dials TCP 443,
                                                                        secret TLS name, mutual TLS
                                    desired state  ------------------>
                                    <------------------  traffic, sessions, health, events
```

The app talks to the panel only to fetch the link. VPN traffic goes straight from the app to the node and out to the internet; it never passes through the panel.

## Next

- [Requirements](requirements.md): what the panel and the nodes need.
- [Install the panel](install-panel.md), then [Add a node](add-node.md) and [First users](first-users.md).
- [Architecture](../reference/architecture.md): how the parts talk to each other, in more detail.
