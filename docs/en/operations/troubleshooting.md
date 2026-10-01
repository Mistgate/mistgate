---
title: Troubleshooting
description: Common problems by symptom, how each one shows in the panel, and what to do about it.
---

Find the symptom, check how it shows in the panel, then follow the steps. Most answers start on the node's page: its banner, the **Profiles**, **Events**, **Logs** and **Doctor** tabs, and the [Health](health.md) page.

Commands on a node run as root. The agent's service is `mistgate-node`; its log is in the journal:

```sh
journalctl -u mistgate-node -n 100 --no-pager
```

## A node stays offline

**How it shows**

- **Waiting for the agent** (pending): the node was added, but no agent has connected yet. The banner says until when the install command is valid, or that it has expired.
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
5. If the log says the agent is not enrolled, or the node was retired, the agent exits and systemd does not restart it. Enroll it again with a **New install command** from the node's page.
6. For a far or slow node, raise **Drop the agent connection after** and **Agent dial timeout** in the node's **Settings**.

A pending node whose command has expired needs a **New install command**; the old one never works again.

## Enrollment fails

`mistgate-node enroll` prints `enroll:` and the reason.

| Message | Cause | What to do |
|---|---|---|
| `--panel, --sni, --ca-sha256 and --token are required` | The command was cut when copying. | Copy the whole command again. |
| `enrollment token unknown, expired or used` | An install command works once, for one hour. | Make a **New install command** on the node's page. |
| `the CA returned by the panel does not match --ca-sha256`, or `no certificate in the chain matches the pinned CA fingerprint` | The agent reached something that is not this panel: a wrong address, or a proxy that ends TLS in between. | Check `--panel`. The agent needs a direct TLS path to the panel. |
| `already enrolled; use --force to replace the identity` | The state directory already holds an identity. | Add `--force` to a fresh command, or remove the old node first. |
| `too many failed attempts, try later` | Too many failed attempts from this address in the last minute. | Wait a minute. |
| `node retired` | The node was retired in the panel. | Add the server as a new node. |
| A connection error or a timeout | The panel cannot be reached on that address and port. | Check DNS, the firewalls, and that the panel speaks TLS there (see above). |

`install` needs root and an enrolled state directory: run `enroll` first, with the same `--state-dir` if you changed it.

If **Create install command** in the admin fails with "panel address is not configured", the panel does not know the address agents should dial: run setup with `--public-url`, or serve with `--agent-addr` (see [CLI](../reference/cli.md)).

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
| The agent lacks permissions on the server | Read the original error under it and the node's log. The agent runs in a hardened service with a small set of permissions; if the node was installed long ago, `mistgate-node install` writes the current service file. |

When the port is free again, **Restart** the profile (or apply the doctor's **Restart profile** fix).

A port inside the port-hopping range of another profile, or held by Caddy or another web server on TCP 443, is reported by the doctor too. The add-profile dialog checks the port and the certificate before you add a profile to a node and offers a free port.

## Clients connect, but no traffic

**How it shows**

- Checks are **degraded** (the tunnel comes up, one test page fails) or fail with "The tunnel works, but the node's exit to the internet does not" or "The test sites answer with an error".
- The alert explains it in the reason; people say "connected, but nothing opens".

**What to check**

1. The node's own resolver: the doctor's **Server resolver**. The check names are resolved by the node; if they fail, apply **Fix the resolver**.
2. **DNS for user traffic** in the node's **Settings**: the resolvers your users' traffic uses. Empty means the server's own resolver.
3. WARP: if only the profiles with WARP egress fail, see "WARP does not work".
4. AmneziaWG: the doctor's **AmneziaWG backend** warns when the host firewall drops forwarded traffic (often a Docker installation on the same server).
5. The doctor's **Foreign firewall rules** and **Leftovers of other VPNs**: a foreign nat or redirect rule can swallow the traffic.
6. The doctor's **IPv6**: an IPv6 address that cannot connect out makes sites with IPv6 records stall.

If the checks are green but one person cannot connect, look at the person: their status (disabled, expired, over quota) and devices on the Users page. See [Users and groups](../guide/users-and-groups.md).

## The hoster blocks UDP

Hysteria2 and AmneziaWG run over UDP, and some hosters drop UDP on some ports or entirely.

**How it shows**

- The check fails with "No handshake: the UDP traffic probably does not arrive".
- "“<profile>” does not answer on port N, while another profile of this node does: the hoster cuts UDP port N."
- "Alive, but no traffic": "the hoster seems to cut this UDP port", or, on 443 or on several ports, "the hoster seems to cut incoming UDP altogether".
- For AmneziaWG, the profile's status on the node's **Profiles** tab tells the two cases apart: "Not one packet reached the port: the hoster or a firewall blocks this UDP port", or "Packets arrive but no handshake completes: the obfuscation parameters on the node and in the config differ".

**What to do**

1. Check the firewall or security group in the hoster's panel: the profile's UDP ports (and its hop range) must be open for incoming traffic.
2. Move the profile to another port on this node: **Change port** on the node's **Profiles** tab.
3. If every port is cut, write to the hoster's support or move the node.

The check uses the profile's main port, not the port-hopping range, so a blocked hop range does not show here.

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

- The profile on the node: "AmneziaWG did not start on the node: see “AmneziaWG backend” in the node settings".
- The doctor's **AmneziaWG backend** reports no working backend; **Kernel headers** lists what is missing for the module.

**What to do**

- The simplest: set the node's **AmneziaWG backend** to **Auto** (the kernel module when it is already loaded, else userspace) or **Userspace**. Userspace needs no module, only `/dev/net/tun`.
- To use the kernel module: choose **Kernel module**. An up-to-date agent builds the module itself after you confirm; AmneziaWG keeps running in userspace until it is ready. Otherwise run it on the node yourself; without `--yes` it only prints the plan:

  ```sh
  mistgate-node awg prepare-kernel
  mistgate-node awg prepare-kernel --yes
  ```

  It supports Debian and Ubuntu (apt), not in a container, without Secure Boot, with systemd. A run started by the panel logs to the journal unit `mistgate-awg-prepare`.
- If the doctor says the service file hides `/dev/net/tun` (an outdated unit), run `mistgate-node install` once.

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
4. **No backend**: the node has neither a WireGuard kernel module nor `/dev/net/tun`; run `mistgate-node install` once to get the current service file.
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
| The program is not the release the manifest describes | Sign with `--built` from the same commit the binaries were built from. |
| The new version crashed three times in a row | Read the node's log. |
| The new program could not start (is it built for this machine?) | Check the architecture of the binary for that node. |
| The panel put the previous version back: the client-eye check failed after the update | See [Health](health.md) for the failing profile. |
| The panel put the previous version back: a profile that worked before broke | Read the profile's error on the node. |

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
| ``master key: … (run `mistgate setup`)`` | `master.key` is missing from the data directory. On an existing installation restore it from a backup and do not run `setup`: it would create a new key, which cannot read the stored secrets. |
| `vault: … is accessible to group or others …; run chmod 600 on it` | Run `chmod 600` on the key file. |
| `--admin-listen conflicts with the stored admin address …` | The installation was set up for a secret host or prefix; drop `--admin-listen`. |
| `--tls-cert and --tls-key go together` | Give both or neither. |
