---
title: Troubleshooting
description: Common problems by symptom, how each one shows in the panel, and what to do about it.
---

Find the symptom, check how it shows in the panel, then follow the steps. Most answers start on the node's page: its banner, the **Profiles**, **History** and **Doctor** tabs, and the [Health](health.md) page.

Commands on a node run as root. The agent's service is `mistgate-node`; its log is in the journal:

```sh
journalctl -u mistgate-node -n 100 --no-pager
```

## A node stays offline

**How it shows**

- **Waiting for install**: the node was added, but no agent has connected yet. The banner ("Waiting for the agent") says until when the install command is valid, or that it has expired. For a node you install over SSH, see "An SSH installation fails" below.
- **Host blip** (grey): no link for less than 10 minutes. Short drops are usually the hoster; nothing is alerted yet.
- **Unreachable**: no link for 10 minutes or more. The alert "Node is unreachable" opens, the event "stopped answering" is written.

**What to do**

1. Open the hoster's panel and check that the server is running.
2. If it is, log in over SSH and restart the agent (the banner has this command to copy):

   ```sh
   systemctl restart mistgate-node && journalctl -u mistgate-node -n 50 --no-pager
   ```

3. Check that the node can reach the panel: the agent always dials out, to the address in its install command (`--panel host:port`, port 443 by default). An outbound firewall on the node or an inbound one on the panel server blocks it.
4. On the panel side, the agent endpoint needs TLS: the public listener must run with `--acme-domain` or `--tls-cert`, or the panel needs a separate `--agent-listen`. See [Configuration](../reference/configuration.md).
5. If the log says the agent is not enrolled, or the node was retired, the agent exits and systemd does not restart it. Enroll it again with a **New install command** from the node's **Server** tab. A retired node cannot be enrolled again: add the server as a new node.
6. For a far or slow node, raise **Drop the agent connection after** and **Agent dial timeout** on the node's **Server** tab.

A pending node whose command has expired needs a **New install command**; the old one never works again.

## Enrollment fails

`mistgate-node enroll` prints `enroll:` and the reason.

| Message | Cause | What to do |
|---|---|---|
| `--panel, --sni, --ca-sha256 and a token source are required` | The command was cut when copying. | Copy the whole command again. |
| `enrollment token unknown, expired or used` | An install command works once, for one hour. | Make a **New install command** on the node's page. |
| `the CA returned by the panel does not match --ca-sha256`, or `no certificate in the chain matches the pinned CA fingerprint` | The agent reached something that is not this panel: a wrong address, or a proxy that ends TLS in between. | Check `--panel`. The agent needs a direct TLS path to the panel. |
| `already enrolled; use --force to replace the identity` | The state directory already holds an identity. | Add `--force` to a fresh command, or remove the old node first. |
| `too many failed attempts, try later` | Too many failed attempts from this address in the last minute. | Wait a minute. |
| `node retired` | The node was retired in the panel. | Add the server as a new node. |
| `remote error: tls: handshake failure` (also `unrecognized name` or `internal error`) | The server at that address did not accept the panel's agent name, so it is not the panel itself: a CDN proxy (a proxied record at Cloudflare), a reverse proxy or a hosting control panel ends TLS there, another service holds the port, or the command is from another panel. The certificate behind the agent name is issued by the panel's own CA on purpose and is pinned by `--ca-sha256`: there is nothing to replace or configure. | Give the agents a direct TLS path: put the server's own IP or a DNS-only name into `--agent-addr`; behind a reverse proxy give them a port of their own with `--agent-listen` ("Behind a reverse proxy" in [Install the panel](../getting-started/install-panel.md), and [Can the panel run behind Cloudflare?](../reference/faq.md)). Then make a **New install command**. |
| A connection error or a timeout | The panel cannot be reached on that address and port. | Check DNS, the firewalls, and that the panel speaks TLS there (see above). |

`install` needs root and an enrolled state directory: run `enroll` first, with the same `--state-dir` if you changed it.

If **Create install command** in the admin fails with "panel address is not configured", the panel does not know the address agents should dial: run setup with `--public-url`, or serve with `--agent-addr` (see [CLI](../reference/cli.md)).

## An SSH installation fails

**Nodes → Add node → Install automatically over SSH** connects from the panel's server, so the node's SSH port must be reachable from there, and the node must reach the panel back. When a job fails, the wizard says why and offers **Retry installation** (with the SSH password again; it continues from where it stopped) and **Open install manager**, which shows the exact reason.

The most common causes:

- The hoster's firewall or security group does not let the panel's server in on the SSH port ("The panel could not reach this SSH address and port in time"). The panel cannot change the hoster's rules.
- The server cannot reach the panel's agent address, or the panel has no public address: both show "The panel's public address is not configured, so the node cannot connect back". Check `--public-url` or `--agent-addr` and the node's outbound TCP to the panel (usually 443).
- The panel holds no trusted node bundle yet ("Installation stopped"): see the **Release bundle** line on the [Updates](updates.md) page.

Every message and what to do about it: "When it fails" in [Install a node over SSH](../getting-started/ssh-install.md).

## A profile does not start

**How it shows**

- On the node's **Profiles** tab the profile says **Failed to start** with the reason, and the node banner says "“<profile>” failed to start".
- The client-eye check shows "✕ not started"; the event "“<profile>” failed to start" is written.

| Reason in the admin | What to do |
|---|---|
| Port N is taken by another program (…) | The doctor's **Ports in use** names the process. Stop that program, or move the profile to a free port on this node: **Change port** on the Profiles tab. To see the holder yourself: `ss -lupn 'sport = :443'` (UDP) or `ss -ltpn 'sport = :443'` (TCP). |
| Could not get the certificate: it needs a domain whose A record points to the node's IP, and ports 80 and 443 open | Set a **Domain (SNI)** whose A record points at the node, open TCP 80 and 443 on the node, or switch the profile to a self-signed certificate. Let's Encrypt does not issue certificates for an IP address. |
| There is no working WARP on the node | See "WARP does not work" below. |
| AmneziaWG did not start on the node | See "The AmneziaWG kernel module is missing" below. |
| The agent lacks permissions on the server | Read the original error under it and the node's log. The agent runs in a hardened service with a small set of permissions; if Health → Doctor reports an old unit, apply **Refresh the service** there, or run `mistgate-node install` once with the current binary when the agent is too old to offer it. |

When the port is free again, **Restart** the profile (or apply the doctor's **Restart profile** fix).

A port inside the port-hopping range of another profile, or held by Caddy or another web server on TCP 443, is reported by the doctor too. The add-profile dialog checks the port and the certificate before you add a profile to a node and offers a free port.

## Clients connect, but no traffic

**How it shows**

- Checks are **degraded** (the tunnel comes up, one test page fails) or fail with "The tunnel works, but the node's exit to the internet does not" or "The test sites answer with an error".
- The alert explains it in the reason; people say "connected, but nothing opens".

**What to check**

1. The node's own resolver: the doctor's **Server resolver**. The check names are resolved by the node; if they fail, apply **Fix the resolver**.
2. **DNS resolvers for this node** on the node's **Exit** tab: the resolvers your users' traffic uses. The default is the server's own resolver.
3. WARP: if only the profiles with WARP egress fail, see "WARP does not work".
4. AmneziaWG: the doctor's **AmneziaWG backend** warns when the host firewall drops forwarded traffic (often a Docker installation on the same server).
5. The doctor's **Foreign firewall rules** and **Leftovers of other VPNs**: a foreign nat or redirect rule can swallow the traffic.
6. The doctor's **IPv6**: an IPv6 address that cannot connect out makes sites with IPv6 records stall.

### The checks pass, but one person's app does not work

The panel's checks connect with the panel's own key, so a green check says nothing about the keys of a person's apps. Each app that fetches the subscription with a device ID gets keys of its own (see [Users and groups](../guide/users-and-groups.md)); the keys of a removed device stop working, and an app over the device limit gets only a placeholder named after the limit.

1. On the person's page, look at their status (disabled, expired, over quota) and **Devices**: how many places are taken out of the limit, and which apps by link.
2. The **24 h** line under the profile on the node's **Profiles** tab: **unknown keys** that grow while the person tries mean their app comes with a key the node does not know, from a removed device or an old subscription.
3. Remove the old apps there, then in the app delete the subscription, add the link again and refresh it. One new device appears. Check that the app's list has no entry named after the device limit.

## A proxy on the same server stops working when the VPN is on

A full-tunnel AmneziaWG client can reach the panel on TCP 443 when it shares the node's address, plus other enabled inbounds and the Hysteria2 HTTPS decoy on their configured ports. The AWG UDP listener ports, SSH, and node management ports stay closed from inside the tunnel.

## The hoster blocks UDP

Hysteria2 and AmneziaWG run over UDP, and some hosters drop UDP on some ports or entirely.

**How it shows**

- The check fails with "No handshake: the UDP traffic probably does not arrive".
- "“<profile>” does not answer on port N, while another profile of this node does: the hoster cuts UDP port N."
- "Alive, but no traffic": "the hoster seems to cut this UDP port", or, on 443 or on several ports, "the hoster seems to cut incoming UDP altogether".
- For AmneziaWG, the profile's status on the node's **Profiles** tab tells the two cases apart: "Not one packet reached the port: the hoster or a firewall blocks this UDP port", or "Packets arrive but no handshake completes: the obfuscation parameters on the node and in the config differ".
- The node's **History → Events** show `host_firewall_sync_failed`: the server's own firewall does not let a profile's UDP port in.

**What to do**

1. Check the firewall or security group in the hoster's panel: the profile's UDP ports (and its hop range) must be open for incoming traffic.
2. Check the server's own firewall. With an active UFW the agent adds allow rules for the UDP ports and hop ranges of the node's servers itself, but it never changes a rule it did not add: a `deny` or `reject` rule of yours for such a port stays, and the event names it. firewalld is never edited by the agent: add those UDP ports to its active zone by hand.
3. Move the profile to another port on this node: **Change port** on the node's **Profiles** tab.
4. If every port is cut, write to the hoster's support or move the node.

The check uses the profile's main port, not the port-hopping range, so a blocked hop range does not show here.

## Works on Wi-Fi, not on mobile data

**How it shows**

- On Wi-Fi the app works; on mobile data it connects and stops answering a few seconds later, or never connects.
- With AmneziaWG on mobile data the first handshake may pass, and then the app's new handshakes never reach the node; a profile on UDP 443 may get no packet at all.
- The panel's checks and the port check pass.

**What it means**

The person's mobile operator cuts the traffic, not the node or the hoster: UDP to foreign addresses, QUIC, or every connection to foreign hosting after a few kilobytes. The panel cannot see it: the port check sends from data centres, and the [reachability](reachability.md) checks from the country test ICMP and TCP, not the UDP ports of AmneziaWG and Hysteria2.

**What to do**

1. Give the person a VLESS REALITY server over TCP 443 for mobile data: see [VLESS](../guide/vless.md).
2. If connections to foreign hosting freeze after a few kilobytes, add a [relay](../guide/relays.md) through an entry node in the person's country. It carries VLESS and Hysteria2, not AmneziaWG yet; under a mobile whitelist it helps only if the entry address is on the list.
3. Another UDP port or another AmneziaWG mimicry preset sometimes helps. The obfuscation parameters rarely do: the operator cuts the flow, not a signature.

## The clock drifts

**How it shows**

- The doctor's **Clock**: Attention over 2 seconds off the panel's clock or when NTP is not synchronised, Problem over 30 seconds.
- The node's events: "the server clock differs from the panel's by N s" when the offset exceeds 30 seconds.

A wrong clock breaks certificates, authenticator codes and WireGuard handshakes.

**What to do** on the node:

```sh
timedatectl set-ntp true
timedatectl
```

If authenticator codes for the admin keep failing, check the clock of the panel server and of the phone: codes are accepted only within one 30-second step either way.

## The AmneziaWG kernel module is missing

**How it shows**

- The profile on the node: "AmneziaWG did not start on the node: see “AmneziaWG backend” on the node's Server tab".
- The doctor's **AmneziaWG backend** reports no working backend; **Kernel headers** lists what is missing for the module.

**What to do**

- The simplest: set the node's **AmneziaWG backend** to **Auto** (the kernel module when it is already loaded, else userspace) or **Userspace**. Userspace needs no module, only `/dev/net/tun`.
- To use the kernel module: choose **Kernel module**. An up-to-date agent builds the module itself after you confirm; AmneziaWG keeps running in userspace until it is ready. Otherwise run it on the node yourself; without `--yes` it only prints the plan:

  ```sh
  mistgate-node awg prepare-kernel
  mistgate-node awg prepare-kernel --yes
  ```

  It supports Debian and Ubuntu (apt), not in a container, without Secure Boot, with systemd. A run started by the panel logs to the journal unit `mistgate-awg-prepare`.
- If the doctor says the service file hides `/dev/net/tun` (an outdated unit), apply **Refresh the service** from Health → Doctor. For an agent too old to offer the fix, run `mistgate-node install` once with the current binary.

See [AmneziaWG](../guide/amneziawg.md).

## WARP does not work

**How it shows**

- The node's WARP card: **Not working**, **No backend** or **Paused**.
- The doctor's **WARP exit** reports a problem; a profile says "There is no working WARP on the node".
- "The direct profile of this node works, but “<profile>” does not: its WARP exit is dead."

Profiles with WARP egress fail closed: they never send traffic out directly instead.

**What to do**

1. On the WARP card: **Restart WARP**, and look at the handshake, the probes and the last error. **Read from Cloudflare again** refreshes the account.
2. **Paused**: **Resume** it.
3. "WARP cannot use this host": another tool uses WARP's routing table, rule priority or interface name (for example an old `wg-quick` setup). Remove that tool's configuration.
4. **No backend**: the node has neither a WireGuard kernel module nor `/dev/net/tun`; if the doctor's unit check is old, apply **Refresh the service** in Health → Doctor. For an agent too old to offer it, run `mistgate-node install` once with a current binary.
5. "Profiles with WARP egress cannot start: the node has no WARP account": register or import an account on the node, or switch those profiles to direct.

A node without IPv6 is fine for WARP. See [WARP](../guide/warp.md).

## An update rolled back

**How it shows**

- On the Updates page the node is **Rolled back**; **Last update** says why.
- The rollout is paused and the alert "The node update stopped" is open.
- The node's events: "update rolled back".

| Reason | What to do |
|---|---|
| The new version did not connect and apply its configuration within 5 minutes | Read the node's log of that time; fix the link or the configuration. |
| The program is not the release the manifest describes | The bundle was signed for other binaries. Sign it again with `mistgate release sign` from a clean checkout of the same tag, without overriding `--built` (see [Releases](releases.md)). |
| The new version crashed three times in a row | Read the node's log. |
| The new version could not start a profile | Read the profile's error and the node's log. |
| The new program could not start (is it built for this machine?) | Check the architecture of the binary for that node. |
| The panel put the previous version back: the client-eye check failed after the update | See [Health](health.md) for the failing profile. |
| The panel put the previous version back: a profile that worked before broke after the update | Read the profile's error on the node. |
| The panel put the previous version back: the node did not apply its configuration after the update | Read the node's log of that time. |
| The panel put the previous version back: the new build did not confirm the update | The new agent never reported that the update took. Read the node's log of that time. |
| The panel put the previous version back: it could not read the node's profiles after the update | A read from the panel's own database failed, not the node. Read the panel's log of that time. |

After fixing the cause, resume or cancel the rollout and update the node again with a new rollout. See [Updates](updates.md).

## The admin is unreachable

| Symptom | What to do |
|---|---|
| The address shows "Coming soon" or a 404 | You reached the decoy: the admin address has a secret part. Run `mistgate setup` on the panel server: it keeps the settings and prints the admin URL. |
| The admin is on a separate listener | It serves plain HTTP on the address given at setup, usually loopback. Open an SSH tunnel, for example `ssh -L 8081:127.0.0.1:8081 root@panel.example.com`, then `http://localhost:8081/`. |
| "The browser refused the passkey for this address" | Open the admin at exactly the address setup printed: passkeys are bound to it. |
| "Too many attempts" | Password sign-in is locked for 15 minutes. Wait, or run `mistgate auth reset-login <login>` on the panel server. |
| Lost phone, or no passkey left | `mistgate auth reset-login`, see [Security](security.md). |
| The Cloudflare check does not pass | `mistgate auth turnstile off` on the panel server. |
| A "Too Many Requests" page | Too many requests from your network; wait the time in `Retry-After`. |
| A certificate error in the browser | With `--acme-domain` the public listener must be reachable on port 443, and `--acme-http` (port 80) serves the HTTP-01 challenge and the redirect. With `--tls-cert` the panel reloads the file when it changes. |
| Nothing answers | Check the service and its log on the panel server (`systemctl status`, `journalctl -u <your unit>`). |

Start-up errors of `mistgate serve` and what they mean:

| Message | What to do |
|---|---|
| ``data dir: … (run `mistgate setup`)`` | The data directory does not exist: on a new server run `mistgate setup`; otherwise point `--data-dir` at the existing directory. |
| ``master key: … (run `mistgate setup`)`` | `master.key` is missing and there is no database yet: run `mistgate setup`. |
| `master key: … is missing but the database exists: restore master.key from your backup …` | The installation lost its key. Put `master.key` back from a backup. `setup` refuses here: a new key cannot read the stored secrets. |
| `vault: … is accessible to group or others …; run chmod 600 on it` | Run `chmod 600` on the key file. |
| `--admin-listen conflicts with the stored admin address …` | The installation was set up for a secret host or prefix; drop `--admin-listen`. |
| `--tls-cert and --tls-key go together` | Give both or neither. |

## Panel logs, retention, export and metrics

On the VPS edition the owner finds these in **Settings → System**; an admin-profile token reads them through MCP (`panel_logs`, `retention_status`, `log_level_plan`, `retention_plan`).

- **Panel resources.** Settings → System → **Panel resources** shows what the panel itself costs its server: its process (CPU in percent of the whole server, memory with the Go heap, open files against the limit, goroutines and threads, how long it has run), the server (CPU busy with steal and disk wait, load, memory, swap, the disk of the data directory), the database (its size with the write-ahead log, and how much of it is free pages), the agents connected and the admin API's last 5 minutes (requests, server errors, the 95th-percentile response time). Every admin sees it, and any token reads it through MCP (`panel_resources`). The database is measured at most once a minute.

- **Log level.** `mistgate serve --log-level debug|info|warn|error` (or `MISTGATE_LOG_LEVEL`, default `info`) sets the level at start, and `--log-format json` (or `MISTGATE_LOG_FORMAT`) writes the log to stderr as JSON. The owner can change the level while the panel runs; a restart returns to the configured one.
- **Panel logs.** The last 5000 records at the current level are kept in memory (lost on restart) and shown, with filters for level, time, text, part of the panel, error code and request code. Addresses, the panel's secret names and key-shaped values are removed from every record, and only allow-listed attributes are shown. Every warning and error the panel writes carries a short `code`.
- **Request code.** A failed call in the admin shows "request code abc12345". The same code is in the response header `X-Request-Id`, in the log records of that request and in the audit row of a change it made, so a person who quotes it and an operator reading the logs talk about the same request. The Cloudflare edition does not stamp request codes.
- **Retention.** Settings → System → Retention sets how long each table the hourly sweep prunes keeps its rows: the node journal (1 to 7 days, default 7), info events (90) and warning and error events (400), the audit log (30 to 1095, default 400), resolved alerts (90), raw check rounds (25 to 168 hours, default 25), daily check aggregates (90), the hourly host load of a node (90), and the hourly traffic per person and per node (400 days each). A shorter time deletes the older rows at the next sweep, for good, and asks for a step-up. The sweep reads the values on every edition; they can be changed on the VPS edition.
- **Export.** Settings → System → Export downloads the events, the audit log, the nodes' saved journal and the panel's own logs of a window of up to 31 days as newline-delimited JSON (one object per line, a `source` field names the kind). Every row is cleaned like the panel logs, and client addresses are left out of the audit rows. It asks for a step-up.
- **Metrics.** `/metrics` on the admin address serves fleet gauges in the OpenMetrics text format: nodes by status, people online, active alerts by severity, deployed checks by latest result, per-node download and upload speed and CPU, relays by state, and the panel process's resident memory (`process_resident_memory_bytes`), goroutines (`go_goroutines`) and database size (`mistgate_db_bytes`). No person's name and no address. It is off until the owner turns it on in Settings → System; that makes a token, shown once and stored only as a hash, and a scraper sends it as `Authorization: Bearer <token>`. While it is off the address answers as any unknown admin path does.

## Debug capture of one node

When a node misbehaves and its warnings are not enough, the owner can ask it to log in detail for a few minutes. On the node's page, **Server → Debug capture**, pick the sources (the agent, `hysteria2`, `vless`, `xray`, `awg`, `update`, `kernel`; none picked means all), the level (debug or info) and the length (1, 5, 10 or 15 minutes), then press **Debug for N min**. A countdown shows the time left; **Stop and show** ends it at once. When the time is up the node sends what it kept and the panel shows the records with filters for level, source and text. The `kernel` source reads the node's kernel log from 30 minutes before capture start through the end; firewall packet-log lines are counted in its summary and not kept. On a kernel-backend node, `awg` turns on the module's debug output during the capture and adds a summary of handshake initiations without a response within five seconds.

- **Limits.** One capture per node at a time, at most 15 minutes, at most 20000 records and 4 MiB (what does not fit is counted, not kept). The node puts its log level back when the capture ends, and a restart of the agent ends it too: nothing is written to disk on the node. The panel keeps a capture and its records 24 hours and at most 5 captures per node; the hourly sweep deletes the rest (VPS edition only).
- **What is in it.** Every record is cleaned on the node (client addresses become `<ip>`, key-shaped text is removed, host names are cut from xray's lines, and only the node journal's short list of attributes is kept), again when the panel stores it, and once more when it is read, with the panel's secret names removed. Debug lines of the chosen sources go only to the capture, not to the node's own log.
- **Who and what is recorded.** The owner starts and stops a capture in the admin. An admin-profile token starts one with `node_debug_capture_plan` / `node_debug_capture_apply` and reads it with `node_debug_logs` (a capture or a node, filters `min_level`, `source`, `query`, `limit`, `cursor`). Every start is in the audit log with who asked, the node, the sources and the duration. An agent that is too old (it does not list `debug/1`) and a node that is offline are refused without anything being sent. The Cloudflare edition does not have it.

## Multipath TCP and CVE-2026-98432

CVE-2026-98432 is a flaw in the Linux kernel's IPv4 TCP (CVSS 9.8) that was shown as remote root through a listener that accepts Multipath TCP (MPTCP). Go opens every listener with MPTCP by default since Go 1.24, so the panel, the node agent and the Xray listeners it hosts accepted it up to v0.1.82. From v0.1.83 they open plain TCP, which nothing in Mistgate needs to change: clients that ask for MPTCP fall back to TCP.

The bug itself is in the kernel, so on every node and on the panel's server also:

1. Turn MPTCP off for every program on the host, Caddy and other Go programs included, and keep it off after a reboot:

   ```bash
   echo 'net.mptcp.enabled = 0' | sudo tee /etc/sysctl.d/99-no-mptcp.conf && sudo sysctl -w net.mptcp.enabled=0
   ```

2. Restart what listens (`sudo systemctl restart mistgate-node` on a node, `mistgate` on the panel's server, and Caddy where it runs): sockets opened before the change keep MPTCP until then. `ss -Hl -M` lists the MPTCP listeners that are left.
3. Install your distribution's patched kernel as soon as it is published, and reboot.
