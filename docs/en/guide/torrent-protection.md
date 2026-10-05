---
title: Torrent protection
description: The per-node BitTorrent blocker, what it recognises, how it blocks on AmneziaWG and Hysteria2, what its events show and what it cannot catch.
---

Many hosters suspend a server after abuse complaints about BitTorrent downloads. Each node can block the recognized BitTorrent traffic of its users. The protection is off by default and is set per node.

## Turn it on

Open the node, **Settings**, tick **Block recognized BitTorrent traffic** and press **Save**. The owner and helpers can change it, like the other node settings.

- The node's agent must support it (a Linux agent that reports `torrentguard/1`). An older agent shows the switch greyed out with "Update this node agent to enable the setting"; see [Updates](../operations/updates.md). Turning it off always works.
- Saving a change restarts the node's Hysteria2 profiles, so connections that are already open cannot keep the old policy. Their users reconnect within seconds. AmneziaWG profiles are not restarted.

## What it recognises

Only plaintext BitTorrent, and only by a validated protocol structure. Ports are never evidence.

| Protocol label | What is recognised |
|:--|:--|
| `bittorrent_tcp` | The BitTorrent handshake at the start of a TCP stream. |
| `bittorrent_dht` | A DHT query (a bencoded KRPC query). |
| `bittorrent_tracker` | A UDP tracker request (connect, announce or scrape). |
| `bittorrent_utp` | The start of a uTP connection: a standalone SYN with a zero timestamp difference. |

Only what the user's client sends is classified, never what comes back from the internet. A remote peer cannot get a user blocked or reported with a crafted packet, and ordinary DHT replies, uTP data packets or QUIC traffic do not count.

## How it blocks

### AmneziaWG

The agent adds its own nftables table, `mistgate_torrentguard`, to the forwarding path. Only traffic that clients send out through the node's AmneziaWG interfaces goes to the agent, and only the start of each flow: the first packets of a TCP connection the client opened, and the UDP datagrams of a flow that has not had a reply yet. Traffic between two AmneziaWG interfaces is left out.

When a packet matches, the kernel marks its connection and drops it, together with every later packet of that connection in both directions; a matching UDP datagram is dropped. Everything else stays in the kernel and never reaches the agent. The queue is fail-open: if the agent stops listening, traffic flows on uninspected instead of stopping.

### Hysteria2

The Hysteria2 engine checks the connections it opens for its users. An outgoing TCP connection whose first bytes are the BitTorrent handshake is closed; a UDP datagram that carries a recognized request is dropped. Other traffic of the same user goes on. Until a stream can be told apart, the engine holds back at most its first 19 bytes; TLS, HTTP and other ordinary traffic differ from the handshake at once.

## What it cannot catch

Detection is best effort. These pass:

- encrypted BitTorrent (message stream encryption);
- BitTorrent inside another VPN or proxy;
- HTTPS web seeds;
- fragmented packets and formats the parsers do not know.

The protection lowers the number of complaints; it does not guarantee that none come.

## Events

Each blocked attempt is a `torrent_attempt` warning in the node's **Events**, for example "Possible BitTorrent attempt by Alice" with "protocol: tcp / bittorrent_tcp". It names the profile, the transport (`tcp` or `udp`) and the BitTorrent protocol. It names the user only when the node can tell reliably: on Hysteria2 the user who signed in, on AmneziaWG the device whose tunnel address belongs to exactly one user of that profile. Otherwise it says "an unknown user".

No address is kept: neither the client's address nor the destination leaves the node. The events can be read on the node's **Events** tab and through the API and MCP (`events_search` with the code `torrent_attempt`).

If the AmneziaWG part cannot start on a node, for example because the kernel has no netfilter queue support, the node shows a `torrent_guard_degraded` warning event with the reason. AmneziaWG traffic on that node then passes uninspected; the Hysteria2 part works on its own.
