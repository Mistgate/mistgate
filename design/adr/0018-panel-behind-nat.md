# 0018. A panel behind NAT: one node is the subscription host

Status: accepted, 2026-11-16.

## Context

Mistgate assumed the panel has a public address: people fetch their subscriptions from it and every agent dials it for
its link. Many owners would rather run the panel at home (a mini PC or a NAS) behind a router's NAT or a provider's
CGNAT, where nothing can reach it from outside and the home address changes. They already own servers with public
addresses: the nodes. The question is how to give such a panel a public subscription address without making a node a
second panel and without putting any secret on a machine that is easier to seize than the panel.

## Decision

**One node is the subscription host.** The owner names a node and a subdomain whose A/AAAA records point at it. The
node then serves that subdomain on its port 443; everything else on the node stays as it was.

**The panel dials out.** The panel opens a TLS connection to the host's 443 with the subscription subdomain as SNI and
an ALPN protocol derived from a tunnel secret (a random name kept only in the panel's settings and the host's routing
table, never shown to an agent or an API caller); the host tells the tunnel from subscription clients by that ALPN
value alone. A secret SNI was tried first and failed in the field: the home panel's ISP path cut TLS whose SNI did not
resolve in DNS, while the same SNI worked on the node itself; the subdomain always resolves. Both ends prove themselves
with fleet-CA certificates: the node presents its ClientAuth node certificate, the panel a ServerAuth certificate naming
the tunnel secret (sent encrypted under TLS 1.3). Over that one connection runs HTTP/2 (`golang.org/x/net/http2`, already
a dependency), so many subscription fetches and agent links share it. yamux was rejected: a new dependency for what
HTTP/2 already does.

**The host forwards raw TLS and stores nothing.** For the subscription subdomain the host splices the client's TLS
bytes into a tunnel stream; TLS terminates in the panel, which holds the certificate and its key. The node never sees a
token, a subscription or a key. The alternative "the node hosts the subscriptions" was rejected because the node would
then store tokens and keys. A separate port was rejected too: a dynamic home address cannot be allowed through a
firewall, and the panel would still need to be reachable. SSH reverse forwarding was rejected: it needs a login on the
node held open by the panel, and the forwarded port would be one more thing to protect.

**The host must own its 443.** The node's 443 must belong to a site the agent controls: a Hysteria2 profile's site, a
REALITY profile that points at itself with PROXY protocol v2 (`xver`), or a free 443 where the agent runs its own site.
A REALITY profile whose target is someone else's site is refused (`sub_host_node_unfit: reason=reality_foreign_target`):
the host could not tell its subdomain's connections from the cover traffic. Only the subscription name is forwarded.

**Agents follow by link only.** In this mode the agents reach the panel only through the WebSocket link (`ws-link/1`)
at the subscription host's address. The host's own agent link runs inside the tunnel. The first host is installed over
SSH with offline enrollment in the same SSH session, because it cannot reach the panel before it is the host. Existing
nodes receive the new `panel_link_url` pushed over their current link; each keeps its old address until the first
successful connection to the new one. A node that was offline during the switch is pointed at the panel with
`mistgate-node set-panel`. Turning the subscription host on or off changes routing at run time, without a restart.

**The admin never goes through the host.** WebAuthn binds passkeys to one RP ID, and the admin would otherwise be
exposed through a node. `mistgate admin-address` moves the admin page to a local listener (the home network) when the
panel has no public address of its own.

**Changes are two-phase and the owner finishes them.** Moving the host to another node starts a move (both nodes get
tunnels; the owner changes the A record), and the owner finishes it once the record points at the new node. Turning the
host off sends the panel back to a public URL (or none) and waits while nodes move their link there; the panel lists the
nodes still connected through the subdomain, and the owner finishes, forcing it if needed. Enabling checks the domain's
A/AAAA records against the node and refuses a mismatch, with an explicit "save anyway" (`skip_dns_check`) for proxied
or not yet propagated DNS. The panel shows the CAA record that limits certificates for the domain to its ACME account.

**VPS edition only.** The Cloudflare edition does not have it: a Worker already has a public address and cannot hold
long-lived outbound sockets.

## Consequences

- A home panel needs no public address, no port forwarding and no dynamic DNS; the fleet stays reachable as long as the
  host node is.
- The host is a single point for subscriptions and agent links; a tunnel down for a while records `sub_host_tunnel_down`
  and its recovery `sub_host_tunnel_up`, and the owner can move the host to another node.
- Seizing the host yields no subscriptions, tokens or keys: only TLS traffic it cannot decrypt and the tunnel secret.
- The tunnel's ALPN value is visible on the wire and unlike any browser's; a network that blocks unknown ALPN values
  would block the tunnel, and with it every subscription and agent link (the subdomain then shows the node's own site).
- Nodes whose agent lacks the link capabilities block enabling (`sub_host_nodes_lack_link`) until they are updated.
- MCP and API tokens can read the state; every change is an approved plan with a fresh owner step-up.
