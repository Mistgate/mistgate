---
title: Panel at home behind NAT (subscription host)
description: "Run the panel without a public address: one node serves the subscription address on a subdomain and forwards it to the panel over a tunnel the panel opens."
---

A panel needs a public address for two things: people fetch their subscriptions from it, and every agent dials it for its link. A panel at home, behind the router's NAT or the provider's CGNAT, has no such address. The **subscription host** solves this: one of your nodes serves the subscription address on a subdomain, for example `sub.example.com`, and forwards it to the panel.

The panel opens the connection itself: it dials the host's port 443 and keeps one encrypted tunnel open, so nothing has to reach your home network. TLS of the subdomain ends in the panel, which holds the certificate and its key. The node only passes the encrypted bytes along. It stores no subscription, token or key. The admin page never goes through the host: it stays on the panel's own address or a local listener. The design is in `design/adr/0018-panel-behind-nat.md`.

This is the VPS edition only. The Cloudflare edition already has a public address and does not show the card.

## Requirements

- **The host's port 443 belongs to the node's own site.** Use one of these:
  - a Hysteria2 profile, whose site is on TCP 443;
  - a TrustTunnel profile on TCP 443;
  - a VLESS REALITY profile whose target is the node itself (the node's own site, with PROXY protocol);
  - a free 443, where the agent runs its own site.

  A REALITY profile whose target is someone else's site is refused with *a REALITY profile on it already uses port 443 for another site*.
- **Every agent supports the link.** All nodes must run an agent with `ws-link/1` and `panel-link/1`. When you enable the host, the panel refuses if some node lacks them and lists those nodes with *update the agent*. Update them first ([Updates](../operations/updates.md)).
- **A DNS record.** An A record for the subdomain pointing at the node's address, and an AAAA record if the node has IPv6. The panel checks the records before it enables the host.
- The host node must be online, and must not be retired.

## A fresh panel at home

1. Set the panel up without `--public-url`, with the admin on a local listener:

   ```sh
   mistgate setup --admin-listen 127.0.0.1:8081
   ```

   Open the admin at `http://localhost:8081/` on that machine, or through an SSH tunnel. See [Install the panel](../getting-started/install-panel.md) for the rest of the setup.
2. Create the subdomain's A record (and AAAA if the node has IPv6) pointing at the server that becomes the first node.
3. Go to **Nodes → Add node** and install the node over SSH ([Install a node over SSH](../getting-started/ssh-install.md)). On the node details step, tick **This node is the subscription host** and enter the **Subdomain**.

   Until then the node cannot reach a panel behind NAT, so the panel enrolls it inside the SSH session itself. When the node is up, the panel moves its subscription address onto the subdomain. If the record does not point at the node yet, the install stops with a DNS error. **Continue anyway** goes on without the check, for example when the DNS is proxied.

## Enabling on an existing panel

Open **Settings → Domains**, card **Subscription host**:

1. Choose the **Node** and enter the **Subdomain**.
2. **Check record** shows the addresses the domain resolves to now and the addresses expected.
3. **Enable**. If the record does not point at the node, the panel refuses and offers **Save anyway**. Use it only when you know the record is right, for example behind a DNS proxy.

The panel switches at run time, without a restart. Its agents receive the new link address over their current link. Each agent keeps its old address until its first connection over the new one succeeds. The **Base of the subscription links** line then reads *via subscription host*.

While the host is on, the card shows the domain, the node, the tunnel's state (*tunnel up* or *tunnel down*, and since when) and the CAA record. The panel records the events `sub_host_tunnel_down` when a tunnel stays down and `sub_host_tunnel_up` when it is back.

The panel's tunnel looks like Chrome opening the subdomain; the node recognises it by a mark that carries the panel's clock and accepts it only within two minutes of its own. If the node answers but does not recognise the panel, the card says so instead of *tunnel down* and the panel records `sub_host_tunnel_not_recognised`: check that time synchronisation is on for both the panel and the node (`timedatectl set-ntp true`) and that the node's agent is up to date. An SSH install of a host checks the server's clock against the panel's before it turns the host on.

## CAA record

The card shows a CAA record that lets only this panel's certificate account issue certificates for the domain. Publish it in your DNS. The line under it says whether DNS has it: *Published in DNS*, *Not in DNS yet*, *Appears once the panel has its certificate account*, or *Could not check DNS for it*.

## Moving to another node

Both the move and turning the host off happen in two phases, and you finish each one yourself.

1. Under **Move to another node**, choose the node and press **Start the move**. The new node gets its own tunnel and the card shows both tunnels.
2. Change the domain's A (and AAAA) record to the new node's address. The card shows where the record points now.
3. Press **Finish** once the record points at the new node and its tunnel is up. **Cancel** drops the move, and the host stays on the current node.

## Turning it off

1. Under **Address the panel goes back to**, enter `https://host[:port]`. Leave it empty if the panel has no public address of its own.
2. Press **Turn off**. The panel is back on that address, and its agents move their link there.
3. The card lists the nodes **Not yet on the new address**. When the list is empty, press **Finish**. If some nodes stay on the old address, the panel asks whether to finish anyway. Those nodes lose the panel until they get its new address (see [Offline nodes](#offline-nodes)). **Cancel** keeps the host on.

## Moving a public panel home

1. On the public panel, enable the subscription host as above, so that people and agents use the subdomain.
2. Wait until no node uses the panel's own address any more. The subdomain carries everything then.
3. Take an [encrypted backup](../operations/backups.md).
4. At home, restore it into a new data directory with `mistgate backup restore` ([Restore on a new panel](../operations/backups.md#restore-on-a-new-panel)).
5. Move the admin to a local listener, then start the panel with the same flag:

   ```sh
   mistgate admin-address --admin-listen 127.0.0.1:8081 --data-dir NEW-DIR
   ```

6. Stop the old panel. The home panel dials the host, and the nodes reach it through the tunnel.

Passkeys are bound to the admin's address, so they stop working at the new address. Sign in with your password and authenticator code, then add your passkeys again ([Security](../operations/security.md)).

## Offline nodes

A node that was offline while the address changed did not receive the new link address. On that node, run:

```sh
mistgate-node set-panel --link-url wss://sub.example.com/<secret-prefix>/
systemctl restart mistgate-node
```

The agent tries the new address and its current one in turn, and keeps the new one once a session over it succeeds ([CLI](../reference/cli.md#mistgate-node-set-panel)).

## MCP

An Admin-profile token can read the state with `subscription_host_get`. It changes the host through `subscription_host_set`, `subscription_host_move` (`start`, `finish`, `cancel`) and `subscription_host_disable`. Every change is a plan that the owner approves with a fresh sign-in. `node_install` takes `subscription_host_domain` and `skip_dns_check` for a first host. See [MCP](../reference/mcp.md).
