---
title: One 443 for everyone
description: How VLESS, TrustTunnel and MTProto share TCP 443 of one node by the name the client asks for, what the others cannot share, what a stranger sees, the DNS records and the setup in the admin and through MCP, the card of the shared port, and the limits.
---

Many networks let through only TCP 443. A node can run several TCP services on it at once: VLESS REALITY, VLESS over TLS, TrustTunnel and MTProto. The node's agent reads the **name** the client asks for in the first bytes of the connection (the SNI, the host name in the TLS hello), and hands the connection to the service that holds that name. Nothing is decrypted on the way and the front answers nothing itself. This page says who can share the port, what a stranger sees, what you must prepare (a name and a DNS record for each service) and how to set it up.

A node with one service on a port works as before: the agent starts the extra listener only when two or more services stand on the same TCP port.

## Who can share the port

| Service | The name it answers to | On a shared port |
|:--|:--|:--|
| VLESS REALITY, the node's own site | The server's **Domain (SNI)**, by default the node's name | Yes. It shows the node's own site, so it can be the default service |
| VLESS REALITY, another site | The names of that site (**Server names**, or the host of the target) | Yes, if another service shows the node's own site. A visitor with that name gets the other site |
| VLESS over TLS (XHTTP, WebSocket) | The **Domain (SNI)**, and behind a CDN also the CDN host | Yes. It shows the node's own site |
| TrustTunnel, HTTP/2 | The **Domain (SNI)** | Yes. It shows the node's own site |
| MTProto, **This node's name** | Its domain: the node's name or the **Domain (SNI)** | Yes. It shows the node's own decoy site |
| MTProto, **Another site** | The domain of that site | Yes, if another service shows the node's own site. A visitor with that name gets the other site |
| MTProto, **Web** mode | Its own name: the node's name or the **Domain (SNI)** | Yes. It shows the node's own decoy site; only a holder of a key gets the bridge page. See [Web Proxy](mtproto.md#web-proxy) |
| Hysteria2, its HTTPS site on TCP | None | No. The site gives its TCP port up to any TCP profile, as before: [VLESS and Hysteria2 on one node](vless-and-hysteria2.md) |
| Hysteria2, AmneziaWG, TrustTunnel HTTP/3 | None (UDP) | No. A UDP port is never shared: TrustTunnel with HTTP/3 beside Hysteria2 on UDP 443 is still refused |

The rules the panel checks when you save:

- **Every service has a name of its own.** The panel compares names exactly, in lower case, with no dot at the end. The same name on two services is refused (`sni_taken`). Disabled profiles count too, so switching one on later cannot break the port. A name must be a host name in ASCII, with a dot, no `*` and not an IP address: a client never puts an IP address into the SNI. A service that has no such name (for example REALITY with another site whose target is an IP address and with no **Server names**) cannot share the port (`share_unsupported`).
- **At least one service shows the node's own site.** Of those that do, the one with the lowest id is the **default** service: it gets everyone who does not name a service. Two services that both show another site cannot share the port (`share_needs_site`).
- **Only VLESS, TrustTunnel and MTProto take part.** A relay's entry and a bridge are not services of the port: the panel treats their ports as before.
- **The agent must list `tcp-share/1`.** What an old agent does is described under "What happens on the node" below.

## What a visitor sees

| What the visitor sends | Who answers |
|:--|:--|
| The name of a service | That service, as if it were alone on the port |
| No name (a probe by IP address), or a name no service holds | The default service, the node's own site |
| Not TLS (a plain `http://` request, noise), a broken hello, a hello longer than 16 KiB | The default service, with the bytes it has already sent |
| The name of a service that is not running, or one that does not take the connection within 5 seconds (it is restarting) | The default service |
| No complete hello within 10 seconds | The connection is closed |

The services see the visitor's real address, not `127.0.0.1`: the connection is passed on inside the agent, with no proxy protocol.

For your own services, with names of your own, the port looks like an ordinary host with several sites. A service that shows another site makes two faces: a visitor with its name sees that site, one without a name sees the default service's site, and someone who tries many names can tell the two apart. The panel warns about it (`share_two_faces`).

## Names, DNS records and certificates

Each service on the port needs a name that leads to the node. The panel proposes `<word>.<the node's address>`, with the first word of `cdn`, `static`, `media`, `assets`, `files`, `img`, `edge`, `app` that is not taken: a neutral name of an ordinary site. A node known by an IP address gets no proposal: type a domain you control. Existing services keep their names, so `tg://` links and subscriptions do not change.

The panel shows the DNS record next to the name, as type, name and value, with a **Copy record** button. You add it where the domain's DNS is kept, **without a proxy** (the grey cloud on Cloudflare):

| Record | When |
|:--|:--|
| `A` | The node is known by an IPv4 address: the value is that address |
| `CNAME` | The node is known by a host name: the value is that name. If your DNS does not take a CNAME here, use an `A` record with the node's IPv4 address |
| `AAAA` | The node is known by an IPv6 address |

The panel does not look the record up and does not wait for it: you add it, and the node asks Let's Encrypt for the certificate of the name by itself once the name leads to it. Until then the service shows what its own page says about a certificate that is not issued yet. Let's Encrypt validates over TCP 443 (TLS-ALPN-01) as well as over TCP 80: a request for the name of a service goes to that service, and a request for any other name of the node goes to the default service, which answers it. A name that is not proxied and leads to the node is all it needs.

Every new name costs a certificate. Let's Encrypt limits new certificates per registered domain (about 50 a week) and lists every one of them in public certificate logs, so keep the names neutral and few.

## Put a service beside another

### In the admin

1. On the node's **Profiles** tab choose **Add profile**, or **Edit** on a server, or press **Put on nodes** on the profile page. Pick the port the other service already holds (443).
2. The panel checks the choice (see [Profiles](profiles.md)). A new service usually starts with the node's own name, which the first service holds, so the panel says the name is held (`sni_taken`). If the port can be shared, the answer is **Put it beside "Main" on port 443**: a field with the proposed name and the button **Put beside on 443**, and next to it **Take 8443**, the old way out. A service that shows another site has no name of its own to set, so it gets the refusal only. A service whose name is free is not refused at all: the dialog just says that the port is shared (step 3).
3. **Put beside on 443** puts the name into the **Domain (SNI)** field and runs the check again. If it passes (or when the name was free from the start), the dialog says who restarts (the service that stood alone on the port; its open connections reconnect), which name the new service answers to, and shows the DNS record.
4. Add the record, then press **Add** or **Save**.

The warnings you may see before the click: `tcp_port_shared` (the port is shared, who is the default, who restarts) and `share_two_faces`. The refusals are `sni_taken`, `share_needs_site`, `share_unsupported` and `share_agent_old`; the dialog words each of them and offers a free port when there is one.

### With MCP

`profile_create` and `profile_deploy` take `sni_overrides`, a map of node id to the name the new server gets on that node, next to `port_overrides`. Only selected nodes may be named, and the name must be a host name with a dot, not an IP address. The plan shows the port, the `sni` and the warnings, the same check as the dialog's; a refusal comes as `sni_taken: name=de1.example.com&node=de1&port=443&profile=Main&share=1&suggest=cdn.de1.example.com`, with `suggest` when the node has a domain. The plan does not name the DNS record and does not check it: tell the owner which record to add. The apply needs the owner's approval, as every profile change. MCP has no tool that sets the port or the **Domain (SNI)** of one server that already stands on a node; do it in the admin. See [MCP](../reference/mcp.md).

## The card of the shared port

When two services or more stand on one TCP port, the node's **Profiles** tab shows a card **TCP 443 is shared**. Each service has a row with its profile, its name, and how many connections it has got; the **Default** mark shows the default service. Below the rows are the counters the agent keeps from its start:

| Counter | What it counts |
|:--|:--|
| **Accepted** | Connections the port took |
| **To the default** | Visitors with no name, an unknown name, not TLS, a broken or oversized hello, and those whose service was down |
| **Refused** | Closed because no service took the connection in time, or there was nowhere to send it |
| **Service down** | The name was a service that was not running: the visitor went to the default service |
| **Silent 10 s** | Said nothing for 10 seconds and were closed |
| **Dropped** | Unfinished handshakes dropped to make room when the port was flooded |

Refused, service down and dropped turn yellow when above zero. Under the counters are the up to five most frequent names visitors asked for that no service holds (host names only, no addresses), and when the agent reported. The agent sends the snapshot with its stats, and the panel writes it when its shape changes or at most every 5 minutes, so the counters lag by a few minutes. A port the agent has not reported yet says so.

A flood of unfinished handshakes is also an event, `front_overloaded`, at most once an hour per node. The services of the port keep running; the agent drops the oldest unfinished connections.

## What happens on the node

- **Restarts.** A port becomes shared, or stops being shared, by restarting the services that stood on it alone: their open connections reconnect. When a service joins a port that already has two or more, only the new one starts. When a service leaves and one remains, that one restarts. A restart of the agent restarts every service, shared or not. Visitors of a service that restarts wait up to 5 seconds, then go to the default service.
- **An old agent.** An agent without `tcp-share/1` cannot share a port. When the node's capabilities are known, saving a second service on the port is refused (`share_agent_old`). A node that has not reported yet is not refused: at deployment the panel sends the old agent only the default service, and the others stay unsent, marked `share_agent_old`. The card says **This node cannot share a port yet** with an **Open updates** button. After the update they start by themselves.
- **The port is held by another program.** The agent cannot take it, none of the services of the port run, and the card says **The node could not take port 443** with the error. The agent tries again on its own.
- **Two names collide later.** For example you changed the node's address, and a name built from it now equals another service's. One keeps the name; the other does not start and says `sni_shadowed`. Give it a name of its own.
- **A service that fails.** Its name is sent to the default service until it runs again, and the others are not touched.

## Limits

- Names match exactly. There are no wildcards, and one name cannot lead to two services: a chain such as REALITY, then MTProto, then a site is not built.
- The front has no limit per visitor address: behind a relay, a bridge or a mobile network many people share one address. It keeps at most 1024 unfinished handshakes per port and drops the oldest, and it reads a hello of up to 16 KiB.
- A node known only by an IP address cannot share the port: a name of its own needs a domain.
- UDP is not shared. Hysteria2 and AmneziaWG keep their UDP ports; TrustTunnel's HTTP/3 needs its own UDP port.
- The counters start from zero when the agent restarts.
