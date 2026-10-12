---
title: VLESS and Hysteria2 on one node
description: Where Hysteria2 and VLESS listen on a shared node, what happens when both want TCP 443, how Let's Encrypt renewal depends on it, and how to try VLESS with a few people first.
---

A node can run Hysteria2 and VLESS side by side. They use different transports (Hysteria2 is UDP, VLESS is TCP), so they rarely meet. The one place they do is TCP 443, and this page is about that port.

## What listens where

| Port | Who | What for |
|:--|:--|:--|
| UDP 443 (or the profile's port) | Hysteria2 | The tunnel, and the HTTP/3 site every non-client sees |
| TCP 443 | Hysteria2 | Its HTTPS site, and the answer to Let's Encrypt |
| TCP, the profile's port (443 by default) | VLESS | The tunnel |
| TCP 80 | The node | Let's Encrypt, when nothing else holds the port |

Two profiles cannot take the same port and network of one node, but UDP 443 and TCP 443 are different ports, and a VLESS profile on another TCP port touches nothing of Hysteria2.

When a VLESS profile on TCP 443 is switched on, the node closes the Hysteria2 HTTPS site on that port and opens it again when the port is free, that is when the VLESS profile is removed, switched off or moved to another port. Hysteria2 itself, on UDP, and its HTTP/3 site keep working. Anyone who opens `https://de1.example.com` on TCP 443 now reaches REALITY, which passes everyone who is not a VLESS client to the REALITY target: they see the target's site, with the target's certificate.

When you add a VLESS profile on TCP 443 to a node with an enabled Hysteria2 profile, or the other way round, the dialog warns about it (`hy2_decoy_paused`) with the names of both profiles, the port, and the target. The warning does not stop you.

## Ways to share a node

| Way | Hysteria2 keeps | What to know |
|:--|:--|:--|
| **VLESS on another TCP port**, for example 8443 | Everything: its site on TCP 443 and the certificate renewal | Open the port at the provider. Some networks filter ports other than 443, and VLESS on such a port does not connect there. |
| **VLESS on TCP 443, REALITY target another site** | The tunnel. Its HTTPS site is paused | The certificate of a Let's Encrypt Hysteria2 profile now renews only through TCP 80: see below. |
| **VLESS on TCP 443, the node's own site as the REALITY target** | The tunnel and the same site, shown through REALITY | **Planned.** It needs a domain on the node. |

The first way costs nothing and changes nothing for Hysteria2; take it when you can open the port. The second gives VLESS the port every network lets through, at the price of the site and of TCP 80. TLS behind a CDN is **Planned** as well.

## Certificates

A node gets a Let's Encrypt certificate for its Hysteria2 profile by answering a challenge from Let's Encrypt, in one of two ways: over TCP 80, or over TCP 443 through the Hysteria2 site. While the site is paused, a challenge sent to TCP 443 reaches the REALITY target instead and fails. That leaves TCP 80:

- **Keep TCP 80 reachable from the internet** on any node that has a Let's Encrypt Hysteria2 profile and a VLESS profile on TCP 443 with another site as the target, and keep other programs off it.
- A failed try on 443 does no harm: the node switches to the check on 80 with its next order, and Let's Encrypt allows five failed validations per name per hour.
- A Hysteria2 profile with a self-signed certificate has nothing to renew and is not affected. REALITY itself needs no certificate.

The **Certificate is about to expire** alert (`cert_expiry`, see [Health](../operations/health.md)) warns 14 days ahead. While VLESS holds TCP 443, the doctor cannot read the certificate the Hysteria2 profile serves there, so the alert may stay silent for it. Look at the expiry date of the Hysteria2 certificate yourself now and then, or keep it on a node where the two do not share the port.

## What Mistgate checks of the ports

The port check measures UDP: it sends datagrams to the node and counts what arrives. It does not tell you whether a TCP port reaches the node, and it does not check TCP 80. After you add a VLESS profile, test its port from another network, for example with `nc -vz de1.example.com 8443`, or connect from a phone.

## Trying VLESS with a few people

You can give VLESS to a few people first and see how it behaves next to Hysteria2.

1. Make a group, for example "VLESS test", and give it your Hysteria2 profile and the new VLESS profile. Move the people who test into that group: a person is in exactly one group, and the group decides the servers they see.
2. Create the VLESS profile. Under **Right after it is created** → **Add it to groups**, Mistgate ticks the groups that have no profile of that protocol yet, which for the first VLESS profile means all of them. Leave only "VLESS test" ticked.
3. Put the profile on the nodes you want to try. People of the test group get the new servers at their next update of the subscription, next to the Hysteria2 ones, sorted by country, location and node, then Hysteria2 before VLESS.

The server name follows the name template of the subscription (by default the country, then the profile's name), so a profile named "VLESS · 443" shows as "🇩🇪 DE · VLESS · 443" next to "🇩🇪 DE · Hysteria2 · 443". Happ also shows the load of the node at the end of the name.

To compare, open a person on the **Users** page: their traffic for the period is listed by node and protocol, and the charts of the overview split by protocol too. Two VLESS profiles on one node are counted together; to compare VLESS over TCP with VLESS over XHTTP, put them on different nodes.

When you are happy, add the VLESS profile to the other groups; if not, remove it from the test group and delete it.
