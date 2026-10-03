---
title: Hysteria2
description: Every Hysteria2 setting in the profile editor, how the node handles the traffic, and what the apps receive and need.
---

Hysteria2 is a QUIC-based protocol over UDP. In Mistgate it is the protocol of the subscription apps: a person adds one link and gets every Hysteria2 server of their group. Each person has a secret token of their own; the node only knows its hash. This page lists the settings of a Hysteria2 profile and what they do; how a profile is made and put on nodes is on [Profiles](profiles.md).

## Settings

The editor groups the settings into **Basics**, **Obfuscation** and **Advanced**. Critical settings break the servers people already have until their app updates the subscription; the editor asks before it saves them (see [Profiles](profiles.md)).

| Setting | Default | Critical | What it does |
|:--|:--|:--|:--|
| **Port** | 443 | yes | The UDP port the node listens on. A node can use another one for itself. |
| **Port hopping** | off (0 and 0) | yes | A UDP range the node forwards to the port. See below. |
| **Domain (SNI)** | empty | yes | The server name in the certificate, the one the app sees. Empty: the node address. |
| **Certificate** | Let's Encrypt | yes | **Let's Encrypt** or **Self-signed (pinned)**. See [Profiles](profiles.md). |
| **Obfuscation** | Salamander | yes | **None**, **Salamander** or **Gecko (experimental)**. |
| **Obfuscation password** | generated | yes | 8–128 characters, the same on the node and in the app. |
| **Masquerade** | Built-in site | no | What the node answers to anything that is not a Hysteria2 client: **Built-in site** or **Nothing (404)**. |
| **Ignore the client-declared speed (always BBR)** | on | no | See "Speed and fairness". |
| **BBR profile** | Standard | no | **Standard**, **Conservative** or **Aggressive**. |
| **Upload ceiling** | 0 (unlimited) | no | Mbit/s, up to 100000: how fast one client may send to the node. |
| **Download ceiling** | 0 (unlimited) | no | Mbit/s, up to 100000: how fast the node may send to one client. |
| **Relay UDP** | on | no | Off: clients can tunnel TCP only. |
| **Exit** | Direct | no | **Direct** or **WARP**. See [WARP](warp.md). |

A new profile always starts with a generated Salamander password and with the client-declared speed ignored.

## Obfuscation

Salamander wraps every packet so that the QUIC handshake does not look like QUIC; the node and the app share the password. Gecko is another obfuscation of the same Hysteria2 core and is marked experimental. With **None** the traffic is plain QUIC.

Changing the type or the password breaks the server for everyone until their app updates the subscription. **Generate** makes a new password.

## Masquerade

Anything that connects without being a Hysteria2 client sees a web site. With **Built-in site** the node serves a neutral built-in site over HTTP/3 on the UDP port and over HTTPS on TCP 443 next to it, and advertises the UDP port as HTTP/3, like a real site does. With **Nothing (404)** every such request gets a plain 404.

TCP 443 is also where the node answers Let's Encrypt, so keep it free on nodes with Let's Encrypt profiles.

## Speed and fairness

A Hysteria2 client can declare its own speed and ask the server to send at that rate (Brutal). One client that declares a huge speed can then take the whole link of the node. With **Ignore the client-declared speed (always BBR)** on, the node ignores the declared speed and uses BBR congestion control, which shares the link fairly. **BBR profile** tunes how fast BBR ramps up.

**Upload ceiling** and **Download ceiling** cap each client of the profile. A per-person limit is set on the user instead: **Speed limit** on the user card (experimental; it applies to Hysteria2). See [Users and groups](users-and-groups.md).

## Port hopping

With a range set, the node forwards every UDP packet that arrives on a port of the range to the profile's port, so a client can change ports during a connection and a filter that blocks one port does not stop it. The node does it with a firewall redirect.

Rules:

- the range starts at 1024 or above and holds at most 20000 ports;
- the profile's port must be outside the range;
- the range must not overlap the port or the range of another profile on the node; the panel refuses that before the click;
- the node also refuses a range that would cover its SSH port or another profile's port. The profile itself listens on its port, but the range is not installed: the profile is reported with the error "port hop rejected: …", and the node's events say "a profile refused its port hopping".

> **Note:** only the Mihomo format carries the range: apps on the mihomo core get the ports and a hop interval of 30 seconds. The link list (Happ and other apps that read share links) carries the single port. A WARP twin of the profile has no hopping.

## Domain (SNI) and certificates

The domain, the certificate and how they interact with the node address are described in [Profiles](profiles.md). In short: Let's Encrypt needs a domain whose A record points to the node and ports 80 and 443 reachable; a self-signed certificate works on an IP, and the app checks its fingerprint.

## How the node handles the traffic

- **Sniffing.** The node reads the destination name from the first bytes of a connection (the HTTP Host or the TLS server name) and uses that name instead of the IP the app sent. The app's own DNS answer is not trusted, so a poisoned or blocked lookup on the person's side does not matter.
- **Per-node DNS.** The node resolves those names itself, with the resolvers in the node's **DNS for user traffic** setting; empty means the server's own resolver. IPv4 is preferred; IPv6 is used for a name without an A record. A WARP exit resolves the same way. See [Nodes](nodes.md) and [DNS](dns.md).
- **Closed destinations.** A user cannot reach private networks, loopback, link-local and cloud metadata addresses, or any address of the node itself through the tunnel.
- **Users.** Adding, removing or disabling a person changes only the node's list of tokens: nobody else is disconnected. A removed or expired person is cut off at their next packet.

## What the apps receive

Two formats carry Hysteria2; which one an app gets is decided by the rules on [Subscriptions](subscriptions.md).

**Link list.** One share link per server, read by Happ and most other apps:

```ini
hysteria2://<token>@de1.example.com:443/?obfs=salamander&obfs-password=<password>&sni=de1.example.com#<server name>
```

- `obfs` and `obfs-password` are there when obfuscation is on;
- `sni` is there when the server name is a domain;
- for a self-signed certificate the link carries `insecure=1` together with `pinSHA256=<64 hex digits>`: the app accepts only the certificate with that fingerprint;
- `#<server name>` is the name from the template, URL-encoded.

**Mihomo YAML.** Apps on the mihomo core (Clash Verge, FlClash and others) get one proxy per server:

```yaml
- name: "🇩🇪 Germany · Hysteria2"
  type: hysteria2
  server: "de1.example.com"
  port: 443
  ports: "20000-30000"
  hop-interval: "30"
  password: "<token>"
  obfs: salamander
  obfs-password: "<password>"
  sni: "de1.example.com"
  skip-cert-verify: false
  alpn: [h3]
```

`ports` and `hop-interval` are there only with port hopping. For a self-signed certificate the proxy carries `fingerprint` with the pin instead of a domain check.

## What the apps need

- An app that reads Hysteria2 share links (Happ and similar) or an app on the mihomo core (Clash Verge, FlClash).
- UDP to the port (and to the hop range, if any) must reach the node. If the hoster cuts it, the client-eye checks fail with a timeout and the node turns **No traffic**; change the port. See [Health](../operations/health.md).
- For a self-signed profile, the node must have started it once: the pin is reported by the node. Until then the link carries no pin and apps refuse the certificate, and the Mihomo format leaves that server out.

> **Note:** the panel says a self-signed certificate is verified with the official Hysteria2 client and with Mihomo, and not yet with Happ. Check it on your phone first.
