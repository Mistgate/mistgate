# 0018. A panel behind NAT: one node is the subscription host

Status: proposed, 2026-11-20.

## Context

Mistgate assumed the panel has a public address: people fetch their subscriptions from it and every agent dials it for
its link. Many owners would rather run the panel at home (a mini PC or a NAS) behind a router's NAT or a provider's
CGNAT, where nothing can reach it from outside and the home address changes. They already own servers with public
addresses: the nodes. The question is how to give such a panel a public subscription address without making a node a
second panel, without putting any secret on a machine that is easier to seize than the panel, and without a new
transport that a censor could tell apart from the node's own site.

## Decision

**One node is the subscription host.** The owner names a node and a subdomain whose A/AAAA records point at it. The
node then hands the raw TLS of that name, on its port 443, to the panel; everything else on the node stays as it was.
TLS ends in the panel, which holds the certificate (Let's Encrypt, TLS-ALPN-01 through the host) and its key. The node
never sees a token, a subscription or a key.

**The panel dials the host over the bridge.** The node bridge (`internal/node/bridge`) already works in reverse: an exit
calls an entry's site and keeps pairs of long POST requests open, and the entry hangs the connections that come to it
on a free pair. The panel is the exit and the host the entry. The panel calls the host's own site on 443 with Chrome's
ClientHello (uTLS), verifies it by its public certificate (or the leaf's pin while it has none), and speaks HTTP/2 as a
browser would. A previous design (a mark in the hello's session id, a fleet client certificate in the handshake, a
two-minute clock window and a replay cache) was rejected: the replay cache was lost on a restart, the certificate
showed in the packet sizes, and every one of those pieces was new code on the wire.

**The panel proves itself inside the connection.** The fleet CA gives the panel a short certificate (seven days, a fresh
P-256 key each time, in memory only) with the URI `spiffe://panel/<ca-id>` and only the ClientAuth usage. The panel
signs the connection's TLS exporter under its own domain (`mistgate-sub-host/1`) and sends the certificate and the
signature in the request headers, inside HTTP/2, as an exit does for the bridge. The host checks the chain against its
fleet CA, the URI and the signature; anything else gets the node's site, byte for byte what a visitor gets. There is no
clock window and no replay cache: the proof is bound to one TLS connection. An agent certificate never passes as the
panel, a panel certificate never passes as an agent, and a bridge proof never passes as a host proof.

**The front gives the host its connections.** The host's 443 is the agent's front (shared port, internal/node/front).
The host role adds a route for the subdomain beside the inbounds' routes, from the node's settings and a copy in
`agent.json` (the host must route before its first session, which runs through it). When no inbound serves the node's
own site on 443, the agent serves one itself for a second name the owner gives (`site_name`), self-signed until ACME
issues a certificate. While the panel has no connection, or the host is overloaded, a subdomain connection goes to the
node's site (the default route), never to a reset: a reset would tell a prober this name is handled specially. The
visitor then sees a certificate for the wrong name, which is the price of that.

**The host must own its 443.** The node's 443 must belong to a site the agent controls: an inbound with its own site
(VLESS TLS, REALITY "self", TrustTunnel, the MTProto decoy) or the agent's site. Refused, on the panel and on the
agent alike: a REALITY inbound on 443 whose target is someone else's site (`reality_foreign_target`), a Hysteria2 site
on TCP 443 (`hysteria2_site_443`, until it can sit behind the front), an inbound on 443 that cannot share the port, an
inbound that claims the subdomain. An unfit role installs nothing on the node; its inbounds keep running.

**Readiness before anything moves.** The panel moves subscriptions and agents to the subdomain only once the host is
ready: the host reports no error, its site has a publicly trusted certificate, the panel holds a connection to it, and
the panel has the subdomain's certificate. Losing readiness later does not move them back; the agents keep the
fallback of a new panel address (#56).

**Agents follow by link.** The other agents reach the panel only through the WebSocket link at the subdomain
(`NodeSettings.panel_link_url`, #56). The host's own link runs inside the bridge (`Entry.DialExit`), never through its
own public address. A link-only agent keeps the per-network statistics and its self-update: it fetches the network
table and update files over the link with a token of its link session (`link-fetch/1`).

**Limits.** The front keeps no per-source limits on purpose. The host's intake does: at most 400 people at once through
the bridge (what four HTTP/2 connections carry), 16 per source address (an IPv6 one by its /64), 64 subscriptions per
network (an IPv4 /24, an IPv6 /48, so a few dozen /64s of one network cannot fill the class) once the subscriptions take
about three quarters of their class (below that a prober cannot make the host switch the name to the site with a few
connections more), and a reserve for the agents' links, recognised by the fleet nodes' addresses the panel sends: the
subdomain is one name and the path is inside TLS, so the source address is the only thing the host can tell them by. A
connection over a limit waits up to two seconds for a place before it goes to the site.

**The first host is installed over SSH** with offline enrollment in the same session: it cannot reach the panel before
it is the host. The panel signs the node's CSR, sends the role on standard input, and reads back only the pin of the
host's site certificate.

**Changes are two-phase.** Moving the host to another node gives both nodes the role (the panel dials both) until the
owner has changed the A record and finishes the move. Turning the host off sends the agents to a public link address
and waits while nodes move there; the owner finishes, forcing it if needed. The admin never goes through the host.

**VPS edition only.** The Cloudflare edition has a public address and cannot hold long-lived outbound sockets.

## Consequences

- A home panel needs no public address, no port forwarding and no dynamic DNS; the fleet stays reachable as long as the
  host node is. The host is a single point for subscriptions and links; the owner can move it.
- Seizing the host yields no stored subscriptions, tokens or keys: TLS it cannot decrypt, the bridge path, and the role.
  It does not keep the subdomain safe, though: the subdomain points at the host, so whoever holds it can pass an ACME
  challenge for the name and get a certificate of their own, then terminate the people's TLS and read their
  subscriptions while it keeps the role. Only a CAA record that names this panel's ACME account (`accounturi`) and
  challenge method stops that, and only at authorities that honour it; the panel shows the record but does not enforce
  it yet.
- What the bridge cannot hide remains: one to four long connections from the panel's address to the node's 443, pings
  every 15 s, a new connection about every hour. A subdomain visitor during an outage sees the node's certificate.
- The extra hop is measurable: a subscription or a link through the host takes the panel–host round trip on top of the
  person–host one, and its first bytes wait for a free pair. A prober that compares the subdomain's timing with the node's
  own site can tell the subdomain is relayed.
- The panel's subdomain certificate is renewed through the host: a host that stays down long enough lets it expire.
- The agent protocol only grows (`sub-host/1`, `link-fetch/1`); fleets without a host see no change.
