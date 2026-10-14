---
title: VLESS REALITY
description: "The VLESS profile: REALITY on TCP and XHTTP, or TLS with XHTTP and WebSocket, direct or behind a CDN. When to choose it over Hysteria2, every setting, the REALITY target, ports, and what the apps receive and need."
---

VLESS is a TCP protocol. With REALITY the node answers anyone who is not a client with a real web site, the **target**: by default the node's own site, or another site you name, so a probe sees a genuine TLS handshake of a site that exists. A person with the right keys is recognised inside the same handshake and gets the tunnel. This page lists the settings of a VLESS profile and what they do; how a profile is made and put on nodes is on [Profiles](profiles.md), and how it shares a node with Hysteria2 is on [VLESS and Hysteria2 on one node](vless-and-hysteria2.md).

> **Note:** VLESS supports REALITY over TCP and XHTTP, plus XHTTP+TLS and WebSocket+TLS. TLS can connect directly with an ACME domain certificate or through a CDN.

## When to pick it

Hysteria2 and VLESS can sit in one subscription, and the app (or the person) uses whichever connects.

| | Hysteria2 | VLESS REALITY |
|:--|:--|:--|
| Carried over | UDP (QUIC) | TCP |
| Where it helps | Links with packet loss and long distance: QUIC recovers faster | Networks that throttle or block UDP and QUIC, which many mobile and office networks do |
| Certificate and domain | Let's Encrypt needs a domain; a self-signed certificate works on an IP | With the node's own site as the target (the default), a domain with a Let's Encrypt certificate. With another site as the target, nothing on the node: it works on a node known only by its IP |
| Port 80 | Used for Let's Encrypt | Not used |
| Cost on the node | Low | Higher: the node copies every byte in user space; TCP with Vision is the cheaper of the two VLESS transports |

Start with Hysteria2 and add VLESS for the people whose network gives them trouble, or try it with a few people first: see [Trying VLESS with a few people](vless-and-hysteria2.md#trying-vless-with-a-few-people).

## Settings

The editor groups the settings into **Basics**, **Obfuscation** and **Advanced**. Critical settings break the servers people already have until their app updates the subscription; the editor asks before it saves them (see [Profiles](profiles.md)).

| Setting | Default | Critical | What it does |
|:--|:--|:--|:--|
| **TCP port** | 443 | yes | The TCP port the node listens on. 1 to 65535, not 80. |
| **Transport** | XHTTP | yes | **XHTTP**, **TCP (RAW + Vision)**, or **WebSocket** with TLS. Vision is always on for TCP. |
| **Security** | REALITY | yes | **REALITY** or **TLS**. TCP with Vision is REALITY-only; WebSocket is TLS-only. |
| **Origin certificate** | Let's Encrypt domain | yes | TLS only: ACME domain certificate for a direct connection. A self-signed certificate is allowed only behind a CDN that accepts it. |
| **Origin server name** | node domain | yes | TLS only: the certificate name on the node. An inbound's Domain (SNI) override takes precedence. |
| **CDN host and port** | empty, 443 | yes | TLS only: clients connect to this endpoint and use the CDN host for both Host and SNI. The origin connection remains TLS. |
| **Site shown to everyone else** | This node's site | yes | **This node's site**: the node's own site with its own Let's Encrypt certificate. **Another site**: a real site you name. See below. |
| **Target host:port** | none | yes | Another site only: the site REALITY shows to everyone else, for example `www.example.org:443`. Required for another site, not shown for the node's own. |
| **Server names** | empty | yes | Another site only: up to 8 host names the target's certificate is valid for. Empty means the host of the target. Needed when the target is an IP address. No wildcards. |
| **Short IDs** | two, generated | yes | 1 to 8 hexadecimal values of an even length, up to 16 characters. Each device gets one of them. |
| **Client fingerprint** | Chrome | no | The browser the client's TLS handshake imitates: Chrome, Firefox, Safari, iOS, Edge or Random. |
| **XHTTP path** | generated | yes | Starts with `/`, up to 64 characters from letters, digits and `/ _ -`. A new profile gets a random one. |
| **XHTTP host header** | empty | yes | Empty: none. |
| **XHTTP mode** | Auto | no | **Auto**, **Packet up**, **Stream up** or **Stream one**. The client picks; the node accepts all of them. |
| **XHTTP padding** | 100-1000 | yes | The random padding added to each request, written `min-max`, from 1 to 4096 bytes. |
| **WebSocket path and host** | generated, empty | yes | TLS only. The server host is empty; the client uses the CDN host when configured, otherwise the optional WebSocket Host value. |
| **Exit** | Direct | no | **Direct** or **WARP**. See [WARP](warp.md). |

The XHTTP settings apply to the XHTTP transport only. Which server name and which short ID a device gets is fixed by the device, so its link does not change from one update of the subscription to the next.

## TLS and a CDN

Choose TLS with XHTTP or WebSocket for a direct connection or a CDN. A direct connection needs an ACME domain certificate. The node can answer ACME TLS-ALPN-01 on TCP 443 and HTTP-01 on TCP 80 when that port is available. The certificate is issued on demand and renews without restarting the inbound.

For a CDN, set its host and port. The client connects to that endpoint and sends the CDN host as both Host and SNI. The CDN connects to the node over TLS; Mistgate never configures a plain-HTTP origin. XHTTP defaults to **Packet up** with a CDN because some CDNs buffer complete request bodies. Use **Stream up** only when the CDN streams request bodies. WebSocket+TLS is the fallback transport when XHTTP does not work with the CDN.

A self-signed origin certificate is only for a CDN configured to accept an unverified origin certificate. It is not suitable for clients connecting directly to the node.

Each device has one VLESS identity, a UUID, used for every VLESS server of that device. It is made at the device's next update of the subscription. The node keeps the UUIDs as they are, because VLESS compares them itself; Hysteria2 is different, its node holds only a hash.

## VLESS behind a CDN

A CDN puts its own address between the client and the node: the client connects to the CDN, the CDN connects to the node. It helps where the node's address is blocked but the CDN's is not.

In the profile choose **Security: TLS**, a transport (XHTTP, or WebSocket), and fill in **CDN host**, the name clients connect to, for example `cdn.example.org`. Every link then carries that name as the address, the server name (SNI) and the Host header, and XHTTP starts as **Packet up**. The node keeps its own name for the certificate; clients never use it.

What to set on the CDN:

1. **Origin**: the node's own name (the profile's **Server name**, or the node's domain) and the profile's TCP port. Keep the DNS record of that name **DNS-only**, not proxied through the CDN: the CDN has to reach the node, and Let's Encrypt has to as well.
2. **Allowed requests**: GET and POST on the profile's path. XHTTP sends its upload as POST requests and receives the download as one long GET. A CDN that only passes GET breaks it.
3. **Caching**: off for that path. The CDN must not store or reshape the responses.
4. **WebSocket**: turn it on at the CDN when the profile's transport is WebSocket.
5. **Origin certificate**: the origin is always TLS, never plain HTTP, because the VLESS header would cross the network in the clear. Let's Encrypt works when the CDN checks the origin's certificate. A self-signed certificate works only if the CDN is set not to verify it, and Mistgate allows it only for a profile that has a CDN host.
6. **Origin port**: some CDNs forward only to a fixed list of ports. Use one the CDN supports (443 is the safe choice).

If XHTTP stalls, the CDN may be holding whole request bodies: keep **Packet up**. If it still does not work, make a second profile with WebSocket; people then see both. Against a CDN that cuts idle connections after 60 to 100 seconds, the node sends keep-alive padding on a quiet download; that has not been checked against a real CDN either.

> **Note:** real CDNs are not tested yet. The whole path, from a client through the node's TLS front to the internet, is tested in the project's end-to-end run, with the node itself standing in for the CDN. How a particular CDN treats long downloads, request sizes and idle connections is not known until someone tries it.

## The node's own site

This is the default. The node serves its own web site, the same one its Hysteria2 profile shows (see [Decoy site](decoy-site.md)), behind REALITY. A visitor of the node's domain on TCP 443 sees that site with the node's own Let's Encrypt certificate, and so does anyone who connects over QUIC, so nothing in the node points to a borrowed site. There is no third-party site to pick, to probe or to pass strangers on to.

What it needs:

- **A domain.** Let's Encrypt issues certificates for domains only. The node's address must be a host name whose A record points to the node, or you fill in a domain for the profile on that node (**Domain (SNI)** when you add the profile to a node). On a node known only by its IP the panel refuses the profile and says so; use a profile whose target is another site there.
- **The agent v0.1.35 or newer.** Older agents do not know this target. The panel does not send it to them: the profile shows on that node as failed with the reason "agent too old", and the rest of the node is not touched. Update the node on the **Updates** page. A profile with another site as the target needs only an agent that has VLESS at all.
- **TCP 443 open** if you want the certificate to renew.

The certificate. Until Let's Encrypt has issued one, the site shows a self-signed certificate of the same name, so clients and visitors are never held up by the issuance; then it switches to the Let's Encrypt one. Renewal does not need TCP 80: Let's Encrypt asks over TCP 443 (TLS-ALPN-01), REALITY passes that request to the node's own site, and the site answers it. TCP 80 still works when it is free. The node page shows the certificate's expiry once it exists, and the **Certificate is about to expire** alert watches it.

The node does not probe a target for this kind (there is nothing external to probe), and the first client after a start does not wait.

## Choosing another site as the REALITY target

Choose **Another site** to have the node pass visitors on to a real site you name. The target is the site your node impersonates. A bad choice makes the node easy to tell from the real site, or makes every handshake fail.

- It must speak **TLS 1.3** and offer the post-quantum key exchange **X25519MLKEM768**. Most large modern sites do. You can ask a site with OpenSSL 3.5 or newer: `openssl s_client -connect www.example.org:443 -tls1_3 -groups X25519MLKEM768`.
- Prefer a site in **the same network (ASN)** as the node, or at least in the same country and close to it: it matches the address the visitor connects to and keeps the handshake fast.
- Choose a site that stays up and that nobody blocks where your people live. The node connects to it for every visitor.
- Do not use a site behind a shared CDN or a big anonymising front: a stranger could use your node as a forwarder to it. Do not use your own panel, your other nodes or any site you operate.
- Put the exact host in the target and the names its certificate covers in **Server names**. If you leave them empty, the host of the target is used; the editor asks for the names when the target is an IP address.

When a server starts, the node tries the target once with a TLS 1.3 handshake that offers X25519MLKEM768 and notes what it finds in the server's health detail: the target does not answer, does not speak TLS 1.3, or does not offer the exchange. A bad target is a warning and never stops the server.

> **Note:** after the agent starts, or when a new target or server name appears for the first time, the first client can wait 5 to 10 seconds to connect. The node measures how the target answers once, then every later connection is quick.

## Ports

The default is TCP 443. The node opens the port in UFW itself, with the comment `mistgate-node-managed-tcp-v1-<port>`; on firewalld, or in your provider's firewall, you open it. Port 80 is refused: it is kept for Let's Encrypt.

TCP 443 is also where a Hysteria2 profile serves its web site. A VLESS profile with another site as the target on TCP 443 of the same node pauses that site; with the node's own site as the target, REALITY shows the same site, and nothing is lost. Hysteria2 itself, on UDP, is not affected either way. Read [VLESS and Hysteria2 on one node](vless-and-hysteria2.md) before you do that.

Two profiles cannot listen on the same TCP port of a node. A TCP port and a UDP port with the same number do not clash.

## What the apps receive

Four formats carry VLESS (the two below, Xray JSON and sing-box JSON); which one an app gets is decided by the rules on [Subscriptions](subscriptions.md). VLESS rides the same switch as Hysteria2, the link-app switch of **Apps in the subscription**: a person who gets Hysteria2 servers gets the VLESS servers of their group too.

**Link list.** One share link per server, read by Happ and other apps. XHTTP with REALITY:

```ini
vless://<uuid>@de1.example.com:443?encryption=none&fp=chrome&mode=auto&path=%2Fq8x2kd7w&pbk=<public key>&security=reality&sid=6ba85179&sni=www.example.org&spx=%2F57999bb8&type=xhttp#de1%20%C2%B7%20vless%20%C2%B7%20443
```

TCP with Vision has `flow=xtls-rprx-vision` and `type=tcp` and no `mode`, `path` or `extra`. `host` is there when you set an XHTTP host header, and `extra` when the padding is not 100-1000. The link carries only what differs from the app's defaults: a default written out would be a fingerprint.

**Mihomo YAML.** Apps on the mihomo core (kl!ck, Clash Verge, FlClash and others) get one proxy per server:

```yaml
- name: "🇩🇪 DE · VLESS"
  type: vless
  server: "de1.example.com"
  port: 443
  uuid: "<uuid>"
  udp: true
  packet-encoding: xudp
  tls: true
  servername: "www.example.org"
  client-fingerprint: chrome
  skip-cert-verify: false
  network: xhttp
  xhttp-opts:
    path: "/q8x2kd7w"
    mode: auto
  reality-opts:
    public-key: "<public key>"
    short-id: "6ba85179"
```

With TCP, `network: tcp` and `flow: xtls-rprx-vision` take the place of `xhttp-opts`.

A server whose node address is not a plain host name or IP address gets no link at all.

## What the apps need

- An app that reads VLESS REALITY links (Happ and most apps of the Xray family) or an app on the mihomo core. Mistgate checks its Mihomo output against Mihomo 1.19.32.
- XHTTP is newer than TCP with Vision. If an older app does not connect to an XHTTP server, make a second profile with the TCP transport; people then see both.
- The TCP port must reach the node. If the provider filters it, the app times out.

## Limits today

- The panel's client-eye check tests VLESS with the same resolved client settings used by subscriptions, including the handshake and tunnel. See [Health](../operations/health.md).
- **Check ports** on the node page shows whether a TCP connection reaches Mistgate from one other node, see [Nodes](nodes.md). It is one vantage point and does not run a full REALITY handshake. To see what a client sees, test from another network (`nc -vz de1.example.com 8443`), or by connecting from a phone.
- sing-box JSON carries VLESS over TCP (RAW + Vision) and WebSocket only, not XHTTP; Xray JSON and Mihomo YAML carry all of them. See [what works where](subscriptions.md#what-works-where).
- VLESS behind a CDN is not tested against a real CDN yet, see [VLESS behind a CDN](#vless-behind-a-cdn).
