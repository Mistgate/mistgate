---
title: Nodes
description: What the Nodes list and a node's page show, what each status means, and how to change, restart or retire a node.
---

A node is a server that runs the Mistgate agent, `mistgate-node`. The panel tells the agent which profiles to run, and the agent reports back its state, its traffic and what it sees on the host. This page walks through the **Nodes** section and a node's page tab by tab. To connect a new server, see [Add a node](../getting-started/add-node.md).

## The Nodes list

The header says how many nodes there are, how many people are online and how many nodes have problems ("3 nodes · 12 online · 1 with problems"). **Add node** opens the dialog that installs a node over SSH or makes a manual install command: see [Add a node](../getting-started/add-node.md).

Above the table:

- the status chips **All**, **Problems** and **Healthy**, each with its count;
- **All protocols**: shows only the nodes that run a profile of one protocol;
- **All countries**: shows only the nodes of one country.

| Column | What it shows |
|:--|:--|
| **Node** | The status dot, the name, the location (or country) and the provider. Under it, the reason of the status, or the status word. |
| **Address** | The domain or IP clients connect to. |
| **Protocols** | The protocols of the profiles on the node. |
| **Online** | People connected now. |
| **Today** | Traffic since 00:00 UTC. |
| **CPU** | The latest CPU reading. |
| **Uptime** | Host uptime as the agent reports it. |

On a phone the table becomes one card per node with the same facts. The list refreshes every 10 seconds. Retired nodes are not listed.

A healthy node whose agent is older than the version this panel ships reads **Healthy · outdated agent** with its version; the line is a link to **Updates**. See [Updates](../operations/updates.md).

## Node statuses

| Status | What it means | What to do |
|:--|:--|:--|
| **Waiting for install** | The install command was made, the agent has not connected yet. The line says how long the command still works, or that it has expired. | Run the command on the server, or make a new one (**New install command** on the node's page). |
| **Healthy** | The agent is connected and nothing is wrong. | Nothing. |
| **Needs attention** | The agent is connected, but: there is no enabled profile on the node (users do not get it), the node's state differs from the panel's, or the server clock differs from the panel's. | Read the line under the name. Add a profile, restart the profiles, or fix the clock. |
| **Partly working** | Some profiles failed to start, others run. | Open **Profiles** on the node and read the error under each failed profile. |
| **Broken** | Every enabled profile failed to start, or a host check of the doctor failed. | Open **Profiles** or **Doctor**. |
| **Unreachable** | No link to the agent, and the host did not come back within 10 minutes. | Check the server at the hoster, then restart the agent over SSH (the banner gives the command). |
| **Host blip** | The link dropped less than 10 minutes ago, or came back with the same boot time. It is not counted as a problem. | Usually nothing: short drops are the hoster. |
| **No traffic** | The agent is connected, but the client-eye checks fail: clients do not get through. | The banner says why when it knows (a blocked UDP port, a certificate, the exit). Restart the profiles or change the port. |
| **Updating** | The agent is being updated. | Wait. If the check after the update fails, it rolls back by itself. |
| **Retired** | The node was retired from the fleet. | Nothing. |

Problems are the red statuses and **Needs attention**, except the clock warning: that one is shown but not counted. These counts appear in the header of the list, on the Overview and in the menu.

## A node's page

The header shows the country code, the name, the status, and a WARP chip when the node has a WARP account (the chip reads the Cloudflare data centre while WARP works and links to the WARP card). Under the name: the location, the provider, the address, the agent version, and **outdated agent** when there is a newer one.

Two buttons sit on the right:

- **Restart profiles** restarts every profile of the node. The dialog says how many people drop for a few seconds; they reconnect by themselves. The button is off while the agent is not connected or the node has no profiles.
- **Doctor** opens the **Doctor** tab.

Under the header a banner explains the current state with the action that helps:

| State | Banner |
|:--|:--|
| Unreachable | "The node has not answered for …", the time it was last heard from, and a command to copy. |
| No traffic | "Alive, but no traffic" with the reason from the health checks, **Restart profiles** and **Check now**. |
| Host blip | "The host blipped" with **Got it**. |
| Updating | "The agent is updating". |
| Waiting for install | "Waiting for the agent" with **New install command**. |
| Retired | "This node is retired". |
| No profiles | "No profiles on … yet — users will not get it" with **Add profile**. |
| A profile failed | The profile name, the cause in words, and **Open profiles**. |

The command of the "Unreachable" banner, to run on the node over SSH:

```sh
systemctl restart mistgate-node && journalctl -u mistgate-node -n 50 --no-pager
```

The page has seven tabs: **Overview**, **Profiles**, **Users**, **Logs**, **Events**, **Doctor** and **Settings**. The **Profiles** tab shows how many profiles failed to start, the **Doctor** tab how many items need attention.

### Overview

- **CPU**, **RAM**, **DISK**, **NETWORK**: the latest readings. The CPU bar shows the softirq part (network interrupts) in a second colour, with the vCPU count and the load. Readings arrive a few seconds after the agent connects; an offline node keeps its last readings and says since when it is offline.
- **Traffic · 24 h**: hourly traffic through the node.
- **Alerts**: the open alerts of this node, with the same buttons as on the Health page, and the last three that closed. **All alerts** opens the Health page. The card is hidden when there was nothing.
- **Client-eye checks**: one row per profile with its latency and exit IP, or why it fails. A click on a row opens its 24-hour history. **Check now** starts a round for this node.
- **Online by protocol**: people connected now, per protocol.
- **Host facts**: hostname, OS, kernel, architecture, CPUs, RAM, disk, virtualization, IPv6, boot time and the engine versions.

The checks, alerts and the doctor are described in [Health](../operations/health.md).

### Profiles

Every profile on the node is a row. The name links to the profile; under it is the protocol.

For Hysteria2 the row shows:

- **Port**: the UDP port on this node;
- **Domain (SNI)**: the server name in the certificate;
- **Certificate**: the expiry date (amber with less than 14 days left, red with less than 3), **self-signed, pinned**, or **none: the profile is not running**.

For AmneziaWG the row shows **Port**, **Backend** (kernel or userspace, with its version), **Egress** (**Direct** or **WARP**) and **Devices** ("5 · 2 online"), and a status line with the last handshake.

On the right is the state: **Running**, **Starting**, **Failed to start** or **Off**. A profile that failed shows the cause in words and the agent's own error text under it. When the cause is clear, a button fixes it: **Change port** (the dialog opens on a free port), **Set the domain**, or **Open WARP**.

Each row has:

- **Edit**: the port and the **Domain (SNI)** for this node only, and the **Enabled** switch. A profile switched off stays on the node but leaves the subscriptions.
- **Restart**: restarts only this profile. Its connections drop for a couple of seconds.
- **Remove**: takes the profile off the node. The dialog says who notices: the users who lose the node at their next subscription update, or, for AmneziaWG, the devices that stop working. Their keys work again if you put the profile back on the same node.

**Add profile** opens the dialog that puts a profile on this node: see [Profiles](profiles.md). When the panel has no profile at all, the dialog offers to create a Hysteria2 profile on port 443 (or the first free port) and put it on the node in one go, with **Set up by hand** as the other way.

### Users

- **On this node now**: who is connected, with the device model, the protocol and the current download speed.
- **Top today**: the ten people with the most traffic through this node since 00:00 UTC.

### Logs

A live tail of the agent and its engines, relayed by the panel. The last 200 lines arrive first, then new lines as they are written; up to 1000 lines stay on screen.

- **All**, **Info**, **Warn**, **Errors** filter by level; **Search the log** filters by text.
- **Pause** freezes the screen while new lines keep arriving; **Resume** shows them.
- **Download** saves what was received as `<node>-agent.log`.

The tab works while the agent is connected (**Healthy** or **No traffic**). If the link drops, it reconnects every 5 seconds. Only the owner can read the logs: they contain client addresses.

### Events

The history of the node, grouped by day: connected, went quiet, came back, profiles added, started, failed and restarted, settings applied, agent updates and rollbacks, certificate renewals, the AmneziaWG module build, and recognized BitTorrent attempts. Related events are joined into one line. A torrent event includes the inbound and the protocol, never an address (neither the client's nor the destination); the panel adds a user's name only when the node can identify that user reliably.

The chips **All**, **Problems**, **Profiles** and **Agent** filter the list. **Details** opens the raw events behind a line: the event code, the exact time, the source (**node agent**, **panel** or **admin action**) and the parameters. **Show more** loads older events; when a filter finds nothing among the latest ones, **Search further** does the same.

### Doctor

The node's own host checks, grouped into **Needs attention**, **Accepted as normal**, **All good** and **Not checked**, with the fixes the panel can apply and the manual steps for the rest. **Run again** asks the node for a fresh report. The first report comes about 30 seconds after the agent connects; an agent that is too old for the doctor gets a link to **Updates**. See [Health](../operations/health.md).

### Settings

The tab holds, top to bottom, the WARP card, the node's fields, the **SSH access** card (a node installed over SSH), the **AmneziaWG backend** card, **New install command** (the owner only) and the **Danger zone**. WARP is described in [WARP](warp.md), the backend in [AmneziaWG](amneziawg.md). **New install command** is for a lost certificate or a reinstalled server: see [Add a node](../getting-started/add-node.md). The **SSH access** card shows the saved SSH server and login, and **Reveal password** shows the password after a fresh sign-in confirmation; how the password is kept and changed is in [Install a node over SSH](../getting-started/ssh-install.md).

| Field | What it does |
|:--|:--|
| **Name** | a–z, 0–9 and dash, 2–24 characters, unique. |
| **Address** | The domain or IP clients connect to, without `https://` or a port. Changing it changes the address in every subscription. |
| **Country** | Two-letter country, or **Not set**. Server names in the apps use it ({flag} {country}), and on nodes in Russia the doctor checks gosuslugi.ru and offers Yandex DNS. |
| **Location** | Free text next to the name ("Frankfurt"). Empty shows the country name. |
| **Provider** | The hoster, for you. |
| **Network capacity** | Mbps, optional: the provider's symmetric limit. The node's load is its CPU use, or the busier direction of its link as a share of this capacity when that is higher; without it the load is the CPU alone (on a one-core VPS the CPU usually runs out first). The server names in Happ end with the load (" · 64％"), and the user page shows a level: low, medium or high (from 50% and 80%). Never the rates themselves, and nothing in other apps, whose names must stay stable. 0 means unknown. **Measure** can fill it in: see below. |
| **Notes** | Free text, up to 500 characters. |
| **Drop the agent connection after** | 30–600 s, default 90. If the agent stays silent longer, the panel closes the connection. The node turns grey (**Host blip**) first; **Unreachable** comes after 10 minutes without contact. |
| **Wait for a command result** | 10–900 s, default 120: how long the panel waits before a command counts as unanswered. |
| **Agent dial timeout** | 5–120 s, default 15: how long the agent waits when it connects to the panel. |
| **DNS resolvers for this node** | **Server's own resolver** (the default), **Yandex DNS** or **Cloudflare + Google**; a list of your own (up to 8 IP addresses, `ip:port` works too) shows as **Custom DNS** and is edited there. The node and its VPN engines resolve names with them, the names in its users' traffic included. With **Server's own resolver** the node uses whatever the server uses. For a node in Russia pick **Yandex DNS**, so Russian services such as gosuslugi.ru resolve reliably. |

**Save** sends only what changed. Most settings take effect without a reconnect; changing the torrent setting restarts the node's Hysteria2 profiles.

#### Measuring the network capacity

Providers often do not state the real channel, and a virtual server's network card does not report the plan's limit, so the node can measure how fast it reaches the internet. Next to **Network capacity** the owner presses **Measure**. One measurement is **three runs** back to back, a second apart, and the best run counts, per direction. In a run the agent on the node downloads for about 5 seconds over 6 parallel connections from a public speed server that needs no account (`speed.cloudflare.com`; if the node cannot reach it, a file on `proof.ovh.net`, then on `cachefly.net`), leaves out the first second (TCP is still speeding up), and then uploads for about 2 seconds over 4 connections to Cloudflare. The whole measurement takes about 30 seconds, moves at most 1.5 GB (per run up to 400 MB down and 100 MB up), and ends within 45 seconds whatever the servers do. Only one measurement runs on a node at a time.

The panel shows "Measured: 940 Mbps ↓ · 870 ↑" and, next to it, **Use 870**. It puts the **slower** direction, rounded (to 5 below 100, to 10 below 1000, to 50 above), into the field: a VPN node relays every byte both ways, and providers often leave the inbound free while they cap the outbound (a node can measure 5000 down and 1000 up on a 1 Gbit/s plan); without an upload figure, the download. The first measurement of a new node stores the same value; nothing is saved until you press **Save**, and the measurement never overwrites a value you typed by itself. Uploads are measured only against Cloudflare: if the node fell back to another server, the upload is not measured.

**The people already on the node count.** During the counted part of every run the agent also reads the byte counters of the node's main network interface (the same ones the node's load figures come from). The estimate of a direction in a run is the larger of what the test alone moved and what the whole interface carried in that window, so what the people using the node carry during the test is part of the capacity, not lost from it. When the interface carried more than the test did (allowing for the test's own protocol overhead), the panel adds "Including people's traffic: 35 Mbps ↓". That part is already inside the figures, not on top of them. If the counters cannot be read, only the test counts.

Take the number as an estimate, not as the provider's limit:

- It is three short tests. It depends on the route from the node to that test server, which can be slower or faster than the route to your users.
- It measures what the node can use **now**, and the users feel the test: their speed may dip for those 30 seconds.
- A very fast link (above a few Gbit/s) hits the 400 MB limit of a run's download within a second or two and the result is a lower bound.
- The result says which server answered. If your provider states the limit, keep their figure.

Only the owner can measure (the API refuses tokens and MCP), and each measurement is written to the audit log (`node.bandwidth_measure`). It needs a node with the agent from this release or newer: for an older agent the panel says "agent too old" and sends nothing, and the node must be connected. The node needs outbound HTTPS to the test servers.

A **new node** is measured once automatically: about 45 seconds after its agent connects for the first time after the installation, if **Network capacity** is still 0. That is the only value the panel writes by itself, and only when the field is 0; the result is in the node's events ("Network capacity measured at the first start") and in the audit log (`node.bandwidth_auto`). A node whose agent is too old, or whose test fails, is skipped without a message: press **Measure** yourself or type the number.

**Block recognized BitTorrent traffic** is off by default. It blocks plaintext BitTorrent that the node's users start, on AmneziaWG in the kernel and on Hysteria2 in its engine, and writes a `torrent_attempt` event without any address. It needs an agent that supports it. See [Torrent protection](torrent-protection.md).

**DNS resolvers for this node** are not the DNS presets that apps receive, and the doctor checks the server's own resolver separately: see [DNS](dns.md).

**DNS choice on the user page** is a separate block of the node, not to be confused with the resolvers above. Here the owner ticks the DNS presets that people may pick for this server on their page (the order is the order on the page) and marks one as the default for the node. Only the owner changes it, and the change is written to the audit log (`node_dns_options`). A node with no presets offered gives no choice. It matters only when **DNS choice on the page** is on in **Subscriptions** → **User page**, and it reaches AmneziaWG keys and the AmneziaWG proxies of a Mihomo profile; see [DNS](dns.md).

> **Note:** AmneziaVPN keys hold the node address they were issued with. After you change **Address**, the devices of the node's AmneziaWG profiles are marked outdated, and the people who connect with keys must import their key again (from their page or from the user card). Subscription apps get the new address at their next update.

#### Renaming a node

Change **Name** and save. The name shows in the panel, in the file names of AmneziaWG configs, and in app server names only when the template uses `{node}`. The default uses the country and profile name. See [Subscriptions](subscriptions.md).

### Danger zone: retiring a node

**Retire from fleet** removes a node for good. The dialog lists what happens:

- users stop getting the node in their subscription; their links stay the same;
- the node's certificate is revoked and its agent is told to stop; the files on the server stay;
- the node's record stays in the audit log, with its traffic history.

To confirm, type the node name. After that the node disappears from the list; its page, if you open it, says "This node is retired". A retired agent cannot connect again. Only the owner can retire a node.

A node installed over SSH keeps its **SSH access** card after retiring: the panel no longer changes that server, but you can still reveal the saved password, and **Forget saved access** (asked twice, with a step-up) deletes it for good. When the panel generated the saved password itself, the retire dialog warns you first, since only the panel knows it.

If the node was offline, its agent never got the order and keeps serving the users it had. The panel then stays on the node's page and says "{name} did not get the order", with the commands that clean the server by hand: the same as in "Remove a node" in [Add a node](../getting-started/add-node.md).

There is no other way to delete a node: retiring is the removal. To clean the server afterwards, as root:

```sh
systemctl disable --now mistgate-node
rm /etc/systemd/system/mistgate-node.service /usr/local/bin/mistgate-node
rm -r /var/lib/mistgate-node
```

The paths are the defaults of `mistgate-node install`; if you installed with other `--bin` or `--state-dir` values, use those.

## Agent version and drift

- **Outdated agent.** The panel knows the newest agent version it ships. A node that runs an older one is marked **outdated agent** in the list and in its header. Some features need a newer agent: the doctor, WARP, the automatic AmneziaWG module build. Update it from [Updates](../operations/updates.md).
- **State drift.** The panel sends every node its full desired state and compares a hash of it with what the node applied. When they differ, the panel sends the state again (event "state drifted from the panel, resent"). If the node still applies a different state, it reads **Needs attention** with "the node's state differs from the panel's", and an alert opens. Restart the profiles; if it comes back, read the node's log.
- **Clock skew.** When the server clock differs from the panel's, the node says so. A wrong clock breaks certificates and handshakes; turn on time sync on the server (`timedatectl set-ntp true`).

## Who can do what

Every admin sees the nodes. The owner and helpers can change a node's settings and restart its profiles. Installing nodes over SSH, making install commands, revealing and forgetting saved SSH access, retiring nodes, reading logs, putting profiles on nodes and building the AmneziaWG kernel module are for the owner only. See [Security](../operations/security.md).
