---
title: MTProto (Telegram proxy)
description: "The MTProto profile: a proxy for the Telegram app only, behind Fake TLS. When it helps and where it does not, the two choices of the name in the handshake, adding a profile, giving a person the link, re-issuing and revoking the key, relays, what the health check proves, and what is not built yet."
---

MTProto is Telegram's own proxy protocol. A Mistgate MTProto profile makes every node it runs on a proxy server for the **Telegram app and nothing else**: the person taps a link, Telegram adds the proxy to its settings, and Telegram's traffic goes through the node. It is not a VPN: other apps do not use it, and no VPN app reads it. This page lists the settings of an MTProto profile and how a person gets the link; how a profile is made and put on nodes is on [Profiles](profiles.md).

> **Note:** MTProto is **experimental**. It is the first version of the protocol in Mistgate and has not been proven in the field yet; the panel marks it as experimental where you pick the protocol. Settings and behaviour may still change.

## When it helps, and its limits

Use it where Telegram is blocked or throttled but the rest of the internet is not, or as a lighter second way to reach Telegram next to a VPN. The proxy speaks Fake TLS: to anyone who watches the connection it starts like an ordinary TLS 1.3 handshake with a site, and the node answers everyone who is not a client with a real site (see [The name in the handshake](#the-name-in-the-handshake)).

What it cannot do:

- **It may not work in Russia.** Since spring 2026 MTProto proxies have been detected and blocked in waves there. A proxy can work for a week and then stop. If your people are in Russia, expect that and keep a VPN profile (Hysteria2, VLESS) as the main way.
- **A green check does not prove it works for your people.** The panel's checks run from the panel's server, outside any country. They show that the node does its job, not that a network inside a censoring country lets the connection through. Try the link on the networks that matter before you promise anything.
- **A careful prober can still tell the fake from a real site.** The node's answer to a Fake TLS hello is built to look like a TLS 1.3 server, but a scan that compares it with the real flight of a real site can see the difference. This is why strangers are sent to a real site, and why no setting makes the proxy invisible.
- **Telegram only.** A tap on a link changes only Telegram's proxy setting. No other app goes through it.
- **No middle proxy.** The node connects straight to Telegram's data centres. Telegram's promoted channel (the sponsor tag) is not available; see [What is not built yet](#what-is-not-built-yet).

## The name in the handshake

A Telegram proxy secret carries a domain: Telegram puts it into the TLS handshake as the server name. The node accepts only a handshake with that name and the person's key, and sends everyone else, other names, wrong keys, replayed hellos, plain HTTP, browsers and scanners, to a site. You choose the domain per profile, in the setting **Name in the TLS handshake**:

| | This node's name | Another site |
|:--|:--|:--|
| The domain | The node's own host name (or the **Domain (SNI)** set for the profile on that node) | A real site you name, for example `www.example.org` |
| What a stranger sees on the port | The node's own decoy site, the one Hysteria2 and VLESS show, with the node's certificate | The real site: the node passes the connection on to it, so the prober completes a real handshake with that site and sees its certificate |
| Let's Encrypt on the node | Keeps working, also on TCP 443: the validation is answered on this port | On TCP 443 the node's own site and that validation are not answered here, so the node's certificate renews over port 80 |
| A node with only an IP address | Not possible: there is no name to use | Works |
| Its weak side | The decoy site must look natural for the node's name | The domain does not belong to the server's IP address, which some networks notice |

How to choose:

- Start with **This node's name** on a node that has a domain. Strangers get the same decoy site as on the node's other protocols ([Your own decoy site](decoy-site.md)), and nothing else changes on the node.
- Choose **Another site** for a node known only by its IP address, or when you want strangers to meet a big, well-known site. Pick a site that is fast and not blocked where your people are, and that has nothing to do with you. **Do not use the domain of the panel**: anyone who probes the node would then see the panel's address.
- With **Another site** on TCP 443, keep port 80 of the node reachable, or its certificate cannot renew. The profile editor warns about this, and about the IP mismatch.
- A site you name must be a plain host name (`www.example.org`): no IP address, no port, no path.

## Settings

The editor shows the settings under **Basics**. Critical settings change the links people already have, so people must add the new link again; the editor asks before it saves them (see [Profiles](profiles.md)).

| Setting | Default | Critical | What it does |
|:--|:--|:--|:--|
| **TCP port** | 443 | yes | The TCP port the node listens on, 1 to 65535. |
| **Name in the TLS handshake** | This node's name | yes | **This node's name** or **Another site**. See above. |
| **Site** | empty | yes | Another site only: the host name of that site. |

On a node the profile can have its own **Port**, and, with **This node's name**, its own **Domain (SNI)**, which is then the name in the handshake.

MTProto is a TCP profile. It cannot share a port with another TCP profile of the same node, VLESS for example; if the node already has VLESS on 443, put MTProto on another port. Telegram accepts any port. The built-in site that Hysteria2 shows on TCP 443 gives way to MTProto, as it does to VLESS ([VLESS and Hysteria2 on one node](vless-and-hysteria2.md)); with **This node's name** the visitor of that port still sees the node's site.

The node's clock matters here. The handshake carries the client's time, and the node accepts it within about five minutes of its own clock. A node whose clock is off refuses every client; the doctor's **Clock** check warns about it (see [Health](../operations/health.md)).

## Add a profile

1. Open **Profiles** and press **New profile**. The first step is the choice of a protocol: pick the **MTProto** card. The second step is the form.
2. Set the **TCP port** and the **Name in the TLS handshake**; with **Another site**, type the **Site**.
3. Give the profile a name and say where it goes: put it on nodes, and add it to a group. A person gets Telegram only from a group that carries an MTProto profile on a node they may use.
4. **Create profile**.

If a node has only an IP address and the profile uses **This node's name**, the node refuses it with the reason: enter a domain for the profile on that node, or choose **Another site**. The panel on the right shows the link a person would get, with the key hidden.

## Give Telegram to a person

Telegram is a switch on the person, next to the subscription link and AmneziaVPN keys, in the user form and on the user card. It is off by default.

- Turning it on makes the person's **Telegram key** at once: one key per person, the same on every MTProto profile and node they may use.
- The Telegram switch comes **in addition** to Happ or AmneziaVPN, not instead of them. At least one of those two stays on.
- The key is listed among the person's devices as **Telegram proxy**, but it takes no place in the device limit, so it never fails for want of room. Turning the switch off, or deleting the person, revokes it.
- A person gets links only while they are active, and the group has an MTProto profile on a node they may use. A disabled, expired or over-quota person loses the proxy with the rest of their access.
- **No link goes into a subscription.** VPN apps would show a `tg://` line as a broken server, and the Telegram app reads no subscription. The link comes only from the person's page or from the admin.

## Where the person finds the link

- **Their own page.** A block **Telegram proxy**, under the servers, when Telegram is on for them. Each link is named by the server's country and place, never by the node's name in the panel, and has **Open in Telegram** and **Copy link**. Until the proxy is ready the block says so. See [User page](user-page.md#telegram-proxy).
- **The admin.** On the person's page in **Users**, the block **Telegram proxy** lists the links with **Open in Telegram**, copy and a QR code, and the buttons **Re-issue** and **Revoke**. Send the link, or show the QR code, over any channel you trust.

A link looks like this, with the key and the name in hex:

```ini
tg://proxy?server=de1.example.com&port=443&secret=ee<key><name>
```

The link is the key: anyone who has it can use the proxy. Treat it like a password. Telegram asks the person to confirm before it uses the proxy.

## Re-issue and revoke

- **Re-issue** gives the person a new key. Every link made before stops working at once, on all nodes, and open connections on the old key are closed. The new links appear in the block; the person adds them in Telegram. Use it when a link leaked.
- **Revoke** takes the key away. The links disappear, open connections are closed, and Telegram stays switched on for the person, with no key. A new key can be made from the same block.
- Turning the Telegram switch off, or removing the **Telegram proxy** device, revokes the key too.

One key holds up to 64 connections at once. Telegram opens about 10 to 15 per account at the busiest, so a leaked key cannot carry more than a handful of people. A connection with no data for 10 minutes is closed.

## Relays and IPv6 twins

A person's Telegram links follow the same rules as other servers (see [Subscriptions](subscriptions.md#extra-servers-over-ipv6-and-through-a-relay)), with the same labels:

- if the node has an [IPv6 entry](nodes.md#ipv6-entry), there is a second link at its IPv6 address, marked **IPv6**;
- for each working [relay](relays.md) of the node there is another link through the entry node, marked **via RU** (the entry's country). The relay passes the connection on unchanged, so the key and the name in the handshake stay those of the exit.

## What the health check proves

The panel checks every MTProto profile as Telegram would use it, with a hidden key of its own that belongs to no person ([Health](../operations/health.md#mtproto)):

1. it opens a TCP connection to the profile's port;
2. it completes the Fake TLS handshake with the name and a key;
3. it asks Telegram's data centre 2, through the proxy, for a first message (`req_pq_multi`) and checks that a valid answer comes back.

A green cell therefore proves that the node accepts a client, can reach Telegram, and that Telegram answers through it. It does not prove that a network inside a country that blocks MTProto lets the traffic through. When it fails, the cell says at which step.

## What is not built yet

- **A middle proxy and the promoted channel.** The node talks to Telegram's data centres directly. Telegram's own proxy configuration, and with it the sponsor tag that proxies can show, are not supported.
- **Speed limits per person and per device.** The limits on the user card apply to Hysteria2 and VLESS, not to the Telegram proxy.
- **People with Telegram only.** Telegram goes together with Happ or AmneziaVPN; a person cannot have the proxy and nothing else.
- **Shaping the fake answer after the real site.** The node does not yet measure the real handshake of the site and copy its shape.
