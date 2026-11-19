---
title: Panel at home behind NAT (subscription host)
description: "Run the panel without a public address: one node serves the subscription address and the agents' link on a subdomain, over a bridge the panel opens to it."
---

A panel needs a public address for two things: people fetch their subscriptions from it, and every agent dials it for its link. A panel at home, behind the router's NAT or the provider's CGNAT, has no such address. The **subscription host** solves this: one of your nodes serves a subdomain, for example `sub.example.com`, and hands its connections to the panel.

The panel opens the connection itself: it calls the host's own site on port 443 like a browser and keeps a few HTTP/2 connections open, so nothing has to reach your home network. TLS of the subdomain ends in the panel, which holds its certificate and key. The node only passes the encrypted bytes along; it stores no subscription, token or key. The admin page never goes through the host: it stays on the panel's own address or a local listener. The design is in `design/adr/0018-panel-behind-nat.md`.

This is the VPS edition only. The Cloudflare edition already has a public address and does not show the card.

## Requirements

- **The host's port 443 belongs to the node's own site.** One of these:
  - a profile on TCP 443 that serves a site of its own: VLESS with TLS, VLESS REALITY whose target is the node itself, TrustTunnel, or MTProto with its decoy site;
  - nothing on 443: then give a **Site name**, a second name pointing at the node. The agent shows a site for it there and gets a certificate (self-signed until Let's Encrypt issues one).

  Refused: a REALITY profile on 443 whose target is someone else's site, a Hysteria2 site on TCP 443, a profile on 443 that cannot share the port, and a profile that answers to the subdomain itself. The panel names the reason; the node's profiles keep running.
- **Every agent supports the link.** All nodes must run an agent with `ws-link/1` and `panel-link/1`. The panel refuses while some node lacks them and lists those nodes with *update the agent* ([Updates](../operations/updates.md)).
- **DNS records.** An A record for the subdomain pointing at the node's address, an AAAA record if the node has IPv6, and the same for the site name if you give one. The panel checks them before it enables the host; **Check records** shows what they resolve to.
- The host node must be online and not retired.

## A fresh panel at home

1. Set the panel up without `--public-url`, with the admin on a local listener:

   ```sh
   mistgate setup --admin-listen 127.0.0.1:8081
   ```

   Open the admin at `http://localhost:8081/` on that machine, or through an SSH tunnel. See [Install the panel](../getting-started/install-panel.md) for the rest of the setup.
2. Create the A (and AAAA) records of the subdomain and of a second name for the node's site, both pointing at the server that becomes the first node.
3. Go to **Nodes → Add node** and install the node over SSH ([Install a node over SSH](../getting-started/ssh-install.md)). On the server step, tick **This node is the subscription host** and enter the **Subdomain** and the **Site name**.

   The node cannot reach a panel behind NAT yet, so the server check marks the panel as unreachable without stopping, and the panel enrolls the node inside the SSH session: the node makes its key, the panel signs it, and the role and the link address go to the node on standard input, never on its command line. If a record does not point at the server, the install stops with a DNS error; **Continue anyway** goes on without the check, for example behind a DNS proxy. If the install fails after the role is on, the panel turns the role off again.

## Enabling on an existing panel

Open **Settings → Domains**, card **Subscription host**:

1. Choose the **Node** and enter the **Subdomain**, and the **Site name** if no profile on the node's 443 serves its own site.
2. **Check records** shows the addresses the names resolve to now and the addresses expected.
3. **Enable**. If a record does not point at the node, the panel refuses and offers **Enable anyway**. Use it only when you know the record is right, for example behind a DNS proxy.

## Ready

Nothing moves until the host is **ready**. Until then the card lists the steps and ticks each one as it comes true:

1. The node is online.
2. The node took the role (an unfit role names its reason; nothing is installed then).
3. The node's site has a public certificate.
4. The panel's bridge to the node is up.
5. The panel has a certificate for the subdomain (Let's Encrypt, through the host).

Once all hold, the panel records `sub_host_ready` and switches at run time, without a restart: the **Base of the subscription links** becomes the subdomain (*via subscription host*), the agents receive the new link address over their current link and keep their old one until a session over the new one succeeds, and new nodes enroll over the subdomain's link. Losing readiness later does not move anything back.

While the host is on, the card shows the domain, the site name, the node, the bridge (*bridge up* or *bridge down* with the reason, and since when) and the CAA record. The panel records `sub_host_tunnel_down` when the bridge stays down and `sub_host_tunnel_up` when it is back.

What a visitor sees: a connection for the subdomain that the panel cannot take (no bridge, or the host is full) goes to the node's own site, never to a reset. The host lets at most 400 people through at once, 16 per address (an IPv6 address counts by its /64), and keeps a reserve for the agents' links.

## CAA record

The card shows a CAA record that lets only this panel's certificate account issue certificates for the domain. Publish it in your DNS. The line under it says whether DNS has it: *Published in DNS*, *Not in DNS yet*, *Appears once the panel has its certificate account*, or *Could not check DNS for it*.

## Profiles on the host

While a node is the host, the panel refuses a change to its profiles that would leave it unfit (`sub_host_inbound_unfit`): a REALITY profile on 443 that borrows another site, a Hysteria2 site on 443, a profile that takes the subdomain, or switching off or deleting the only site on 443 when there is no site name. Keep the port for the node's own site, or move the host first.

## Moving to another node

Both the move and turning the host off happen in two phases, and you finish each one yourself.

1. Under **Move to another node**, choose the node (and its **Site name** if it needs one) and press **Start the move**. The panel dials both nodes and the card shows both bridges.
2. Change the subdomain's A (and AAAA) record to the new node's address. The card shows where the record points now.
3. Press **Finish** once the record points at the new node. If it does not yet, the panel asks whether to finish anyway. **Cancel** drops the move, and the host stays on the current node.

## Turning it off

1. Under **Address the panel goes back to**, enter `https://host[:port]`. Leave it empty only if the panel will have no public address: the panel asks first, since it then hands out no subscription links and nodes cannot reach it.
2. Press **Turn off**. The panel is back on that address, and its agents move their link there.
3. The card lists the nodes **Not yet on the new address**. When the list is empty, press **Finish**. If some nodes stay on the old address, the panel asks whether to finish anyway. Those nodes lose the panel until they get its new address (see [Offline nodes](#offline-nodes)). **Cancel** keeps the host on.

## Moving a public panel home

1. On the public panel, enable the subscription host as above and wait until it is ready, so that people and agents use the subdomain.
2. Wait until no node uses the panel's own address any more.
3. Take an [encrypted backup](../operations/backups.md).
4. At home, restore it into a new data directory with `mistgate backup restore` ([Restore on a new panel](../operations/backups.md#restore-on-a-new-panel)).
5. Move the admin to a local listener, then start the panel:

   ```sh
   mistgate admin-address --admin-listen 127.0.0.1:8081 --data-dir NEW-DIR
   ```

6. Stop the old panel. The home panel dials the host, and the nodes reach it through the subdomain.

Passkeys are bound to the admin's address, so they stop working at the new address. Sign in with your password and authenticator code, then add your passkeys again ([Security](../operations/security.md)).

## Offline nodes

A node that was offline while the address changed did not receive the new link address. On that node, run:

```sh
mistgate-node set-panel --link-url wss://sub.example.com/<secret-prefix>/
systemctl restart mistgate-node
```

The agent tries the new address and its current one in turn, and keeps the new one once a session over it succeeds ([CLI](../reference/cli.md#mistgate-node-set-panel)).

## Agents that reach the panel only by the link

With a host, the agents reach the panel only through the WebSocket link at the subdomain. They keep their self-update and the per-network statistics: an agent with `link-fetch/1` downloads update files and the network table over the link, with a token of its link session. An agent without it cannot update from a panel behind NAT; update it before the switch ([Updates](../operations/updates.md)).

## MCP

An Admin-profile token reads the state with `subscription_host_get`. It changes the host through `subscription_host_set`, `subscription_host_move` (`start`, `finish`, `cancel`) and `subscription_host_disable`. Every change is a plan that the owner approves with a fresh sign-in, and no tool shows the bridge path or the link prefix. `node_install` takes `subscription_host_domain`, `site_name` and `skip_dns_check` for a first host. See [MCP](../reference/mcp.md).

## Known limits

- The host is a single point for subscriptions and links; move it to another node when it has to go down.
- What the bridge cannot hide: one to four long connections from the panel's address to the node's 443, pings every 15 seconds, a new connection about every hour. A visitor of the subdomain during an outage sees the node's certificate for another name.
- The panel's certificate for the subdomain is renewed through the host: a host that stays down long enough lets it expire.
- A Hysteria2 site cannot share TCP 443 with the host yet.
