---
title: Add a node
description: Choose between the SSH installation and the manual install, enroll a server with a one-time command, watch it come online, and remove it cleanly later.
---

A node joins the fleet when its agent gets a certificate from the panel, installs itself as a systemd service and connects. **Nodes → Add node** offers two ways:

| Way | What you do | When to pick it |
|:--|:--|:--|
| **Install automatically over SSH** | Enter the server's address, confirm its SSH host key, type the password; the panel does the rest. | The usual way. Described in [Install a node over SSH](ssh-install.md). |
| **Get a manual install command** | Copy the agent to the server and run a one-time command as root. | The panel cannot reach the server over SSH, the server allows only key login, or you want to run the commands yourself. Described on this page. |

An AI agent can also start an SSH installation through MCP; you approve it and type the password on the approval screen: see the [AI agent guide](ai-agents.md). This page also explains how to remove a node. If the dialog opens directly on the name, country and address fields, the panel predates the SSH installation: update it first (see [panel updates](../operations/updates.md)).

## Before a manual install

- A server that meets the [requirements](requirements.md), with root over SSH.
- The agent binary of the same release as the panel: `mistgate-node-linux-amd64` or `mistgate-node-linux-arm64`. A panel with a trusted release bundle prints a ready `scp` command for it; otherwise take it from the same [GitHub release](https://github.com/Mistgate/mistgate/releases/latest) as the panel, or from `bin/` of your own build.
- The panel must be reachable from the server at its public address on TCP 443 (or at the address you gave `serve --agent-addr`). The panel never connects to the node.

## 1. Choose manual installation

In **Nodes → Add node**, choose **Get a manual install command**. The same window opens from the **Add node** tile on the Overview, the command palette and the end of the setup wizard. If you already created a pending manual node, retire it before using the same name in the SSH wizard, or choose another unused name.

| Field | What to enter |
|:--|:--|
| Name | a–z, 0–9 and dash, 2 to 24 characters, for example `de1`. Names are unique among live nodes and active SSH installs; retiring a node releases its name while keeping its history. |
| Country | Optional. It matters for DNS: on nodes in Russia the doctor checks gosuslugi.ru and offers Yandex DNS. |
| Address | A domain or an IP, without `https://` and without a port. Clients connect to it, and it goes into every subscription. A Let's Encrypt certificate for Hysteria2 needs a domain whose A record points at the server. |

When the address is an IP, the window says so before you go on: Hysteria2 on that node then needs a domain with an A record or the self-signed (pinned) certificate, which you pick when you add the profile. AmneziaWG works on an IP as it is.

Press **Create install command**. The node appears in the list as "Waiting for install", and the window shows three steps.

## 2. Put the agent on the server

If the panel holds a trusted release bundle in its data directory (a panel from the official release downloads one by itself), step 1 of the window shows a ready `scp` command to run on the panel's server. It copies the amd64 agent; for an ARM server change `amd64` to `arm64`.

Otherwise copy the agent of the same release yourself, to `/root/mistgate-node`, for example from your build machine:

```sh
scp bin/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node
```

## 3. Run the command as root

Log in to the server over SSH as root and paste the command from step 2 of the window (**Copy the command**). It looks like this:

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

| Part | Meaning |
|:--|:--|
| `--panel` | The host and port the agent dials. |
| `--sni` | The secret TLS name that leads to the agent endpoint. Without it the panel shows the decoy site. |
| `--ca-sha256` | The fingerprint of the panel CA. The node trusts the panel only by it. The window also shows it under **Details**. |
| `--token` | The one-time enrollment token. It works once and expires after an hour; the window shows until when. |

On success it prints:

```text
enrolled as nod_... with panel.example.com:443; state in /var/lib/mistgate-node
next: mistgate-node install   (or, in the foreground: mistgate-node run)
installed /etc/systemd/system/mistgate-node.service (state /var/lib/mistgate-node)
started; follow it with: journalctl -u mistgate-node -f
```

> **Note:** while `enroll` runs, the token is visible in the server's process list. It is single-use and short-lived; if other people share the server, set `MISTGATE_ENROLL_TOKEN` or pass the token on standard input with `--token-stdin` instead of `--token`.

## What happens on the server

`enroll`:

1. Generates a P-256 key and a certificate request on the node. The key never leaves the server.
2. Dials `--panel` over TLS 1.3 with the secret name and accepts the server only if its chain ends in a CA with the pinned fingerprint.
3. Sends the token and the request. The panel checks the token (it stores only its hash), issues a client certificate valid for 30 days and returns it with its CA.
4. Writes the key and certificate (`identity.pem`), the panel CA (`ca.pem`) and the panel address (`agent.json`) into `/var/lib/mistgate-node` (directory 0700, files 0600).

`install`:

1. Checks that it runs as root and that the state directory holds an identity.
2. Copies itself to `/usr/local/bin/mistgate-node`. The file in `/root` is no longer needed.
3. Writes the hardened unit `/etc/systemd/system/mistgate-node.service`, sized to the server's memory, then runs `systemctl daemon-reload`, `enable` and `restart`.

The agent, once started:

1. Applies its host baseline: the fq and BBR sysctl values, UDP socket buffer maxima (`net.core.rmem_max` and `net.core.wmem_max`) of at least 16 MiB, the journald size cap and the SSH guard in its own nftables table. New UDP relay sockets inherit at least 1 MiB from `net.core.rmem_default` and `net.core.wmem_default`; `net.ipv4.udp_mem` still caps total UDP memory. The conntrack table gets at least 65,536 entries and 16,384 buckets, preserving higher live values.
2. Brings back the last applied state from its state directory (nothing on the first start).
3. Opens one long-lived connection to the panel with mutual TLS, reports the host's facts, and receives its settings and the desired state: which servers to run, with which users' credentials.
4. Sends traffic, sessions and host metrics every 10 seconds, runs the doctor 30 seconds after connecting and every 10 minutes after that, and renews its certificate (with a new key) when less than 10 days are left.

The full list of what the agent changes on a host is under "What the agent changes on a node" in [Requirements](requirements.md).

## 4. The node comes online

The window refreshes by itself. Within seconds of `install` step 3 turns into **Connected**, and the node's events show "connected for the first time" with the agent version. Press **Add a profile to de1** to go on with [First users](first-users.md): until a profile runs on it, the node says "no profiles: users do not get this node".

| Status | Meaning |
|:--|:--|
| Waiting for install | The node was created, no agent has enrolled yet. The node's page shows until when the command works. |
| Healthy | The agent is connected and its servers run. |
| Host blip | The connection dropped less than 10 minutes ago, often the hoster. |
| Unreachable | No contact for 10 minutes. The node's page shows the command to restart the agent and read its log. |
| Retired | Taken out of the fleet. |

If the node does not come online, read the agent's log on the server:

```sh
systemctl restart mistgate-node && journalctl -u mistgate-node -n 50 --no-pager
```

Lines with `panel connection lost` carry the reason. Check also that the server clock is right.

## New install command

A node that is waiting for install has a **New install command** button on its page, on the Overview and in the first-run checklist. Use it when the command expired or was lost. A new command makes the older unused ones stop working.

A node that was enrolled before (a lost certificate, a reinstalled server, a lost state directory) has the same button on its **Settings** tab, for every node that is not retired. Only the owner sees it. The node keeps its name, profiles and users; the old certificate stops working once the new agent connects. If the old state directory is still on the server, `enroll` refuses with `already enrolled; use --force to replace the identity`: add `--force` after `enroll`.

## Remove a node

Open the node, **Settings → Danger zone → Retire from fleet**, and type the node's name to confirm. The panel then:

- takes the node out of every user's access: users stop getting it in their subscription, their links stay the same;
- revokes the node's certificate and cancels its unused install commands;
- keeps the node's record and history in the panel and the audit log; its name becomes available for a later install;
- keeps the saved SSH access of a node installed over SSH: you can still reveal the password under the retired node's **Settings → SSH access**, and only **Forget saved access** deletes it. If the panel generated that password (a rotation through MCP), the retire dialog warns you: reveal it and keep a copy.

If the agent is connected, it gets the order to retire and, on the server:

1. stops its servers;
2. removes WARP, when the node had it;
3. removes its host changes: its nftables tables, its tagged UFW rules (the UDP listener rules and the 80/443 rules of an SSH install; the SSH rule stays), the AmneziaWG interfaces, the sysctl and journald files (journald is restarted, so its size cap goes at once), and the doctor's resolver fix if one was applied;
4. deletes its state directory (the key first) and exits.

What stays on the server: the binary, the unit (still enabled), the values already set in the running kernel (fq, BBR, UDP buffers, IP forwarding) until the next reboot, and the AmneziaWG kernel module with its packages if you installed it. Remove the rest by hand:

```sh
systemctl disable --now mistgate-node
rm -f /etc/systemd/system/mistgate-node.service
systemctl daemon-reload
rm -f /usr/local/bin/mistgate-node /usr/local/bin/mistgate-node.prev /usr/local/bin/mistgate-node.new /root/mistgate-node
rm -rf /var/lib/mistgate-node
```

> **Warning:** a node that was offline when you retired it never got the order. The panel refuses its certificate, but the agent keeps running the last state it applied (its servers keep working for the users it had) and keeps trying to connect. The panel says so right away ("{name} did not get the order") and shows all the commands to copy. Clean such a server by hand, starting with the steps below, then run the commands above.

```sh
systemctl disable --now mistgate-node      # its stop runs cleanup-net: tunnel interfaces, WARP routes, their tables
nft delete table inet mistgate_node        # the SSH guard and port-hopping rules
rm -f /etc/sysctl.d/90-mistgate.conf /etc/systemd/journald.conf.d/90-mistgate.conf
systemctl restart systemd-journald
```

If you applied the doctor's resolver fix on that node, remove `/etc/systemd/resolved.conf.d/90-mistgate.conf` and restart systemd-resolved, or put `/etc/resolv.conf` back from `/etc/resolv.conf.mistgate.bak` (when `/etc/resolv.conf.mistgate.bak.link` exists, the original was a symlink to the path written in it).

`mistgate-node cleanup-net` can be run at any time. It removes the AmneziaWG and WARP interfaces, the WARP routes and rules and their nftables tables; the agent creates them again when it starts.

## When enrollment fails

| You see | Cause | What to do |
|:--|:--|:--|
| `enroll: --panel, --sni, --ca-sha256 and --token are required` | The command was cut while pasting. | Copy it again with **Copy the command**. |
| `--ca-sha256 must be 64 hex digits` | The same. | The same. |
| `enrollment token unknown, expired or used` | The command is older than an hour, was already used, or a newer one was made for this node. | **New install command** on the node's page. |
| `too many failed attempts, try later` | Ten failed attempts within a minute from this address. | Wait a minute and use a fresh command. |
| `node retired` | The node was retired. | Add a new node. |
| `no certificate in the chain matches the pinned CA fingerprint`, `panel did not present its CA certificate` or `the CA returned by the panel does not match --ca-sha256` | Something other than the panel answers TLS at `--panel`: a CDN or reverse proxy in front of it, a wrong address, or a panel whose data directory was recreated after the command was made. | Point the panel's DNS straight at the panel server, check `--public-url` and `--agent-addr`, make a new command. |
| A timeout or `connection refused` | The panel is down, a firewall blocks TCP 443 to the panel, or the address in the command is wrong. | Check the panel and its firewall; test with `curl -I https://panel.example.com/` from the node. |
| `already enrolled; use --force to replace the identity` | The state directory already holds an identity. | Add `--force` after `enroll`, or remove `/var/lib/mistgate-node` first. |
| `install: must run as root` | Not root. | Run the command as root. |
| `holds no identity: run mistgate-node enroll first` | `enroll` failed, or `install` got another `--state-dir`. | Run `enroll` again with the same state directory. |
| The admin answers **Create install command** with "panel address is not configured" | The panel knows no address for agents: setup ran without `--public-url` and `serve` has no `--agent-addr`. | Restart the panel with `--agent-addr panel.example.com:443`. |

More symptoms and fixes are in [Troubleshooting](../operations/troubleshooting.md). Day-to-day node settings are in [Nodes](../guide/nodes.md).
