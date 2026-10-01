---
title: AmneziaWG
description: AmneziaWG 3.1 and 2.0 profiles, their parameters and mimicry looks, the userspace and kernel backends of a node, and the per-device keys people import into AmneziaVPN.
---

AmneziaWG is WireGuard with obfuscation: the same fast tunnel, with packets that do not look like WireGuard on the wire. Unlike Hysteria2, every device holds its own key: a person gets a key per phone or computer and imports it into AmneziaVPN (or another AmneziaWG app). This page covers the AmneziaWG profile, the node's backend and the devices; how a profile is made and put on nodes is on [Profiles](profiles.md).

## One profile, one version, one port

- A profile is one AmneziaWG interface on one UDP port, with one protocol version.
- A device (a key) belongs to one profile and works on every node that runs it. Its config differs between those nodes only in the address and the node's public key.
- Every node has its own server key for the profile: a compromised node does not give away the others.
- A person who needs both versions, or a profile with WARP and one without, gets one device per profile.

## Protocol versions

| Version | For | What it has |
|:--|:--|:--|
| **AmneziaWG 3.1** | Current clients (the default) | Header protection, random trailers, tunable timers, everything of 2.0. |
| **AmneziaWG 2.0** | Older clients only | Junk packets, padding, header ranges, signature packets. The 3.1 protections are off. |

Older versions (1.5, 1.0) are not offered. Switching the version builds a new obfuscation block for it (the 3.1 fields are refused on 2.0), so it is a critical change.

The editor shows the **Oldest client that works** for the chosen version. An older client does not refuse a config, it drops the keys it does not know, and the handshake then fails without a word.

| Version | Oldest client |
|:--|:--|
| 3.1 | AmneziaVPN 5.0.1.5, AmneziaWG Android v3.1.20260814, AmneziaWG Windows 3.1.0, AmneziaWG Apple v3.1.3, Mihomo v1.19.30 |
| 2.0 | AmneziaVPN 4.8.12.9, AmneziaWG 2.0.0, Mihomo v1.19.14 |

The same list is shown next to every config a person gets.

## The profile editor

Above the usual form, an AmneziaWG profile has two cards of its own.

**Protocol version** holds the version choice, the oldest clients, and **Paste .conf**: paste a config from AmneziaVPN or another AmneziaWG client, and the form takes its version, MTU and obfuscation values. This happens in the browser; the keys of the pasted config are neither used nor stored. A 1.5 or 1.0 config switches the form to 2.0; a plain WireGuard config has nothing to take.

**Look of the first packets** decides what a handshake looks like to an observer:

| Look | Looks like | Normal port |
|:--|:--|:--|
| **QUIC** | A QUIC Initial. A large first packet: about 2.4 KB of config, too big for a QR code. | 443 |
| **QUIC (curl)** | The curl flavour of QUIC, same size. | 443 |
| **DNS** (the default) | A DNS query. | 53 |
| **STUN** | A binding request, as in calls. | any |
| **WebRTC** | STUN with ICE attributes. | any |
| **SIP** | A SIP message. | 5060 |
| **NTP** | A 48-byte time request. | 123 |
| **RTP** | A voice stream packet. | any |
| **SSDP** | A device discovery search. | 1900 |
| **DTLS** | A DTLS hello. | any |
| **Custom** | The packets you write in I1–I5. | any |

A look on a port where that traffic is not normal stands out, and the editor says so. Picking a look changes only the signature packets I1–I5, which the client sends before a handshake and the node ignores: no issued config breaks.

- **Domain in the packets**: the host name that QUIC, DNS and SIP packets carry. Empty: every device draws its own from a built-in list (**Random**, **List**).
- **A signature of its own for every device**: each device gets different signature packets of the look, the same every time its config is shown. Off: all devices share one set. STUN, WebRTC and DTLS are the same bytes for everyone, so the switch changes nothing for them.
- **New values for the whole block** replaces junk, S, H, timers and the header protection key with fresh random values. Clients then need new parameters, so saving reissues the configs of the devices.
- **Disguise N/100** rates the settings, not a censor: a high number means no known tell is left in them. A click shows the starting point of the version and each reason it lost or gained points.

## Settings

Critical settings must match on the node and in every client; changing them makes every device of the profile outdated (see [Profiles](profiles.md)).

**Basics**

| Setting | Range and default | Critical | What it does |
|:--|:--|:--|:--|
| **UDP port** | 1–65535; a new profile gets a random one in 10000–60000 (never 51820, 51821 or 55424) | yes | The node listens here. **Random port** draws another. |
| **MTU** | 1200–1420, default 1280 | no | Tunnel MTU on the node and in the `.conf`. MTU + S4 + 80 must fit in 1500. |
| **Exit** | **Direct** or **WARP** | no | See [WARP](warp.md). |
| **Client network (IPv4)** | a private /16–/24 network | yes | The node is .1, each device gets one address. Cannot change once the profile is on a node or has devices. |
| **Client network (IPv6)** | a /64 from fc00::/7, or empty | yes | Empty means no IPv6 inside the tunnel. Fixed like the IPv4 network. |

**Obfuscation**

| Setting | Range | Critical | What it does |
|:--|:--|:--|:--|
| **Junk packets (Jc)** | 0–128 | no | Sent by the client before each handshake; 4–12 is usual. |
| **Junk size, min (Jmin)** / **max (Jmax)** | 0–1280 bytes | no | Keep Jmax below the MTU. |
| **Init padding (S1)**, **Response padding (S2)** | 0–150 bytes | yes | Bytes added to the handshake messages. |
| **Cookie padding (S3)**, **Data padding (S4)** | 0–64 bytes | yes | S4 is added to every data packet: keep it small. |
| **Init header (H1)** … **Data header (H4)** | a number or a range lo-hi | yes | Message type values. 1, 2, 3, 4 are plain WireGuard. The four ranges must not overlap. |
| **Signature packet 1 (I1)** … **(I5)** | up to 3500 characters each | no | Tags b, r, rc, rd, t. Filled by the look; typing here makes the look **Custom**. |

**Advanced** (the 3.1 fields are ignored on 2.0)

| Setting | Critical | What it does |
|:--|:--|:--|
| **Signature seed** | no | Not a secret. With the device id it makes the per-device packets reproducible; a new seed changes them the next time a config is shown. |
| **Header protection key** | yes | 3.1 only. The same 32 bytes on the node and in every client. Stored encrypted. |
| **Random trailers** | yes | 3.1 only. Must match on both sides. |
| **Turn off cookie replies** | no | 3.1 only, node side. On removes the protection against handshake floods. |
| **Extra padding** | no | 3.1 only. A range of random bytes added to data packets. |
| **Rekey after**, **Rekey timeout**, **Reject after**, **Keepalive timeout**, **Handshake attempts** | no | 3.1 only. Timers, a number or a range. |
| **Persistent keepalive** | no | Written into the client; keep it under 30 s, the UDP timeout of many NATs. 2.0 takes one number; Mihomo uses the first one. |

While you edit, the editor warns about legal but risky values: too many junk packets, Jmax not below the MTU, S4 that does not fit 1500 at this MTU (with the MTU that would fit), H values of plain WireGuard, a narrow H range, cookie replies turned off, a keepalive longer than a NAT timeout, a look on an unusual port.

## Client networks and addresses

Each AmneziaWG profile has its own client networks, and the networks of two AmneziaWG profiles must not overlap. The panel's slots are `10.66.4.0/22` with `fd66:66:0:1::/64`, then `10.66.8.0/22` with `fd66:66:0:2::/64`, and so on: `10.66.0.0/16` holds 63 of them. A profile created without naming its networks (through the API, or as a twin) takes the first free slot. The editor fills in the first slot; if another profile already has it, the field says "overlaps the network of profile …": type a free network, for example the next slot.

The node takes `.1`; every device gets the next free address of the profile. When the network is full, a new device is refused ("The profile's client network is full"); remove unused devices or use another profile. Once the profile is on a node or has a device, its networks are fixed: every issued config holds an address from them.

## The node's backend: userspace or kernel module

A node runs AmneziaWG in one of two ways, one setting for all its AmneziaWG profiles. It is the **AmneziaWG backend** card in the node's **Settings**, and the first AmneziaWG profile put on a node asks the same question.

| Backend | What it is |
|:--|:--|
| **Auto** | The kernel module when it is already loaded, else userspace. The node installs nothing. |
| **Kernel module** | Faster. Needs the `amneziawg` module on the host; without it the AmneziaWG profiles of the node do not start. |
| **Userspace** | `amneziawg-go` inside the agent. Works anywhere with `/dev/net/tun`, a bit slower. |

A change applies at once; connected devices reconnect. **Running now** shows what the node reports, for example "userspace · amneziawg-go v3.1…".

### Preparing the kernel module

The module is built from source on the node, which installs packages as root. There are two ways.

**From the panel** (agents that can do it). Pick **Kernel module** and **Save**. The panel asks the node first:

- the module is already loaded: the setting just switches;
- it can be built: the window **Build the kernel module on …?** explains what happens. The node installs what the build needs (dkms on Ubuntu, make, gcc and the headers of its running kernel: a few hundred MB), builds and loads the module. It usually takes 1–5 minutes, with a hard limit of 15. AmneziaWG keeps running as before the whole time. When the module is ready, the node switches to it and connected devices reconnect once;
- it cannot be built here: the card says why and shows the manual command.

The card follows the build: "Preparing the module… (running N min)", then **Module ready**, or **Failed** with the reason and **Retry**. A failed build changes nothing: the node keeps the backend it had. The build's log is on the node:

```sh
journalctl -u mistgate-awg-prepare
```

**By hand**, on the node as root. Without `--yes` the command only prints its plan:

```sh
mistgate-node awg prepare-kernel          # print the plan, run nothing
mistgate-node awg prepare-kernel --yes    # run it (hard limit 15 minutes)
mistgate-node awg prepare-kernel --verify-only   # check a module that is already there
```

The build needs Ubuntu or Debian, a kernel version the node can tell, systemd, and Secure Boot off (the kernel refuses an unsigned module). It cannot work in a container (OpenVZ, LXC, Docker and the like): pick **Userspace** there. The add-profile dialog says whether a node is ready for the module, using the doctor's kernel headers check.

Only the owner can start a module build.

### The status line of a profile on a node

In the node's **Profiles** tab, each AmneziaWG profile shows what the node last reported: the backend, the number of devices, how many are online (a handshake in the last 190 s), how many never connected, and the last handshake. Two hints point at the cause when nobody gets through:

- "Not one packet reached the port": the hoster or a firewall blocks this UDP port. Try another port.
- "Packets arrive but no handshake completes": the obfuscation parameters on the node and in the config differ, usually an outdated config.

The doctor's **AmneziaWG backend** check covers the rest: no working backend, `/dev/net/tun` hidden by an old service unit (run `mistgate-node install` once with the current binary), the kernel module missing, or a host firewall (often Docker's FORWARD policy) that drops what clients send to the internet. See [Health](../operations/health.md).

## Devices and keys

A device is made by the admin in the user card, or by the person on their page when self-service is on (see [User page](user-page.md)).

In the user card, under **Devices** → **AmneziaVPN keys**, **Add device** opens **Add a device for …**:

- **Profile**, when the group has more than one AmneziaWG profile on a node;
- **Platform**: iPhone / iPad, Android, Windows, Mac, Linux or Other;
- **Name**, up to 40 characters; empty uses the platform name.

**Create** makes a key pair and a tunnel address, and the config window opens at once. A device counts against the person's device limit. The add is refused when the limit is reached, the user is disabled, AmneziaVPN is switched off for them, the profile is not in their group or not on any node they may use, or the client network is full.

**Show key** opens the same window for an existing device:

- a QR code of the `.conf` ("In AmneziaWG or AmneziaVPN: "+" → scan the QR code"); a config too long for a QR code says so;
- **Node**, when the profile runs on several nodes: one config per node;
- **Download .conf**, **Copy vpn:// key**, **Copy .conf text**;
- **Oldest client** and any notes about this config;
- **Replace key**: a new key pair for the same device and address. The old key stops working at once, and the new one has to be added to the app again;
- **Delete**: removes the device; its key stops working on every node.

Opening a config marks the device as having the current one: an **outdated** badge (after a critical change of the profile, or a port change on a node) goes away once the person has imported the new config.

> **Warning:** a config holds the device's private key. Send it only to its owner, and never paste it into a shared chat.

## vpn:// keys and .conf files

| | `.conf` file | `vpn://` key |
|:--|:--|:--|
| Imports into | AmneziaVPN and the AmneziaWG apps (also as a QR code) | AmneziaVPN |
| MTU | Kept | Desktop AmneziaVPN sets its own |
| Size | A QR code holds most looks; QUIC looks are too big | One line of text |

On a computer, import the file, not the key: with the key the MTU can be wrong and the connection may not work.

The config carries `AllowedIPs = 0.0.0.0/0, ::/0` (everything through the tunnel) and two DNS servers. They are the first two plain IPv4 servers of the person's DNS preset; when the preset has none, the built-in 1.1.1.1 and 8.8.8.8 are written and the window says so. AmneziaWG cannot split DNS: the main servers are used for every domain. See [DNS](dns.md).

## The AmneziaVPN app

On a phone: open AmneziaVPN, tap "+", paste the key (or scan the QR code), tap "Continue". On a computer: AmneziaVPN → "+" → "Connection settings file" → pick the downloaded `.conf`. The person's page gives these steps in their language, for their platform.

## AmneziaWG through the subscription

Apps on the mihomo core can carry AmneziaWG too. When a person has AmneziaVPN switched on and their app gets the Mihomo format, the subscription includes their AmneziaWG servers. A Mihomo app has no device identity, so all Mihomo apps of one person share one key per profile, made at the first fetch, and they count as the one subscription device in the limit. See [Subscriptions](subscriptions.md).
