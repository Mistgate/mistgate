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

People then have both the node's own server and the one through the entry. To give them only the one through the entry, turn the server off under **In subscriptions** in **Direct** on the exit node's **Ways in** tab (see [Nodes](nodes.md#direct)): it keeps running for the relay.

Accounting stays on the exit: traffic, quotas, speed limits, devices and [torrent protection](torrent-protection.md) work per person as before. The exit sees the entry's address instead of the person's. The traffic crosses the entry twice (from people and to the exit), so the entry's own traffic limit and load count both, and the latency is the person to the entry plus the entry to the exit. MTU does not change.

## Add a relay

1. Update the entry node's agent if **Updates** says so: older agents cannot forward ("The agent of msk1 is too old for relays. Update it on the Updates page."). The server's firewall must allow forwarded traffic; if it drops it (`FORWARD` policy `DROP`, as UFW and Docker set by default) the relay stays down and the row says so.
2. Open the node people cannot reach: **Nodes** → that node → **Ways in**. When the node is dark from the country of the check, the button **Make reachable from Russia…** is on top of the list. **Add a way in…** under the list opens the same window, and so does the grey row **Via another node** ("a spare way, in case the node stops opening directly"). The [IPv6 entry](nodes.md#ipv6-entry) has its own button, **Add IPv6 entry…**.
3. The window **Make reachable from Russia** has four steps: **Node**, **Entry**, **What people get** and **Done**. On **Node** the node you came from is already chosen, with its verdict: "there is a way, adding a spare one", "partly" or "no way". **Next** goes on.
4. On **Entry** pick the node people go through ("Through which node should people go to de1"). The nodes that **Fit** come first; each shows how the country check sees it, whether it reaches de1 (where the TSPU probe runs from it) and its load. A node that already passes servers of de1 is not offered again: add more of them with **Route more servers…** in its lane. An entry in the same country as the exit gains people nothing, because their route to it does not change: it never **Fits**, it carries the hint "Same country as the exit", and you can still pick it.
5. On **What people get** tick the servers to relay. Each line shows the profile and its port on the entry ("tcp/443 on msk1"); a server that cannot be relayed is grey with the reason. The panel picks the port: the exit's own when it is free on the entry, else a free one; **Change port…** in the server's menu changes it later. Under the list the window says who gets what ("12 people get "DE · via RU": 3 servers through msk1, beside the old ones"). When direct does not reach the exit but the exit reaches the entry, it says "Will go through the bridge". **Check and turn on** starts the run; **Back** returns to the choice of the entry.
6. **Done** shows the run ("Checking and turning on · 1 of 3") and a line per server ("checking msk1 → de1 and from outside", then "works"). Both checks of [How it works](#how-it-works) run for each server, and the relay is turned on only when both pass. **Stop** lets the server in flight finish and starts no more. You can close the window: the run goes on, and the result is in the **Ways in** tab ("Not finished" with **Finish it** shows relays that were made but not turned on). A server that fails stays off and nothing is given to people; if its port was taken, its line offers **Take 8444**, and **Another entry…** starts over with another node. At the end: "de1 is reachable through msk1. People get the new servers with the next subscription update."
7. The row of each server shows **Works**, **Off**, **Down**, **Turning on…** or **Checking…**. A working one says how a client sees it ("As a client: 182 ms · 3 min ago"); a down one says why ("Down since 14:20: msk1 cannot reach de1 on 443/tcp." for the leg, "443/tcp on msk1 is not reachable from outside" for the path: the entry's firewall or hoster, or `FORWARD` dropped). The **⋯** menu of the row has **Check now**, **Turn on** or **Turn off**, **Change port…**, **Method…** and **Remove…** (it asks "Really remove?"). **Turn on** needs a check that passed less than 15 minutes ago.

A server the panel has no way to prove is its own is checked as far as it can be: an [MTProto](mtproto.md) server whose Fake TLS falls back to another site has no name of this node to probe. Its row says "On. The panel cannot check it: the server has no name to check it by." instead of a failure, and it can still be turned on. An MTProto server whose Fake TLS falls back to the node's own site is checked like a VLESS one.

You can start from the entry as well: the entry node → **Ways in** → **Whom it carries** → **Carry more nodes…** opens the **Paths** view of **Nodes**, and the buttons there start the same window.

On the exit node, each relay is a lane **Through msk1** on the **Ways in** tab; on the entry node it is a row for the exit in **Whom it carries**. Removing a profile from a node, or retiring a node, also removes the relays that depend on it; the dialog says how many.

## Supervision

Every relay that is on joins the client-eye check rounds as a target of its own: the panel connects through the entry's forwarded port with the exit's profile and credentials, as an app would, and reads 64 KB through it, so a transfer that starts and then stops counts as a freeze.

- The relay row shows the last round: "As a client: 182 ms · 4 min ago", or the stage that failed ("Through the relay: the transfer stalls after the first kilobytes, 4 min ago").
- It also shows the bytes and packets that went through the forward since the entry's agent installed it, and when the agent read them. Older agents without counters show nothing.
- After two failed rounds in a row a `relay_down` alert opens on [Health](../operations/health.md): "Relay de1 via msk1 is down". It names the stage in words: `leg`, the entry does not reach the exit; `entry`, the forward on the entry is not installed; `path`, nothing comes through from outside through the entry; `freeze`, the transfer stalls after the first kilobytes. The alert's **Open the way in** button opens the exit node's **Ways in** tab on that lane.
- The alert gives way while the exit's own check alert or a `node_down` of either node is open, and closes when the relay works again. Telegram sends it too.

The panel's own checks run from the panel's server, outside the entry's country, so they catch a broken leg or a stall on the panel's path, not a freeze that the TSPU applies to people inside the country; for that, look at the bsbord and kl!ck reports on [Reachability](../operations/reachability.md).

## Bridge: when the entry cannot reach the exit

A relay that forwards (**Forwarding**, called direct below) works in one direction: the entry connects to the exit. Some filters cut connections in that direction but let through a connection the exit opens to the entry. The **bridge** uses that. The exit connects to the entry and keeps several encrypted connections open. When a person connects to the entry's port, the entry hands their session to one of those connections, and the exit answers it. The entry does not open the session, as with a direct relay.

The bridge carries TCP only.

The method is set for each relay: **Method…** in the **⋯** menu of its row, or the button **Method: auto** in the **Method** block of the pair (the entry and the exit together). Only the owner can change it, and the open connections of people drop when it changes; their apps reconnect by themselves.

| Way | What it does |
|:--|:--|
| **Auto (recommended)** | Forwarding while it works. When the entry stops reaching the exit, the panel moves to the bridge by itself, and comes back when direct works again. |
| **Forwarding only** | The entry connects to the exit. Nothing switches by itself. |
| **Bridge only** | The exit connects to the entry and keeps the line open. Nothing switches back by itself: only you can change it. |

**What works through the bridge:**

- **VLESS over TCP** (with Vision, and with XHTTP): yes.
- **MTProto**: part of the first version, but not yet proven with real traffic. In the lab, a server with its own site showed its direct path as dead and the bridge as up, yet the panel still refused to switch it to the bridge. The node's site had a self-signed certificate, and the path check needs one it can verify (the lab has no Let's Encrypt). A server whose Fake TLS falls back to another site cannot use the bridge: the panel cannot check its path and refuses cleanly. Telegram's own traffic through the bridge has not been tested.
- **Hysteria2 and AmneziaWG** (UDP): direct only. Putting UDP inside a TCP connection spoils AmneziaWG and breaks Hysteria2's own congestion control, so the panel does not offer the bridge for them.

**How Auto switches:**

- **To the bridge.** The panel checks each relay about every 10 minutes. Two failed checks in a row switch it to the bridge, when the entry does not reach the exit or the transfer freezes after the first kilobytes. The switch needs the bridge to be up and the exit to reach the entry. If the bridge does not hold, a `relay_down` alert opens with the stage `bridge`.
- **Back to direct.** Every 30 minutes the panel checks the direct path with a quick packet test. Three passes in a row, at least 30 minutes apart and at least 30 minutes after the last switch, start a flow test: the entry opens a connection to the exit, and the exit sends 8 MB back. If it passes, the relay goes back to direct. If not, the relay stays on the bridge, the count starts again, and the next flow test waits at least 6 hours. Going back takes at least an hour.
- **If direct fails again soon after.** Within 30 minutes of going back, one failed check is enough to return to the bridge.
- The pair shows the progress: "Forwarding failed at 14:20. We go back to it after 3 successful checks in a row and a speed check (now 2 of 3)".
- Each switch is logged as an event, with no Telegram message.
- A switch drops the open connections of people. Their apps reconnect by themselves.

**Load on the bridge:**

- The bridge holds a few connections, and people share them. Each connection serves about 120 people: about 240 people with two connections, about 480 with four.
- The panel starts with two. It moves to four by itself when the bridge reaches 80 % of the two-connection limit (about 190 people), or when the relay carries 10 Mbit/s or more. It goes back to two after 30 minutes with no reason to keep four, once fewer than about 100 people use it.
- The bridge line shows **Used: N of ~capacity**. From 80 % it says **almost full**. That is information only: no alert and no Telegram message.
- At four connections the bridge cannot grow. New people are turned away within a second, and their apps move to another server. The line then offers **Another entry for {exit}…**: a second entry shares the people.

**Speed check:** the **Check by speed (8 MB)** button sends 8 MB from the exit to the entry, and counts toward the daily limit of speed checks on both nodes. The test uses a separate port of the entry, not the bridge's port. It shows that the line can carry a full flow, not that the bridge itself works.

**When the exit cannot reach the panel:** its agent connects through the bridge and keeps its control session there. The encryption to the panel is end to end, so the entry sees only encrypted bytes. While on the bridge, the agent tests the direct path about every 10 minutes; two passes in a row bring it back to direct. After a restart it first tries direct for about 50 seconds.

Agent updates do not go through the bridge. An exit on the bridge updates from GitHub or Cloudflare, if the panel offers them; by default it offers GitHub. If only the `panel` source is on, such an exit cannot update while its direct path to the panel is closed.

**Limits of the first version:**

- **TCP only.** UDP servers stay direct.
- **The exit sees its own agent, not the person.** For connections through the bridge, the exit sees its agent's address, so their network (ASN) and logs are wrong. Traffic, quotas, speed limits, devices and torrent protection still work per person, because they use the key. The panel marks these connections **through the bridge**.
- **Both nodes need the new agent.** If one is older, the panel names the node to update, and the relay stays direct.
- **Tested in a lab, not on live networks.** The lab simulates a filter. It shows the logic works, not how a real filter treats the bridge. Not yet tested: a session longer than a day; real Mihomo and Telegram clients; older agent versions; VLESS on the node's own site and VLESS with TLS (both need a Let's Encrypt certificate); a second entry at four connections; an exit and an entry that reach the panel in different ways.

## The second server in subscriptions

For each relay that works, a person gets a **twin** of the exit's server right after it in their subscription: the same keys, SNI and certificate pin, with the entry's address and the relay's port. Its name ends with the entry's country code: `🇩🇪 DE · VLESS · via RU` (in a Russian subscription `через RU`, in a Chinese one `经 RU`), cut to fit Happ's 30-character limit. It appears in every format (link list, Mihomo YAML, Xray JSON, sing-box JSON); in a Mihomo group it is one more entry of the same selector.

- **Who gets it:** everyone who may use the exit's server and may use the entry node. There is no new setting; restrict it with the person's node choice.
- **When:** at the subscription's next refresh (12 hours by default). A relay that is off or failed has no twin.
- **What people see:** a second server to pick when the first does not connect. Nothing changes for the servers they already use.
- **MTProto:** a relay passes an [MTProto](mtproto.md) server on as well. The twin is not in the subscription: it is one more link in the person's **Telegram proxy** block, marked **via RU**, with the same key and the same name in the handshake as the exit's.

- **TrustTunnel:** a relay passes a [TrustTunnel](trusttunnel.md) server on over HTTP/2 only: the twin is TCP only and goes to the Mistgate app, not into a `tt://` link.

## AmneziaWG through a relay

A relay passes an [AmneziaWG](amneziawg.md) server on like any other: the entry forwards its UDP port and the exit holds the tunnel. The one difference is the key. The exit remembers one address for each key, so a key used on two paths at once would make the exit jump between them and lose packets. So everyone who is allowed both the exit's server and the entry node gets a **separate key and tunnel address for the path through the entry**; the direct server keeps its own key.

- **Apps on the subscription link** (Mihomo apps, the Mistgate app): the server through the entry, `🇩🇪 DE · AmneziaWG · via RU`, appears right after the direct one at the next refresh of the subscription. The app gets its key then; nobody has to do anything.
- **AmneziaVPN keys:** the path through the entry is a separate config. It is in the device's card (**Show key**, the **Node** list: "de1 · via RU") and on the person's subscription page. It has to be imported into AmneziaVPN as a new server; the direct config stays as it was.
- **Devices:** a key through an entry takes no place in the device limit.
- **Port:** by default the entry first tries the port that suits the profile's look (443 for QUIC) when the entry can use it, then the exit's port, then any free one. After the port of a relay changes, download the AmneziaVPN config again.
- **Removing:** when the last relay of a server through an entry is removed, the keys of that path are revoked by themselves.
- **What the exit sees:** these people come from the entry's address.
- **History:** in a person's connection history, the rows of such a key carry the chip "via RU".

An AmneziaWG server is not in link lists (URI): that format cannot carry it.

## What it cannot pass on

| Server | Why | What to do |
|:--|:--|:--|
| VLESS behind a CDN | People connect to the CDN, not to the node. | Nothing: it does not need a relay. |
| A server whose Let's Encrypt certificate is issued for the node's IP address | Apps would reject that address on the entry. | Use a certificate for a name, or a self-signed one with a pin. |
| A server with a port-hopping range | A relay passes one port. | The twin uses that one port: port hopping does not work through a relay. |

A relay works over **IPv4** only: the entry's forward uses IPv4 on both sides. If the entry's name has an `AAAA` record and an app picks the IPv6 address, it does not connect. Keep only an `A` record for that name, or give the node its address instead of the name.

## With MCP

Admin tokens read and manage relays: `relays_list`, `relay_candidates` and `relay_check` read and test, `relay_plan` / `relay_apply` make, switch, move and delete (always with the owner's approval). Nothing shows a node's address. See [MCP](../reference/mcp.md).
