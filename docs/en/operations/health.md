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
| `https://www.gstatic.com/generate_204` | The tunnel carries traffic. Must answer 204. The panel then asks for it once more over the same connection; the times are explained below. |
| `https://www.cloudflare.com/cdn-cgi/trace` | Where the traffic leaves: the exit IP and its country. |

The host names are resolved by the node, not by the panel, so a broken resolver on the node shows up as a failed check.

A result is one of:

- **OK**: both pages answered.
- **Degraded**: the tunnel works but one of the two pages did not answer. The number is shown in the warning colour. A degraded result never opens an alert by itself.
- **Failed**: the tunnel did not come up, or neither page answered.

The client depends on the protocol:

- **Hysteria2.** The official Hysteria2 client, running inside the panel. It dials the node's address and the profile's own port (not the port-hopping range, so a blocked hop range is not seen). It uses the profile's obfuscation (Salamander or Gecko) and the certificate rules of a real client: a self-signed certificate is pinned to the fingerprint the node reported, any other is checked against the profile's **Domain (SNI)**.
- **AmneziaWG.** amneziawg-go inside the panel process on a user-space network stack: no network interface, no root, nothing added to the panel server's routes. It uses the same configuration a real device gets. Every round is a fresh device, so every round also tests the handshake. A node does not answer an unknown peer or a packet with the wrong obfuscation, so a missing handshake and a blocked UDP port look the same: both are reported as a timeout.
- **VLESS.** Each attempt runs xray-core in a short-lived helper process and uses the same resolved client parameters as the subscription link. REALITY checks server authentication; TLS verifies the certificate chain for the profile's server name. A profile with a CDN override is dialed at the CDN host and port, using that host for SNI and HTTP Host; without a CDN, the check dials the node address and inbound port. TLS verification stays enabled for the CDN certificate, so a self-signed origin certificate behind the CDN is not presented to the panel. The check covers REALITY over TCP Vision or XHTTP, and TLS over XHTTP or WebSocket. A failed CDN path is reported as a `via_cdn` check failure with the CDN host; CDN inbounds do not count toward the node's critical all-checks-failed alert. Fleet keeps the inbound pending until the connected node reports its required capability: `vless/1` or `vless-self/1` for REALITY, and `vless-tls/1` for TLS. No client-eye check runs while the inbound is pending.

A protocol without a test client is shown as **not checked**.

The check uses a hidden system account on each profile of each node: it belongs to no user and no device, and it never appears in user lists, subscriptions, quotas or the MCP tools. VLESS uses an ordinary stable UUID credential for this account. On a Hysteria2 profile the node limits it to 2 Mbit/s. On an AmneziaWG profile it takes one address of the profile's client network.

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

**Health → Checks** is a table of nodes (rows) by profiles (columns). A cell shows the response time in milliseconds or a state:

| Cell | Meaning |
|---|---|
| `123 ms` | The last round passed. The number is the response time, median of the last hour. |
| `2.6 s` with a clock, under the number | Connecting took 2 s or more, median of the last hour. The tunnel works, so the colour stays calm. |
| ✕ fails | The profile fails the check. |
| ✕ not started | The node could not start the profile. |
| off | You switched the profile off on this node. |
| no link | The node is offline. |
| not checked | The panel has no test client for this protocol. |
| starting | The profile is starting. |
| upd. | The node is updating its agent. |
| … | No result yet: the first round comes within a few minutes. |
| — | The profile is not deployed on this node. |

Only "fails" counts as a failure. The other states are skipped rounds: they are not stored and do not touch the failure streak.

Click a cell to see the last 24 hours in half-hour bars as tall as the time from scratch (red where a round failed), the exit IP and **Failed in a row**. An alert opens after two failed rounds in a row.

When a round fails, the cell says why:

| Code | What the admin says |
|---|---|
| `timeout` | No handshake: the UDP traffic probably does not arrive |
| `auth` | The node refused the panel's check account |
| `tls` | Certificate or domain does not match |
| `refused` | Connection refused: nothing listens on the port |
| `exit_unreachable` | The tunnel works, but the node's exit to the internet does not |
| `http_status` | The test sites answer with an error |

### What the numbers mean

All three numbers are measured on the panel's server, not on a person's device.

| Number | What it is |
|---|---|
| Response | The number in the cell. Right after the first answer, the panel asks for `generate_204` once more over the same connection: the time of a request while the tunnel is up, which is what a person feels. The cell shows the median of the last hour; until there is one, the last round, then the time from scratch. |
| Connect | From the start of the round until the tunnel carries its first connection: the Hysteria2 or AmneziaWG handshake, or VLESS with its TCP and REALITY or TLS. Median of the last hour. From 2 s on, the cell gets a second line with a clock. |
| From scratch | From the start of the round to the first byte of the first answer: the handshake, a fresh TLS connection to the test site through the tunnel, and the request. The 24 h bars show this number. Older panels showed it in the cell. |

Hover a cell, or move to it with Tab, to see the response and connect medians of the hour, the last round with its time, response and connect, and the passed and failed rounds of the last 24 hours. On the node page, the rows of **Client-eye checks** show the same.

### When the panel's server loses UDP

Some hosters drop bursts of inbound UDP. A Hysteria2 or AmneziaWG check from the panel's server then waits for a handshake retry: about 2.5 s for QUIC and 5 s for WireGuard. Connect times of several seconds on these profiles, while VLESS on the same nodes connects quickly, point at the panel's network, not at the nodes.

The panel then says so above the matrix, names the nodes and gives the share of slow UDP rounds. It shows the notice when, over the last hour:

- at least 20% of a node's Hysteria2 and AmneziaWG rounds (and at least 3 of them) took 2 s or more to connect;
- under 5% of the VLESS rounds did;
- with no VLESS profile at all, at least two nodes match the first rule.

People are not affected: they connect to the nodes directly. The checks still pass, and no alert opens because of it. If the notice keeps coming back, ask the hoster of the panel's server about inbound UDP filtering or rate limits, or move the panel to another server.

### What green means

A green cell means that, within the last few minutes, a client from the panel's network completed the handshake with this profile on this node and reached two well-known sites through the node's exit. It does not prove that the port-hopping range works, that your users' networks let the traffic through, or that every app handles the profile.

## Country check

The checks above run on the panel's server, which is not inside the country where your users are, so they cannot see a block there. The country check asks probes inside one country (home and mobile networks first) to ping every online node and to open TCP connections to its ports, and compares the answers. It uses [Globalping](https://globalping.io), a free network of probes, and is an **integration** of its own: **off by default**, because switching it on sends the IPv4 address of each online node to Globalping and to nobody else.

- **Where**: the settings are in **Integrations**, group *Reachability from a country*. Results show as the last column, *From Russia*, of **Health → Checks** (a row per node on a phone) and as a row on the node page; the column hides while every source of the check is off.
- **Settings** (owner only): for Globalping, on or off, the interval in minutes (default 15, at least 10) and an optional token, which is write-only and raises the free limit from 250 to 500 tests an hour. The **country** (default `RU`) is a separate, shared setting, *Country reachability*, because every source of the check (Globalping, the [block lists](#block-lists) and the [TSPU probe](#tspu-probe)) looks from the same one. **Test** reads Globalping's rate-limit endpoint and sends no address. **Disconnect** switches the integration off and forgets the token.
- **Off means off.** With the integration off no round starts and nothing is sent. A round in flight is cancelled when you switch it off. Stored results stay for 7 days, but the API stops returning them.
- **What is asked.** Three probes (two when the budget needs it) take part in every round. Each node gets an ICMP ping of its address and a TCP ping of each TCP port of its active profiles (VLESS, and the HTTPS site of Hysteria2). A node with only UDP profiles gets the ICMP ping alone: Globalping has no UDP test.
- **Control.** Every round the same probes also open TCP 443 to a large site that is never blocked in that country (`ya.ru` for `RU`). A probe that cannot reach it is thrown away for that round. With fewer than two valid probes the verdict is `unknown`.
- **Verdict per node.** `ok`: TCP connects from at least half of the valid probes (ICMP for a node without TCP ports). `tcp_blocked`: ICMP answers from at least half, but TCP fails from all of them: the connection requests are dropped while the address is not. `blocked`: nothing passes. `unknown`: too few probes, a limit, an error, a timeout, or a split picture that proves nothing.
- **Budget.** A round costs (1 control + for each node 1 + its TCP ports) x probes tests. If the rounds of an hour would use more than 80 % of the limit, the probes go down to two, and if that is still too much the interval is stretched (the panel logs it once). On a rate limit the round is `unknown` and the next one waits until the limit resets.
- **Both editions.** The VPS panel looks every 30 seconds whether a round is due; the Cloudflare edition does it from its once-a-minute cron.

The alert `country_blocked` opens after two rounds in a row with `blocked` or `tcp_blocked`, and closes after two rounds with `ok`; `unknown` rounds change nothing. With the [TSPU probe](#tspu-probe) on, a round is a Globalping round or a run of the probe node, each combined with the newest result of the other; a node that only the probe node sees gets a round per run. A block list never counts. The alert and its Telegram message say which sources saw the block (`globalping`, `tspu`) and how sure the verdict is (`eyeball`, `dc_only`, or `conflict` when the two disagree).

### Block lists

The second source of the country check: the panel compares the IPv4 address of every active node with public lists of blocked addresses. The lists are downloaded to the panel and compared there, so no node address leaves the panel. It is an integration of its own, **off by default**, and works for Russia only: with another country selected the card says so and nothing is downloaded.

- **Where**: the *Block lists* card next to Globalping in **Integrations**, group *Reachability from a country*. Its verdicts go into the same *From Russia* column and node row.
- **The lists.** [`ipsum.lst`](https://antifilter.download/list/ipsum.lst) from antifilter.download, the RKN registry summed up to /24 subnets, with [antifilter.network](https://antifilter.network/download/ipsum.lst) as the mirror when the main address does not answer. [`community.lst`](https://community.antifilter.download/list/community.lst), addresses the community found blocked. [cdn-ip-ranges](https://github.com/123jjck/cdn-ip-ranges), the ranges of CDN and hosting providers: it only names the subnet a node sits in ("Hetzner subnet, DE") and never makes a verdict. IPv6 addresses are not checked.
- **Schedule.** Every 6 hours the panel refreshes the lists with a conditional request (an unchanged list costs a 304) and compares every node again; a new node is compared at once. The lists are kept in memory only. When no list can be downloaded, the next try is in 15 minutes. **Test** does it now and reports the date of the lists, the number of prefixes and CDN ranges, how many nodes are listed, and whether the mirror answered. **Disconnect** switches it off and also deletes the downloaded lists and every stored result.
- **`listed` never opens an alert.** A node whose address is in a list and that no live source has checked shows `listed`, in yellow: in the registry, not checked live. The registry says the address is written down, not that its traffic is dropped, and a list alone would raise alerts for every node in a crowded subnet. A live verdict from Globalping always wins; the lists then add their detail to its tooltip.
- **Off means off.** With the source off nothing is downloaded, neither on schedule nor by **Test**, and Health shows nothing from it. Stored results stay hidden until they expire after 7 days.

### TSPU probe

The third source of the country check, and the one that tests the TCP ports of your nodes from inside the country with your own hardware. One of your nodes in the country, the **probe node**, finds the box of the national DPI (the TSPU) on its own path and then sends TCP SYN packets with a limited TTL to the other nodes: when the packets die silently at that box and no router behind it answers, the port is blocked. The method is the one of [tspu-check](https://github.com/porutchik/tspu-check) and [cheburcheck](https://github.com/LowderPlay/cheburcheck); the node agent re-implements it and uses none of their code. It is an integration of its own, **off by default**, and works for Russia only: with another country selected the card says so and nothing is sent.

- **Where**: the *TSPU probe* card in **Integrations**, group *Reachability from a country*. Its verdicts go into the same *From Russia* column and node row; the tooltip names the probe node and the hop where the box sits (`dpi_hop`).
- **Settings** (owner only; changing them asks for a fresh confirmation): on or off, the **probe node** and the interval (default 120 minutes, at least 30). The picker lists the nodes in the selected country first. A node can be the probe node when it is online and its agent declares `country-probe/1`; an older agent has to be updated first. Nothing runs until a probe node is chosen.
- **A data-centre view.** The probe node sits in a data centre, so it sees the filtering of its own uplink, not what a person on a home or mobile network sees. Its verdicts carry the confidence `dc_only`, and the home and mobile probes of Globalping (`eyeball`) outrank it.
- **How it joins the verdict.** For each node the newest run counts as a live verdict: every tested port blocked, or some blocked and none open, is `tcp_blocked`; any open port makes it `ok`. When Globalping and the probe node agree, the verdict keeps the better confidence. When they disagree, the **worse** verdict is shown with the confidence `conflict` (better a false alarm than a miss), and the tooltip says what each of them saw. A run older than three intervals no longer counts. The block lists stay detail only.
- **What is sent.** Commands to the probe node, over its agent link. The probe node then sends SYN packets to the addresses of your other online nodes (the TCP ports of their enabled profiles, 443 first; never to itself) and to two fixed addresses of the country: a control that is never blocked (Yandex) and a reference that the TSPU always drops, which is how the box is found. The references are built into the panel; when the first cannot place the box, the panel tries the next one. No node address goes to a third party.
- **No extra privileges.** The probe is an ordinary TCP connection attempt with a limited TTL, so the node agent keeps its hardened service: no raw sockets, no `CAP_NET_RAW`. It relies on the kernel handing the answers of routers to the connecting socket. That was tested on one kernel (Linux 6.6) only; watch the first run on a node with an older one, which ends as `no_dpi_hop` or `failed` if the kernel does not cooperate.
- **Only TCP.** UDP is not tested. A Hysteria2 or AWG node is judged by the TCP ports of its other profiles (VLESS, the HTTPS site of Hysteria2); a node with only UDP profiles and no site gets no verdict from this source.
- **Schedule.** Every interval the panel sends the probe node a few commands in a row (6 to 8 pairs of host and port each; a blocked pair is checked again alone, so one command can take two minutes) and stores each answer as it arrives. The next run is due when the newest stored row is older than the interval, so restarting the panel does not start one. A busy probe node is asked again after 5 minutes; an offline probe node, or an agent without `country-probe/1`, skips the round. **Test** runs it now (at most once a minute; it can take a few minutes) and shows the status and the verdict of each node.
- **Status of the last run.** `ok`. `control_failed`: the control address is blocked behind the box on the probe node's network, so the method does not apply there and no verdict is made. `no_dpi_hop`: the box could not be placed, no reference worked. `busy`. `failed`: no answer, an agent that is too old, or a lost link.
- **Off means off.** With the source off no command is sent, neither on schedule nor by **Test**, and Health shows nothing from it. Stored rows stay hidden for 7 days. **Disconnect** switches it off and deletes the stored rows and the settings (the probe node, the interval, the last run).
- **Not on the Cloudflare edition yet.** A run takes minutes and the scheduled jobs of that edition have seconds, so the card says the source is unavailable there and nothing runs (see [Not available in this edition yet](../getting-started/install-edge.md#not-available-in-this-edition-yet)).

## Node doctor

Every node agent runs 17 checks of its own host. They only read: files under `/proc` and `/sys`, a few configuration files and read-only commands (`systemctl list-*`, `nft list`, `timedatectl show`, `dmesg`, `journalctl -k`). Changing anything is a separate, confirmed step (see "Fixes" below).

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
| Base network settings (`net_baseline`) | `fq` as the default qdisc, BBR congestion control, UDP buffer maxima (`net.core.rmem_max` and `net.core.wmem_max`) of at least 16 MiB, Mistgate's sysctl file and its journald drop-in. New UDP relay sockets inherit at least 1 MiB from `net.core.rmem_default` and `net.core.wmem_default`; `net.ipv4.udp_mem` still caps total UDP memory. The conntrack table has at least 65,536 entries and 16,384 buckets, preserving higher live values. | Something differs, or the conntrack table is at least 80% full | None | Restore baseline settings when they differ |
| Certificates (`cert_expiry`) | The certificate each TLS profile actually serves, whether it matches the domain, and the agent's own certificate for the panel connection. | A profile certificate has less than 14 days; the agent certificate less than 5 days | Less than 3 days, expired or not matching the domain; the agent certificate less than 1 day | Restart profile, for a self-signed certificate only |
| Memory (`memory_pressure`) | Available memory, swap, memory pressure (PSI) and OOM kills in the kernel log of the last 24 hours. | Less than 12% available, swap over 50% under pressure, or an OOM kill | Less than 5% available, heavy pressure, or the OOM killer hit the agent or a VPN engine | None |
| CPU softirq (`cpu_softirq`) | Mean softirq share and total CPU over 10 minutes. Skipped until 30 samples are collected. | Softirq 50% or more | Softirq 90% or more, or CPU 97% or more | None |
| Kernel headers (`kernel_headers`) | Headers, `dkms`, `make` and `gcc` for the AmneziaWG kernel module. Skipped without an AmneziaWG profile; only a fact while AmneziaWG runs in userspace. | Kernel mode is requested and the module does not run while tools are missing | None | None: see [AmneziaWG](../guide/amneziawg.md) |
| AmneziaWG backend (`awg_backend`) | Which backend runs AmneziaWG: the kernel module or userspace. Skipped without an AmneziaWG profile. | The host firewall drops forwarded traffic (often Docker) | No working backend: no `/dev/net/tun`, an outdated service file, or no module in kernel mode | Refresh the service when `unit_outdated` is reported and the agent offers the fix |
| WARP exit (`warp_path`) | The node's WARP tunnel and its routes. Skipped when the node has no WARP account and no profile exits through WARP. | WARP is paused while profiles exit through it, or the tunnel is up but its latest check failed | Profiles exit through WARP but the node has no account, the host clashes with WARP's routing table, rule priority or interface name, no WireGuard backend, or WARP is down | Check WARP / reconnect if down, when the tunnel is down or its latest check failed |
| Unit generation (`unit_generation`) | Whether the systemd unit running the agent matches this build. Skipped when the agent was started by hand. | The unit is older than this build | None | Refresh the service; the agent restarts and connections can drop for a few seconds |

A node without IPv6 that runs WARP is fine: WARP uses its IPv4 endpoint, and the admin shows the IPv6 item as OK.

A warning or problem opens an alert (see "Kinds of alerts" below). The certificate check is the exception: it opens a "Certificate is about to expire" alert instead of a doctor alert.

### Fixes

There are exactly six fixes. They are compiled into the agent: the panel sends only the fix id, and the agent checks the parameters (a profile must be one the node runs). None of them installs packages or deletes data.

| Button | What it does | Offered by |
|---|---|---|
| Trim the journal | `journalctl --vacuum-size=200M --vacuum-time=7d` | System journal size, Disk space |
| Restore the base settings | Writes Mistgate's sysctl file (`/etc/sysctl.d/90-mistgate.conf`: fq, BBR and UDP buffer maxima of at least 16 MiB) and journald drop-in (`/etc/systemd/journald.conf.d/90-mistgate.conf`: the 200 MB cap) again, and the SSH guard (a per-address rate limit on new SSH connections). | Base network settings |
| Restart profile | Restarts one profile on the node, or every profile that failed to start. Connections through it drop for a couple of seconds and come back by themselves. | Ports in use, Certificates (self-signed) |
| Fix the resolver | Points the host resolver at the node's **DNS resolvers for this node** (node settings) or, when that is the server's own resolver, at the default for the node's country: Yandex DNS (`77.88.8.8`, `77.88.8.1`) on nodes in Russia, `1.1.1.1` and `8.8.8.8` elsewhere. With systemd-resolved it writes a drop-in (`/etc/systemd/resolved.conf.d/90-mistgate.conf`); otherwise it rewrites `/etc/resolv.conf` and keeps the original as `/etc/resolv.conf.mistgate.bak`. | Server resolver |
| Check WARP / reconnect if down | Runs the WARP checks again at once. Only when the tunnel is down, or this check takes it to its failure threshold, it rebuilds the configured tunnel and its routes with the same account; otherwise nothing changes. During a reconnect, profiles using WARP may briefly lose traffic. If the tunnel recovered before Apply, nothing is changed. | WARP exit |
| Refresh the service | Runs the installed binary from a fixed transient systemd unit to write its compiled unit template, reload systemd and restart the agent. The node identity and state stay in place; connections can drop for a few seconds. | Unit generation; AmneziaWG backend when it reports `unit_outdated` |

A fix always takes two steps:

1. Click the fix button on the doctor item or on its alert. The panel asks the node what it would do ("Asking the node what it would do…"); the node answers with a dry run that changes nothing.
2. The dialog shows the plan: for example, the journal size before and after, or the profiles to restart and how many connections drop. **Apply** performs exactly that plan.

The plan is valid for 10 minutes, works once and is bound to the node, the fix and its parameters. One fix runs at a time on a node. Both steps go to the audit log. After a successful fix the node runs the affected checks again, and alerts that the fix cleared close as "fixed". For a unit refresh, the apply result reaches the panel before the restart; the agent reports again after it reconnects. Applying a fix needs the owner.

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

![The life of an alert: it opens while its condition holds, Mute hides it for up to 7 days, it closes when the condition is gone, reopens if the condition returns within an hour, and is kept 90 days](../diagrams/alert-lifecycle.svg)

### Kinds of alerts

| Title in the admin | Kind | Severity | Opens when |
|---|---|---|---|
| Node is unreachable | `node_down` | Critical | The agent has had no connection for 10 minutes. A shorter outage is an event, not an alert. |
| Host blip | `host_blip` | Info | History only: written when the link came back within 10 minutes without a reboot. It is never active. |
| Alive, but no traffic | `no_traffic` | Critical | The agent is connected and the profiles run, but every checked profile of the node failed two rounds in a row. The node's status turns to "no traffic". |
| "<profile>" fails the client-eye check | `check_failed` | Warning; Critical when it is the node's only profile of that protocol | Some, not all, profiles of an online node failed two rounds in a row. |
| UDP packet loss on a profile port | `port_lossy` | Warning | The latest check finds loss on the current port of an enabled inbound. Closes after a clean re-check, or when the inbound changes port, is disabled or removed, or the node is retired. |
| A node is unreachable from the country | `country_blocked` | Warning | The [country check](#country-check) saw `blocked` or `tcp_blocked` in two rounds in a row (Globalping, the [TSPU probe](#tspu-probe), or both). Closes after two rounds with `ok`; `unknown` rounds change nothing. Muting works as for any other alert. |
| The panel cannot reach many nodes | `check_failed` (fleet-wide) | Warning | The last round failed for at least 80% of the checked profiles, on nodes of at least three different **Provider** values. The panel's own connection is the suspect: per-node check alerts are paused until a round passes. Fresh failures (streaks 1–3) on nodes from at least two providers also open one fleet-wide warning; they do not open per-node alerts, existing alerts stay unchanged, and failures that reach streak 4 (about 20 minutes) are judged per node. |
| The doctor item's title | `doctor_warn` / `doctor_fail` | Warning / Critical | A doctor check reports Attention / Problem. A kernel headers warning on a node without an AmneziaWG profile is Info. |
| The node's state differs from the panel's | `state_drift` | Warning | The configuration the node applied differs from the panel's even after an automatic full resend. |
| Certificate is about to expire | `cert_expiry` | Warning; Critical under 3 days, expired or not matching the domain | From the doctor's certificate check. For an agent without the doctor, from the panel's own view of the profile certificates (under 14 days). |
| The node update stopped | `update_failed` | Warning | A rollout paused because a node rejected the update, failed the check after updating, or the panel lost track of a step after a restart. Closes when the rollout is resumed, cancelled or replaced. See [Updates](updates.md). |
| A user keeps trying after access ended | `access_ended` | Warning | A user with an expired subscription or exhausted quota fetched a subscription in the last six hours. |
| A user may have a connection problem | `user_connection` | Info | An AWG device has never connected or has a stale key, or an active user keeps fetching a subscription despite having no recent traffic. |
| An app was turned away at the device limit | `device_limit` | Warning | Someone was actually refused at the device limit in the last 24 hours: a new app in the **Limit** or **Strict** mode, or a key that could not be made. One alert per person. It closes after 24 hours without a refusal. In the **Only count** mode nobody is refused: the events "the device limit is reached" are history only and open no alert. Telegram tells it once when it opens, with no message when it closes. See [Subscriptions](../guide/subscriptions.md#device-limit-for-apps-on-the-link). |
| A user tries torrents | `torrent` | Warning | A user has at least one blocked `torrent_attempt` event with an `evidence` in the last 24 hours. One alert per person, over all nodes. It closes after 24 hours without an attempt. Telegram tells it once when it opens (no message when it closes), so at most once a day per person. Attempts the node cannot attribute to a user raise nothing, and neither do the events of an older agent, which carry no evidence. See [Torrent protection](../guide/torrent-protection.md). |
| Connections dropped for some users | `users_impacted` | Warning | After 20 minutes in the hour, the peak is at most a quarter of the seven-day median for that hour, with a baseline of at least three users. |

These alerts are derived from user, device and hourly peak data the panel already stores. The alerts about a person (`access_ended`, `user_connection`, `device_limit`, `torrent`) are listed on the **People** tab of Health and in the **Connection** block of the user's page, not among the node alerts, and they do not count in the alert badge; `users_impacted` is about a node and stays with the node alerts.

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

Closed alerts are kept for 90 days. The API returns up to 30 days of history, at most 200 alerts. A long history is paged under the list (20, 50 or 100 rows); the page number is kept in the address.

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

> **Note:** Alerts are seen in the admin (the header badge, the Overview, the Health page) and through the [API](../reference/api.md) and [MCP](../reference/mcp.md). The panel can also send them to Telegram when they open and when they resolve: see [Telegram alerts](telegram-alerts.md). A muted alert stays quiet there too.

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
| Possible BitTorrent attempt by … | The node's torrent protection blocked a flow that a client started and that it recognised as BitTorrent. A uTP SYN alone is not reported: it takes at least five of them to three destination ports from the same user and profile within 10 minutes; the other matches are reported at once. The line under it names the protocol, what the node matched and the destination port. The user is named only when the client's tunnel address belongs to exactly one account, otherwise it says "an unknown user". Neither the client's address nor the destination address is stored, only the port. See [Torrent protection](../guide/torrent-protection.md). |
| updated to … / update failed / update rolled back | A rollout step ended. |

A few warnings of the agent have no sentence of their own yet and are shown by their code, with the error text under it:

| Code | Written when |
|---|---|
| `torrent_guard_degraded` | Torrent protection is on, but the node could not set up the packet queue that inspects AmneziaWG traffic (for example a build without it). AmneziaWG traffic is then not inspected; Hysteria2 is checked inside its engine and is not affected. |
| `host_firewall_sync_failed` | The agent could not bring the UFW rules for the servers' UDP ports in line, a UFW rule of yours denies one of these ports, or an active firewalld does not open them. The event lists the UDP ports and hop ranges the server's firewall has to let in. `host_firewall_sync_recovered` follows when it works again. See "The hoster blocks UDP" in [Troubleshooting](troubleshooting.md). |

Events of severity info are kept for 90 days, warnings and errors for 400 days. Raw check rounds are kept for 25 hours after the day is summarised; the daily summaries for 90 days.

## Who can do what

| Action | Owner | Helper | Read-only |
|---|---|---|---|
| See alerts, checks and doctor reports | yes | yes | yes |
| Mute an alert, Check now, Run again | yes | yes | no |
| This is normal for this node, Count it again | yes | yes | no |
| Apply a doctor fix | yes | no | no |

An API token with the operator profile can mute alerts, start checks and run the doctor. A doctor fix through a token is possible only via MCP, as a plan the owner approves (see [MCP](../reference/mcp.md)).
