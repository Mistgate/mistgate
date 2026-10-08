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
| `bittorrent_tracker` | The connect request of a UDP tracker: 16 bytes with the protocol's fixed 64-bit magic number. |
| `bittorrent_utp` | A standalone uTP SYN with no acknowledged packet and one of the two client header shapes. |

A tracker is recognized by its connect handshake only. Announce and scrape requests have no fixed marker, so their layout alone is not evidence, and a real client always connects first, which is blocked. DNS is never inspected: datagrams to ports 53 and 5353 are left alone, because a DNS query is arbitrary-looking bytes that can match a BitTorrent layout by chance.

Transmission DHT queries use a four-byte transaction ID; the detector accepts that alongside the two-byte IDs used by other clients.

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

Every detected flow is still blocked. Strong matches (`tracker_connect`, `dht_query` and `tcp_handshake`) are reported at once as a `torrent_attempt` warning in the node's **Events**. A uTP SYN is weak evidence: one SYN never names a person. The agent reports it only after at least five detections to at least three destination ports for the same user and profile within 10 minutes. The five-minute event limit then applies.

An event names the profile, transport (`tcp` or `udp`) and BitTorrent protocol. It names the user only when the node can tell reliably: on Hysteria2 the user who signed in, on AmneziaWG the device whose tunnel address belongs to exactly one user of that profile. Otherwise it says "an unknown user". Each event also says what the node matched (`evidence`) and the destination port (`dst_port`).

| `evidence` | What the node matched |
|:--|:--|
| `tracker_connect` | A UDP tracker connect request (the protocol's magic number). |
| `dht_query` | A DHT query. |
| `utp_syn` | The start of a uTP connection. |
| `tcp_handshake` | The BitTorrent handshake at the start of a TCP stream. |

Only the destination port is kept. No address is kept: neither the client's address nor the destination address leaves the node. The events can be read on the node's **Events** tab and through the API and MCP (`events_search` with the code `torrent_attempt`).

## Alert and Telegram

A person who tries torrents gets one **Torrent attempts** alert on the **People** tab of Health and in the **Connection** block of their page, for example "alice is trying to use torrents on EE: 7 attempts in a day, blocked". It names the nodes (at most five, then "+N"), the number of attempts in the last 24 hours, the evidence of the last attempt and the destination ports (at most five). The count is the number of recorded events, which the five-minute limit above caps.

The alert is one per person, whatever the number of nodes. It stays open while attempts continue and closes after a day without any. Telegram tells it once when it opens, so a person gets at most one message a day; a continuing stream of attempts does not repeat it, and the close is silent. A person who is back after a quiet day is a new alert and a new message. The alert does not count in the alert badge. You can mute it like any other.

Attempts the node cannot attribute to a user (an "unknown user" event) raise no alert in this version, and neither do users who are disabled or deleted. Events from older agents without `evidence` stay in the node's **Events** but do not raise a people alert.

If the AmneziaWG part cannot start on a node, for example because the kernel has no netfilter queue support, the node shows a `torrent_guard_degraded` warning event with the reason. AmneziaWG traffic on that node then passes uninspected; the Hysteria2 part works on its own.
