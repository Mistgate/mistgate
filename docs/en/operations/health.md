---
title: Health
description: How the panel checks every profile as a real client, what the node doctor looks at and can fix, and how alerts open, close and stay quiet.
---

Mistgate watches the fleet from two sides. The panel connects to every profile on every node the way a client app does (client-eye checks), and the agent on every node checks its own host (the node doctor). Alerts are derived from both and from the state of the nodes. Everything is on the **Health** page, in the tabs **Alerts**, **Checks** and **Doctor**; the node page has its own **Doctor** tab and shows the checks of that node on its **Overview** tab.

## Client-eye checks

### How a check works

For every profile on a node the panel opens a tunnel with a built-in client and, through that tunnel, requests two pages:

| Request | What it proves |
|---|---|
| `https://www.gstatic.com/generate_204` | The tunnel carries traffic. Must answer 204. The latency shown is the time from the start of the connection to the first byte of this answer. |
| `https://www.cloudflare.com/cdn-cgi/trace` | Where the traffic leaves: the exit IP and its country. |

The host names are resolved by the node, not by the panel, so a broken resolver on the node shows up as a failed check.

A result is one of:

- **OK**: both pages answered.
- **Degraded**: the tunnel works but one of the two pages did not answer. The latency is shown in the warning colour. A degraded result never opens an alert by itself.
- **Failed**: the tunnel did not come up, or neither page answered.

The client depends on the protocol:

- **Hysteria2.** The official Hysteria2 client, running inside the panel. It dials the node's address and the profile's own port (not the port-hopping range, so a blocked hop range is not seen). It uses the profile's obfuscation (Salamander or Gecko) and the certificate rules of a real client: a self-signed certificate is pinned to the fingerprint the node reported, any other is checked against the profile's **Domain (SNI)**.
- **AmneziaWG.** amneziawg-go inside the panel process on a user-space network stack: no network interface, no root, nothing added to the panel server's routes. It uses the same configuration a real device gets. Every round is a fresh device, so every round also tests the handshake. A node does not answer an unknown peer or a packet with the wrong obfuscation, so a missing handshake and a blocked UDP port look the same: both are reported as a timeout.

A protocol without a test client is shown as **not checked**.

The check uses a hidden system account on each profile of each node: it belongs to no user and no device, and it never appears in user lists, subscriptions, quotas or the MCP tools. On a Hysteria2 profile the node limits it to 2 Mbit/s. On an AmneziaWG profile it takes one address of the profile's client network.

> **Note:** The checks run from the panel's server. A green check proves that a client on the panel's network gets through the node. It cannot see blocking that happens only on your users' networks.

### Schedule

| What | Value |
|---|---|
| Interval for each profile on a node | 5 minutes. Each profile has a fixed offset inside the interval plus up to 10 s of jitter, so the rounds are spread out. There is no setting for it in the admin. |
| A round | One attempt and, if it failed, one retry 15 s later. The outcome of the round is the last attempt. |
| Time limits | 8 s for the handshake, 10 s for each page, 25 s for the whole attempt. |
| While a profile fails | A round every 60 s, for a fast confirmation and a fast recovery. |
| Rounds at the same time | 4. |
| A new profile | Checked for the first time within 30 s. |
| **Check now** | Starts a round at once for the whole fleet (Health → Checks) or for one node (**Check this node now**). One round per profile per 30 s; a second click earlier says "Checked a moment ago". |

### Reading the matrix

**Health → Checks** is a table of nodes (rows) by profiles (columns). A cell shows the latency in milliseconds or a state:

| Cell | Meaning |
|---|---|
| `123 ms` | The last round passed. |
| ✕ fails | The profile fails the check. |
| ✕ not started | The node could not start the profile. |
| off | You switched the profile off on this node. |
| no link | The node is offline. |
| not checked | The panel has no test client for this protocol. |
| starting | The profile is starting. |
| — | The profile is not deployed on this node. |

Only "fails" counts as a failure. The other states are skipped rounds: they are not stored and do not touch the failure streak.

Click a cell to see the last 24 hours in half-hour bars (red where a round failed), the exit IP and **Failed in a row**. An alert opens after two failed rounds in a row.

When a round fails, the cell says why:

| Code | What the admin says |
|---|---|
| `timeout` | No handshake: the UDP traffic probably does not arrive |
| `auth` | The node refused the panel's check account |
| `tls` | Certificate or domain does not match |
| `refused` | Connection refused: nothing listens on the port |
| `exit_unreachable` | The tunnel works, but the node's exit to the internet does not |
| `http_status` | The test sites answer with an error |

### What green means

A green cell means that, within the last few minutes, a client from the panel's network completed the handshake with this profile on this node and reached two well-known sites through the node's exit. It does not prove that the port-hopping range works, that your users' networks let the traffic through, or that every app handles the profile.

## Node doctor

Every node agent runs 16 checks of its own host. They only read: files under `/proc` and `/sys`, a few configuration files and read-only commands (`systemctl list-*`, `nft list`, `timedatectl show`, `dmesg`, `journalctl -k`). Changing anything is a separate, confirmed step (see "Fixes" below).

### When it runs

- About 30 seconds after the agent connects, then every 10 minutes (with up to a minute of jitter).
- **Run again** on Health → Doctor or on the node's **Doctor** tab asks the node now and waits up to 30 seconds for the report.
- Every check has 10 seconds, the whole run 30 seconds. A check that hangs, or that cannot run on this host (a container, no systemd, not root), is **Skipped** with the reason. It is never reported as OK.
- A report older than 25 minutes is stale: the admin greys it out and says how old it is. An offline node shows its last report, and fixes are not available until it is back.
- An agent that predates the doctor is shown as "too old for the doctor". Update it on the [Updates](updates.md) page.

Each item has one of four states: **OK**, **Attention** (a warning), **Problem** (a failure) and **Skipped**.

### The checks

| Check | What it looks at | Attention | Problem | Fix |
|---|---|---|---|---|
| Disk space (`disk_space`) | Used space and inodes on `/` and on the volume of the agent's state directory, counted like `df`. | 80% used or less than 1 GB free | 92% used, less than 300 MB free, or 95% of inodes used | Trim the journal, when the journal is at least 300 MB |
| System journal size (`journald_size`) | Size of the systemd journal on disk. The baseline caps it at 200 MB. | Over 300 MB (the cap does not work) | Over 1 GB | Trim the journal |
| Hung processes (`dstate_tasks`) | Processes stuck in uninterruptible sleep in three samples 2 s apart, QXL/TTM errors in the kernel log, and a load equal to the CPU count while the CPU idles for 10 minutes. | A stuck process | Stuck at two runs in a row, a QXL/TTM hang, or the idle-load pattern | None: reboot the server from the hoster's panel |
| Clock (`time_sync`) | Offset against the panel's clock and whether NTP is synchronised. | More than 2 s off, or NTP not synchronised | More than 30 s off | None: turn NTP on (`timedatectl set-ntp true`) |
| Server resolver (`resolver`) | Whether the host's own resolver answers for `www.cloudflare.com`, `www.gstatic.com` and `www.google.com`, plus `gosuslugi.ru` on nodes whose country is Russia. 3 s per name, one retry. | One name fails, or the median answer takes over 500 ms | Two or more names fail, or `gosuslugi.ru` fails on a node in Russia | Fix the resolver, only when the recommended resolvers answered in the same run |
| IPv6 (`ipv6`) | A global IPv6 address, and an outbound IPv6 connection to two public resolvers on port 443. | An IPv6 address that cannot connect out (clients that get AAAA answers stall) | None | None |
| Leftovers of other VPNs (`foreign_vpn`) | systemd units of x-ui, Xray, remnanode, hysteria-server and `wg-quick@`, Docker containers of VPN images, `wg*` and `awg*` interfaces that are not Mistgate's, running `xray`, `x-ui` and `sing-box` processes. | Any of them found | One of them holds a port of a profile | None: removing them is your decision |
| Foreign firewall rules (`foreign_nft`) | nftables tables that are not Mistgate's. Legacy iptables rules are not visible to this check. | A foreign table has a nat hook | A foreign rule redirects, rewrites or drops a port or hop range of a profile | None |
| Ports in use (`port_conflicts`) | Listening sockets on the profiles' ports, their hop ranges and the TCP port of the HTTPS decoy and ACME of a Hysteria2 profile. | A foreign listener inside a hop range, a foreign process on the TCP port, or a profile that failed to bind while the port is free now | A foreign process holds a profile's port | Restart profile, only for the profile that failed to bind |
| Base network settings (`net_baseline`) | `fq` as the default qdisc, BBR congestion control, Mistgate's sysctl file and its journald drop-in. | Something differs | None | Restore the base settings |
| Certificates (`cert_expiry`) | The certificate each TLS profile actually serves, whether it matches the domain, and the agent's own certificate for the panel connection. | A profile certificate has less than 14 days; the agent certificate less than 5 days | Less than 3 days, expired or not matching the domain; the agent certificate less than 1 day | Restart profile, for a self-signed certificate only |
| Memory (`memory_pressure`) | Available memory, swap, memory pressure (PSI) and OOM kills in the kernel log of the last 24 hours. | Less than 12% available, swap over 50% under pressure, or an OOM kill | Less than 5% available, heavy pressure, or the OOM killer hit the agent or a VPN engine | None |
| CPU softirq (`cpu_softirq`) | Mean softirq share and total CPU over 10 minutes. Skipped until 30 samples are collected. | Softirq 50% or more | Softirq 90% or more, or CPU 97% or more | None |
| Kernel headers (`kernel_headers`) | Headers, `dkms`, `make` and `gcc` for the AmneziaWG kernel module. Skipped without an AmneziaWG profile; only a fact while AmneziaWG runs in userspace. | Kernel mode is requested and the module does not run while tools are missing | None | None: see [AmneziaWG](../guide/amneziawg.md) |
| AmneziaWG backend (`awg_backend`) | Which backend runs AmneziaWG: the kernel module or userspace. Skipped without an AmneziaWG profile. | The host firewall drops forwarded traffic (often Docker) | No working backend: no `/dev/net/tun`, an outdated service file, or no module in kernel mode | None |
| WARP exit (`warp_path`) | The node's WARP tunnel and its routes. Skipped when the node has no WARP account and no profile exits through WARP. | WARP is paused while profiles exit through it | Profiles exit through WARP but the node has no account, the host clashes with WARP's routing table, rule priority or interface name, no WireGuard backend, or WARP is down | Reconnect WARP, only while the tunnel is down |

A node without IPv6 that runs WARP is fine: WARP uses its IPv4 endpoint, and the admin shows the IPv6 item as OK.

A warning or problem opens an alert (see "Kinds of alerts" below). The certificate check is the exception: it opens a "Certificate is about to expire" alert instead of a doctor alert.

### Fixes

There are exactly five fixes. They are compiled into the agent: the panel sends only the fix id, and the agent checks the parameters (a profile must be one the node runs). None of them installs packages or deletes data.

| Button | What it does | Offered by |
|---|---|---|
| Trim the journal | `journalctl --vacuum-size=200M --vacuum-time=7d` | System journal size, Disk space |
| Restore the base settings | Writes Mistgate's sysctl file (`/etc/sysctl.d/90-mistgate.conf`: fq and BBR) and journald drop-in (`/etc/systemd/journald.conf.d/90-mistgate.conf`: the 200 MB cap) again, and the SSH guard (a per-address rate limit on new SSH connections). | Base network settings |
| Restart profile | Restarts one profile on the node, or every profile that failed to start. Connections through it drop for a couple of seconds and come back by themselves. | Ports in use, Certificates (self-signed) |
| Fix the resolver | Points the host resolver at the node's **DNS for user traffic** (node settings) or, when that is empty, at the default for the node's country: Yandex DNS (`77.88.8.8`, `77.88.8.1`) on nodes in Russia, `1.1.1.1` and `8.8.8.8` elsewhere. With systemd-resolved it writes a drop-in (`/etc/systemd/resolved.conf.d/90-mistgate.conf`); otherwise it rewrites `/etc/resolv.conf` and keeps the original as `/etc/resolv.conf.mistgate.bak`. | Server resolver |
| Reconnect WARP | Rebuilds the configured tunnel and its routes while keeping the same account. Profiles using WARP may briefly lose traffic. The plan is refused if the tunnel recovered before Apply. | WARP exit, only when the tunnel is down |

A fix always takes two steps:

1. Click the fix button on the doctor item or on its alert. The panel asks the node what it would do ("Asking the node what it would do…"); the node answers with a dry run that changes nothing.
2. The dialog shows the plan: for example, the journal size before and after, or the profiles to restart and how many connections drop. **Apply** performs exactly that plan.

The plan is valid for 10 minutes, works once and is bound to the node, the fix and its parameters. One fix runs at a time on a node. Both steps go to the audit log. After a successful fix the node runs the affected checks again, and alerts that the fix cleared close as "fixed". Applying a fix needs the owner.

When a fix fails, the dialog says why:

| Error | Meaning |
|---|---|
| `unknown_fix` | This node's agent does not know that fix |
| `bad_params` | The node refused the parameters |
| `not_applicable` | The problem is already gone or the host cannot do it |
| `unsupported_host` | This kind of host does not support it |
| `busy` | The node is busy with another fix |

For checks without a fix, the item opens a **Manual** block with the commands to run on the node: turning on time sync, finding what holds a port, reading the firewall rules, or a note to reboot from the hoster's panel.

### "This is normal for this node"

Some warnings are facts you accept: Docker's own nftables tables, a host without working IPv6. **This is normal for this node** on a doctor item or its alert stops it from raising an alert and from counting in the badges. The item moves to **Accepted as normal** and shows who accepted it and when.

- Only a warning (Attention) can be accepted. A problem cannot, and a certificate warning never can.
- The acceptance is for the exact fact the node reported (its detail code). It ends by itself when the node reports a different fact for that check or the check turns into a problem. An agent too old to send detail codes cannot have its warnings accepted.
- **Count it again** takes the acceptance back.
- The alert of an accepted warning closes as "accepted as normal".

## Alerts

### How alerts open and close

Alerts are a function of the current state. Every 10 seconds, and after every check round and doctor report, the panel works out which conditions hold now: a condition without an alert opens one, an alert whose condition is gone is resolved. There is one active alert per kind, node and subject (a profile, a doctor check). Because nothing depends on remembered events, alerts survive a panel restart.

When the panel cannot judge a condition, the alert stays as it is instead of closing: an offline node keeps its other alerts (only "Node is unreachable" is decided for it), a stale doctor report keeps the doctor alerts, and a check result from before the node went away keeps the check alerts.

An alert that fires again within an hour of closing reopens with its original id and start time. During the first 90 seconds after the panel starts, no "Node is unreachable" opens: the agents are still reconnecting.

### Kinds of alerts

| Title in the admin | Kind | Severity | Opens when |
|---|---|---|---|
| Node is unreachable | `node_down` | Critical | The agent has had no connection for 10 minutes. A shorter outage is an event, not an alert. |
| Host blip | `host_blip` | Info | History only: written when the link came back within 10 minutes without a reboot. It is never active. |
| Alive, but no traffic | `no_traffic` | Critical | The agent is connected and the profiles run, but every checked profile of the node failed two rounds in a row. The node's status turns to "no traffic". |
| "<profile>" fails the client-eye check | `check_failed` | Warning; Critical when it is the node's only profile of that protocol | Some, not all, profiles of an online node failed two rounds in a row. |
| The panel cannot reach many nodes | `check_failed` (fleet-wide) | Warning | The last round failed for at least 80% of the checked profiles, on nodes of at least three different **Provider** values. The panel's own connection is the suspect: per-node check alerts are paused until a round passes. |
| The doctor item's title | `doctor_warn` / `doctor_fail` | Warning / Critical | A doctor check reports Attention / Problem. A kernel headers warning on a node without an AmneziaWG profile is Info. |
| The node's state differs from the panel's | `state_drift` | Warning | The configuration the node applied differs from the panel's even after an automatic full resend. |
| Certificate is about to expire | `cert_expiry` | Warning; Critical under 3 days, expired or not matching the domain | From the doctor's certificate check. For an agent without the doctor, from the panel's own view of the profile certificates (under 14 days). |
| The node update stopped | `update_failed` | Warning | A rollout paused because a node rejected the update, failed the check after updating, or the panel lost track of a step after a restart. Closes when the rollout is resumed, cancelled or replaced. See [Updates](updates.md). |

The reason under a traffic alert names the likely cause:

| Diagnosis | When |
|---|---|
| UDP port blocked | A profile times out while another profile of the node answers on another port, or the whole node times out on one port: the hoster seems to cut that UDP port. |
| All UDP blocked | Every profile times out, on port 443 or on two and more ports: the hoster seems to cut incoming UDP. |
| WARP exit dead | A profile with WARP egress fails while a direct profile of the same protocol on the node works. |
| auth, tls, refused, exit unreachable, test sites answer errors | The error code of the check, explained in plain words. |
| Mixed | Every profile fails, each in its own way. |

Two more kinds exist in the API, but the panel does not raise them as alerts: a user who reached the quota (the user's status shows it) and a subscription that looks shared (written as an event, "a subscription link is used from N networks a day").

### Severity and badges

Alerts are **Critical**, **Warning** or **Info**. The badge in the header and on the Overview counts active alerts that are not muted and not Info. The active list shows Critical first, then the newest.

### How an alert ended

The **History · 7 days** list says how each alert closed:

| In the admin | Resolution | When |
|---|---|---|
| cleared by itself | `cleared` | The condition went away. |
| fixed | `fix_applied` | It closed within 5 minutes after a doctor fix that the alert offered. |
| node retired | `node_retired` | The node was retired. |
| recovered by itself | `node_returned` | The unreachable node came back. |
| replaced by a bigger alert | `superseded` | A bigger alert says the same: "Alive, but no traffic" replaces the per-profile ones, a doctor problem replaces the warning of the same check, the doctor's WARP problem replaces the failing WARP profile, the fleet-wide alert replaces the per-node check alerts. |
| accepted as normal | `accepted` | The owner accepted the doctor warning as normal for the node. |

Closed alerts are kept for 90 days. The API returns up to 30 days of history, at most 200 alerts.

### Buttons on an alert

Depending on what the alert says, its card offers:

- the doctor's fix (only while the doctor still offers it and the node is connected);
- **Restart profile** or **Restart the profiles**, with the number of connections that drop;
- **Open the node's profiles**, where a port or a domain is changed;
- the node's WARP card;
- **Open node**;
- **This is normal for this node**, for a doctor warning;
- **Mute**.

### Muting

**Mute** offers: For 1 hour, Until morning (08:00), For a day, For 7 days. The longest mute is 7 days; **Unmute** ends it.

- A muted alert stays on the list with "Muted until …". It is still evaluated and still closes by itself.
- It is not counted in the badges.
- If its severity rises, the mute ends and the alert counts again.
- Muting is written to the audit log.

> **Note:** The panel sends no notifications yet. A Telegram bot for the fleet is planned. Today alerts are seen in the admin (the header badge, the Overview, the Health page) and through the [API](../reference/api.md) and [MCP](../reference/mcp.md).

### Events on the node page

The node's **Events** tab and the Overview's event feed keep a timeline that answers "what happened last night":

| Event | Written when |
|---|---|
| stopped answering (since …) | The node has been silent for 10 minutes. |
| the link dropped for … and came back by itself | The node came back after 1 to 10 minutes of silence. |
| the server rebooted (… without a link) | It came back with a different boot time. |
| back online after … | It came back after "stopped answering". |
| traffic stopped: X of Y profiles fail the check | "Alive, but no traffic" opened. |
| traffic flows again (…) | It closed as cleared or fixed, with how long it lasted. |
| "<profile>" fails the client-eye check | A per-profile check alert opened. |
| "<profile>" passes the check again (…) | It closed as cleared or fixed. |
| updated to … / update failed / update rolled back | A rollout step ended. |

Events of severity info are kept for 90 days, warnings and errors for 400 days. Raw check rounds are kept for 25 hours after the day is summarised; the daily summaries for 90 days.

## Who can do what

| Action | Owner | Helper | Read-only |
|---|---|---|---|
| See alerts, checks and doctor reports | yes | yes | yes |
| Mute an alert, Check now, Run again | yes | yes | no |
| This is normal for this node, Count it again | yes | yes | no |
| Apply a doctor fix | yes | no | no |

An API token with the operator profile can mute alerts, start checks and run the doctor. A doctor fix through a token is possible only via MCP, as a plan the owner approves (see [MCP](../reference/mcp.md)).
