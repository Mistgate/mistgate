---
title: TrustTunnel
description: "The TrustTunnel profile: a VPN over ordinary HTTPS with a real certificate, with official apps on every platform. What it does and does not give against blocking, what it needs (a domain, a port), the settings, the self-signed case, giving a person a tt:// key, revoking it, and what is not built yet."
---

TrustTunnel is a VPN that runs inside ordinary HTTPS (HTTP/2, and HTTP/3 if you switch it on) and uses a real certificate. To an outside observer the node's port is a website. People connect with the **official TrustTunnel apps**, which exist for every platform and for routers, or with the Mistgate app. The server side is Mistgate's own engine inside the node agent; no one else's program runs on the node. This page lists the settings of a TrustTunnel profile and how a person gets a key; how a profile is made and put on nodes is on [Profiles](profiles.md).

> **Note:** TrustTunnel is **experimental**. It is new in Mistgate and has not been proven in the field yet; the panel marks it as experimental where you pick the protocol. Settings and behaviour may still change. It is not available in the Cloudflare edition.

## What it is, and what it is not

It is a full VPN (TCP and UDP traffic, DNS) with an app of its own on iOS, Android, Windows, macOS, Linux and routers. The app imports a server from a `tt://` link or a QR code. The apps are listed in the Clients section of the README at <https://github.com/TrustTunnel/TrustTunnel>: App Store (iOS and macOS), Google Play (Android), and clients for Linux, macOS, Windows and routers.

What it does not do:

- **It does not promise to pass a block that other protocols do not.** On the test bench a flow over HTTP/2 froze after the first kilobytes, like any other TCP flow under the "freeze after 16 KB" rule; HTTP/3 (UDP) passed at the same time. So HTTP/2 is as exposed as VLESS over TCP, and HTTP/3 is the way around that rule, where UDP is not blocked.
- **It does not hide from traffic analysis.** The tunnel is TLS inside TLS, which a careful observer can tell from a real site. A real certificate and a real site make the port look ordinary at the first glance; they do not change how the traffic inside looks.
- **A green check does not prove it works for your people.** The panel checks from its own server, outside any country. Try it on the networks that matter before you promise anything.

Keep it as one more way to connect, mainly for the official apps and routers, next to Hysteria2 and VLESS ([Profiles](profiles.md)).

## What it needs

- **A domain and a public certificate**, for the `tt://` links of the official apps. The app checks the server's certificate the usual way, so the node needs a name and a Let's Encrypt certificate for it. Without a domain only the Mistgate app can connect (see [A self-signed certificate](#a-self-signed-certificate)).
- **A free TCP port**, 443 by default. It carries HTTP/2.
- **A free UDP port of the same number for HTTP/3**, if you turn HTTP/3 on. UDP 443 is usually taken by Hysteria2, so HTTP/3 is **off by default**. It starts only on a node whose agent is new enough; the panel does not send the profile to an older agent, and the server shows as not started.

## Settings

The editor shows the settings under **Basics** and **Advanced**. Critical settings change the links people already have, so the editor asks before it saves them (see [Profiles](profiles.md)).

| Setting | Default | Critical | What it does |
|:--|:--|:--|:--|
| **Port** | 443 | yes | The TCP port for HTTP/2, 1 to 65535. With HTTP/3 on, the same number is used on UDP. |
| **HTTP/3** | off | yes | Also listens on UDP at the same port. A `tt://` link is always HTTP/2; HTTP/3 is for the Mistgate app. |
| **SNI override** | empty | yes | The name of the certificate. Empty means the node's address, which must then be a domain. |
| **Certificate** | Let's Encrypt | yes | **Let's Encrypt**, or **Self-signed (pinned)**. See below. |
| **Exit** | Direct | yes | **Direct** or **WARP**: how the node reaches the sites people open ([WARP](warp.md)). |

Use your own domain for the SNI override, not a stranger's name: the apps check the certificate against this name, and Let's Encrypt issues it only for your own domain; a stranger's name with a self-signed certificate gives the node away. Picking a neighbour's name is only for the REALITY target of a VLESS profile, see [VLESS](vless.md#finding-a-site-near-the-node).

TrustTunnel is a TCP profile, so it cannot share a port with another TCP profile of the same node (VLESS, for example). The built-in site that Hysteria2 shows on TCP 443 gives way to TrustTunnel: the panel warns with `hy2_site_taken` ("The Hysteria2 site on this TCP port gave way to TrustTunnel"), the certificate of that site keeps renewing, and Hysteria2 itself is not touched.

## Add a profile

1. Open **Profiles** and press **New profile**. Pick the **TrustTunnel** card (it says it is experimental). The form opens.
2. Set the **Port**; switch on **HTTP/3** only if the node's UDP port is free. Leave **Certificate** on **Let's Encrypt**.
3. Give the profile a name, put it on nodes, and add it to a group. A person gets TrustTunnel only from a group that carries the profile on a node they may use.
4. **Create profile**.

A node with only an IP address is refused with `acme_needs_domain`: enter a domain for the profile on that node, or choose **Self-signed (pinned)**.

## A self-signed certificate

With **Self-signed (pinned)** the node makes its own certificate, and the Mistgate app checks it by its pin. The official apps cannot do that, so **no `tt://` link is made** for such a server: in the person's list its row says "needs a domain with a public certificate". Use it for a node that has no domain and serves the Mistgate app only.

## Give TrustTunnel to a person

**TrustTunnel** is a switch among the person's apps, in the user form and on the user card. It is off by default.

- Turning it on makes the person's key at once when the person is active. If they are not, or the key was revoked, the card has the button **Issue key**.
- The key is a device of the person and **takes a place in the device limit**, like an AmneziaVPN key and unlike the Telegram key. At the limit the key is refused with the device-limit error (see [Users and groups](users-and-groups.md#devices-and-the-device-limit)). A disabled person gets no key either.
- A disabled, expired or over-quota person has the key **suspended**: the links stop working and come back with the person's access.
- Turning the switch off revokes the key.
- **No `tt://` link goes into a subscription.** Happ, Mihomo and the others cannot read it. The links come from the person's page and from the admin. The Mistgate app gets the path by itself ([Client apps](client-apps.md#what-each-app-receives)).

## Where the person finds it

- **Their own page.** A block **TrustTunnel** with one `tt://` link for each server that fits, a QR code and **Copy link**. The person installs the TrustTunnel app, scans the QR code or opens the link, and the server appears in the app. See [User page](user-page.md#telegram-proxy).
- **The admin.** On the person's card, the block **TrustTunnel** lists the same links with a QR code, **Copy** and the buttons **Issue key** and **Revoke key**.

The link carries the person's name and password for the node, so it is the key: anyone who has it can use the VPN. Treat it like a password. A link is always HTTP/2.

## Revoke a key

**Revoke key** takes the key away. The links stop working, the node drops the credential with the next update, the place in the device limit is free again, and the switch stays on. **Issue key** makes a new one; the person adds the new link in the app. Use it when a link leaked.

## What a stranger sees on the port

The port answers like an ordinary HTTPS site: a `GET` returns the node's decoy site ([Your own decoy site](decoy-site.md)), the same as on Hysteria2 and VLESS. A tunnel request without the right password gets the 404 page of that site. A wrong password and an unknown user get the same answer, so a prober cannot learn which names exist.

## Relays, traffic and limits

- **Relays.** A [relay](relays.md) passes a TrustTunnel server on over **HTTP/2 only**: its twin is TCP only. A `tt://` link through a relay is marked "via RU" (the relay's country) and says HTTP/2 like every link.
- **Traffic and speed.** Traffic is counted per person and per device, and the speed limits of the user card apply, as for the other protocols.
- **Disabling a person** closes their connections within seconds and does not touch anyone else's.

## What is not built yet

- **Ping inside the tunnel and ICMP.** Neither works.
- **A tunnel over HTTP/1.1.** There is none; the apps use HTTP/2 or HTTP/3.
- **HTTP/3 through a relay**, and HTTP/3 in a `tt://` link.
- **A self-signed certificate in a `tt://` link.** It needs a domain and a public certificate.
- **The Cloudflare edition.** TrustTunnel is not offered there.
