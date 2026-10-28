---
title: Relays through an entry node
description: When a relay helps people whose network blocks a node abroad, what it does not solve, what the entry node can see, how to add and check one, the second server it adds to subscriptions, and its limits.
---

A **relay** is a port on one node, the **entry**, that passes the encrypted traffic of a server on another node, the **exit**, on to it unchanged. People whose network blocks the exit's address connect to the entry instead; the entry hands their bytes to the exit, and the exit does everything else. Nothing is decrypted on the way: the session still runs from the person's phone to the exit.

The entry node forwards with the kernel (one `nftables` table of its own, `mistgate_relay`, with a DNAT rule per relay), so it costs almost no CPU or memory. It holds no keys of the relayed server.

## When it helps

| What blocks the person | Through a relay |
|:--|:--|
| The exit's IP address or subnet is blocked by their operator or in their region, while the rest of the country reaches it | Helps. People connect to the entry's address; the entry reaches the exit from its data centre. |
| Connections to foreign hosting freeze after a few kilobytes (mostly mobile operators) | Helps if the entry's own network is not on that list and the leg from the entry to the exit does not freeze. |
| A whitelist on mobile networks: only approved addresses open | Only if the entry's address is itself on the list. An ordinary VPS usually is not; check before you promise anything. |
| The exit's IP is blocked in the whole country, including the entry's data centre | No: the leg from the entry to the exit is blocked too. The check before switching on catches this. |
| The entry gets filtered as well (its address, its hoster's range) | No: people cannot reach the entry. The entry's own country check on **Reachability** shows it. |
| A protocol that is cut on the whole path | No: a relay passes the same protocol on, so it cuts there too. |

So a relay is a tool for a block that is **by address or by network, not by country**, and for an entry that people can reach. The dialog shows three hints before you pick: how the country check sees the exit, whether the entry reaches the exit (known when the [TSPU probe](../operations/reachability.md#tspu-probe) runs from that entry), and whether people reach the entry. "No data" means unknown, not fine.

## What the entry's country sees

- **It sees:** the addresses of the people who connect (home and mobile lines are tied to a person there), the time, the amounts, and the address of the exit.
- **It does not see:** sites, contents or keys. The session is encrypted from the phone to the exit. The entry's own operator sees only what any operator sees: connections to an address.
- **A seized entry** yields the table "port → exit address" and whatever the node stores for its own servers. It holds nothing about where people go.

A relay in a country with censorship is under that country's law: its hoster may have to identify you and give access to the server. A relay sees addresses and amounts, not contents. The hoster can also close the server; a relay goes with one click, and nothing on the entry says where people go. This is not legal advice: decide for yourself whether the entry's country is acceptable for you and your users.

## How it works

1. You pick a server of another node and a port on the entry (by default the exit's own port, if it is free on the entry).
2. The panel checks the **leg**: the entry node tries the exit's server (for TCP the port must answer as Mistgate's own server; for UDP the exit counts the packets it receives).
3. The relay is switched on: the entry's agent installs the forward.
4. The panel checks the **path**: a third node (never the entry or the exit; the panel itself only when no node can) reaches the exit's server **through the entry's port**.
5. Only then is the relay *Works*, and only then does the second server appear in subscriptions. A relay that fails either check is switched off again and the dialog says which stage failed.

Accounting stays on the exit: traffic, quotas, speed limits, devices and [torrent protection](torrent-protection.md) work per person as before. The exit sees the entry's address instead of the person's. The traffic crosses the entry twice (from people and to the exit), so the entry's own traffic limit and load count both, and the latency is the person to the entry plus the entry to the exit. MTU does not change.

## Add a relay

1. Update the entry node's agent if **Updates** says so: older agents cannot forward ("Update the node agent to add relays"). The server's firewall must allow forwarded traffic; if it drops it (`FORWARD` policy `DROP`, as UFW and Docker set by default) the relay stays down and the card says so.
2. Open the entry node → the **Relays** block → **Add relay…**.
3. Pick the exit node. The list puts first the nodes the country check sees as blocked or partly blocked, with the networks where they are dark.
4. Tick the servers to relay. Each has a port on the entry (**auto** is the exit's port if free, else a free one). A server that cannot be relayed is grey with the reason.
5. **Check and turn on** runs both checks and switches the relay on. **Only check** runs the leg and adds the relay switched off; turn it on from the block's menu within 15 minutes, or check again (switching on needs a leg check that passed less than 15 minutes ago).
6. The row shows *Works, checked 3 min ago*, or *Not turned on* with the stage: `leg` (the entry cannot reach the exit's server: a block, a route, the exit's firewall), `path` (the port on the entry is not reachable from outside: the entry's firewall or hoster, or `FORWARD` dropped). **Check now** repeats the checks; **Turn off** and **Remove** stop the forward on the entry.

The exit node's page says *Also reachable through …*. Removing a profile from a node, or retiring a node, also removes the relays that depend on it; the dialog says how many.

## The second server in subscriptions

For each relay that works, a person gets a **twin** of the exit's server right after it in their subscription: the same keys, SNI and certificate pin, with the entry's address and the relay's port. Its name ends with the entry's country code: `🇩🇪 DE · VLESS · via RU` (in a Russian subscription `через RU`, in a Chinese one `经 RU`), cut to fit Happ's 30-character limit. It appears in every format (link list, Mihomo YAML, Xray JSON, sing-box JSON); in a Mihomo group it is one more entry of the same selector.

- **Who gets it:** everyone who may use the exit's server and may use the entry node. There is no new setting; restrict it with the person's node choice.
- **When:** at the subscription's next refresh (12 hours by default). A relay that is off or failed has no twin.
- **What people see:** a second server to pick when the first does not connect. Nothing changes for the servers they already use.

## What it cannot pass on

| Server | Why | What to do |
|:--|:--|:--|
| VLESS behind a CDN | People connect to the CDN, not to the node. | Nothing: it does not need a relay. |
| A server whose Let's Encrypt certificate is issued for the node's IP address | Apps would reject that address on the entry. | Use a certificate for a name, or a self-signed one with a pin. |
| A server with a port-hopping range | A relay passes one port. | The twin uses that one port: port hopping does not work through a relay. |
| AmneziaWG | Its twin is a separate key and is not built yet. | Later. |

A relay works over **IPv4** only: the entry's forward uses IPv4 on both sides.

## With MCP

Admin tokens read and manage relays: `relays_list`, `relay_candidates` and `relay_check` read and test, `relay_plan` / `relay_apply` make, switch, move and delete (always with the owner's approval). Nothing shows a node's address. See [MCP](../reference/mcp.md).
