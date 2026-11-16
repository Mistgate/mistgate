---
title: Own probes
description: A router or a server you control inside a country checks your nodes from its own network; what a Keenetic needs, one-line install, check, update, remove and revoke, what the probe does and does not do on a router, and what its provider sees.
---

The public checks of [Reachability](reachability.md) look at your nodes through other people's probes. An **own probe** is a small script that you (or a friend) run on a router or a server inside the country: it checks your nodes from that network, which is the view of a real home or mobile connection. Its points join the other sources in each node's verdict. One probe is one narrow point, so on its own it can only raise a *partial* alert, as a hint.

It is **off by default**, and the Cloudflare edition has none. It is the owner's: the list, the install commands and every change are the owner's, and creating, revoking, switching and disconnecting ask for a fresh confirmation. API tokens and the MCP cannot reach it.

## In the admin

**Reachability → Sources → Own probes.** The switch turns the source on; the list shows each probe with its state:

- **Waiting for install**: the command was made and not run yet (it works for 24 hours; after that make a new one).
- **Works**: it asked the panel within the last hour. The row says when it was heard from, its operator and the city you typed, and how many checks it made in 24 hours.
- **Not heard from**: no contact for more than an hour (a router that is off, a flash drive that was pulled out, no network).
- **Network changed**: the probe now asks from another operator than the one it was installed on. It is paused until you press **Confirm network**: a probe moved to a place you do not know is not a probe you chose.
- **Outdated**: the panel has a newer script than the probe runs. Press **Update command**.
- **No filtering on its path**: the probe's reference address, which a filter blocks, opened. A probe on such a network (a router abroad, for example) sees no blocks, so a node it reaches proves nothing and its "reachable" counts as unsure. This is the right state for a probe outside the country, and it is shown before the first point appears.
- A row can also say that the **reference address could not be tried** (the probe has no network of its own, or `ref=off` is set in its configuration): its points prove nothing then.

**Check now** asks the probe for a round at its next contact, within about 15 minutes. **Revoke** is permanent; **Disconnect** at the bottom switches the source off and keeps the probes (they wake up when you switch it on).

## What it needs

A **Keenetic** router needs:

1. The KeeneticOS components **Open package support (OPKG)** and **Ext file system**, installed in the router's web interface.
2. **Entware**: on a USB flash drive formatted ext4 (1 GB is enough), or in the built-in memory of models that have NAND, on KeeneticOS 3.7 or later. The router's architecture does not matter, the probe is a shell script.
3. **SSH into Entware** (port `222`, not the router's own SSH). Change the default password of the Entware `root` account first.

A **server** needs a systemd Linux with `curl`, `sha256sum` and `sudo`.

The panel needs its public HTTPS address (**Settings**): the command is built from it, and the panel refuses to make one without.

## Install

1. **Add a probe**: its name (how you will know it in the list), the city (typed by you: the panel keeps no address of the device) and where it stands: *Home*, *Mobile* or *Data centre*.
2. The window shows **one command**, once. It is valid for 24 hours and cannot be shown again; if you did not copy it, make a new one (the waiting probe is revoked and a new one made). Switch between **Keenetic (Entware)** and **Server (VPS)**: they are two commands.
3. Open SSH on the device, paste the command and press Enter. The window waits for **Connected**.

The command downloads the script from your panel, **checks its sha256** against the one the panel computed, and only then runs it with the one-time token. On a Keenetic it first installs `curl`, `ca-bundle` and `coreutils-sha256sum`; on a server the script goes to a private temporary file and runs through `sudo` (as root, drop the `sudo`). Running it again is safe: an already registered probe is refreshed.

The script registers itself with the token (the token is sent in the request body, never in a command line), keeps its secret in a file closed to other users, and starts. On a Keenetic it becomes `/opt/etc/init.d/S99mistgate-probe`, so it starts with Entware; on a server it is a systemd service `mistgate-probe`.

## Check

- On the device: `/opt/etc/init.d/S99mistgate-probe status` (Keenetic) or `systemctl status mistgate-probe` (server).
- In the panel: the probe is *Works* with the right operator; **Check now**, and the points appear in the node rows of Reachability within about 15 minutes.

After a reboot of the router Entware and OPKG stay, and the probe starts on its own. `/tmp` is empty after a reboot, which is normal. Pull the flash drive out and the probe says nothing (the panel shows *Not heard from*); put it back and it wakes up.

## Update

When **Outdated** shows, press **Update command**. The panel makes the command: the same download and sha256 check as the install command, then `upgrade`, with no token in it. Pick **Keenetic** or **Server**, run it on the device. The secret and the settings stay, and the probe restarts.

## Remove

On the device: `sh /opt/sbin/mistgate-probe uninstall` (Keenetic) or `sh /usr/local/sbin/mistgate-probe uninstall` (server). It removes the program, the service, the configuration and the state.

**Revoke** in the panel works without the device: both of the probe's credentials stop working at once, its points leave the node rows, and a probe that is still running is answered with the decoy page and goes silent. Revoke it before you remove it, or when you cannot reach the device.

## What the probe does, and does not, on a router

**It does:**

- Asks the panel what to check, usually every 12 to 18 minutes. It measures only as often as the panel's interval says (30 to 240 minutes, 60 by default), never more often than every 30 minutes.
- In a round, checks up to 32 addresses of your nodes, two connections at a time, at most 20 connections a minute, 600 a day and 64 different addresses in a day. A connection is a TLS handshake or a page of at most 64 KiB. The report holds times and codes ("reset", "timeout"), never addresses.
- Sleeps between asks: no wake-ups in between. It runs as two shell processes (a watcher and a worker) with the lowest priority, under the user `nobody` when there is one, and skips a round when less than 16 MB of memory is free.
- Writes only to memory (`/tmp/mistgate-probe`). Its configuration is in `/opt/etc/mistgate-probe/conf` (closed to other users) and is not written after the install. Nothing goes to the flash drive or to a log on it; syslog gets short lines without secrets.

**It does not:**

- Open a port: no listening socket of any kind.
- Touch the router's settings: firewall, routes, DNS, Wi-Fi, schedules, passwords. It installs no packages itself (the one install line does, when you run it).
- Run anything the panel sends: the answer is read as text, every line is checked against a strict pattern, and one wrong line throws the whole answer away. No `eval`, no loading of files, no self-update (an update is the command you run).
- Go anywhere but your panel and the addresses of the round; it follows no redirects and takes no proxy from the environment.

**The numbers.** They were measured in a lab under busybox `ash` on x86; a real router will differ (many are 32-bit and smaller, but this is only known from a real one: `grep -E 'VmRSS|Pss' /proc/<pid>/status` for the watcher and the worker). A target is what the script was built for.

| | Target | Measured in the lab |
|---|---|---|
| Script size | up to 32 KiB | 31.8 KiB |
| Memory, idle | up to 2 MB | 2.3 MB (PSS) |
| Memory, at the peak of a round | up to 12 MB | 10.8 MB (PSS) |
| Processor | a small share | about 3 s a day (0.003 %), none while idle |
| Traffic | about 150 MB a month | about 180 MB a month, with two targets that read a page |
| Writes outside memory | none | none |
| Listening ports | none | none |

## What the provider sees

For the person who agrees to host a probe, in plain words:

- About every 15 minutes the device makes an ordinary HTTPS request to your panel's domain (it looks like any site), and about once an hour it opens short connections to the addresses of your servers and to one address that has long been blocked: a handshake and at most 64 KB of a page.
- Nothing of the household's traffic goes through the probe, and the probe reads none of it. It does not touch DNS.
- To the provider it looks like someone using a VPN. If it ever acts against VPN users, this connection may count too.
- One command removes it. Ask the person first, and do not install it for someone who cannot afford any attention.

## What the panel keeps

The probe's name and city (yours), its operator and ASN (found from the address of a request, which is then dropped), the version of its script and the time of its contacts. Never the address of the device, and the panel stores only a hash of the token and of the secret.
