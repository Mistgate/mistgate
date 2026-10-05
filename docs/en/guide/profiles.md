---
title: Profiles
description: What a profile is, how the editor works, how a profile is put on nodes with its port and certificate, and which changes break existing users.
---

A profile is a protocol setup with a name: "Hysteria2 · 443 with Salamander and Let's Encrypt", "AmneziaWG 3.1 that looks like DNS". You make it once and put it on as many nodes as you like; each placement is a server on a node. People get a profile through their group. This page covers the **Profiles** section, the profile editor and placing profiles on nodes; the settings of each protocol are on [Hysteria2](hysteria2.md) and [AmneziaWG](amneziawg.md).

## Profiles, nodes and groups

| Thing | What it decides |
|:--|:--|
| Profile | The protocol and its settings: port, certificate, obfuscation, exit. |
| Profile on a node | Where it runs. A node can change the port and the domain (SNI) of a profile for itself. |
| Group | Who gets it. A group is a set of profiles; every user is in one group. |

A person gets a server when all of these hold: the profile is in their group, it is on a node they may use, that profile on the node is switched on and running or starting, the person is active, and an app that the person has switched on can use the protocol. Users and groups are on [Users and groups](users-and-groups.md).

## Protocols are plugins

Each protocol is a plugin of the panel: today Hysteria2 and AmneziaWG. A plugin brings its settings as a schema, and the editor draws the form from it. That is why every protocol's form looks alike:

- fields are grouped into **Basics**, **Obfuscation** and **Advanced**; a group header counts what you changed ("2 changed") and what is wrong ("1 error");
- numbers carry their unit (Mbit/s, s, bytes);
- secrets (the Hysteria2 obfuscation password, the AmneziaWG header protection key) have a **Generate** button. A secret is stored encrypted and never shown again: the field says "Saved on the server, it cannot be shown. Generate a new one to replace it.";
- some fields are critical: changing them breaks the configs people already have. The editor asks before it saves them, see below.

## The Profiles list

Every profile is a card: its name, the protocol, a short line of its settings ("UDP 443 · Salamander · Let's Encrypt"), how many nodes it runs on and how many users get it. Yellow labels say what keeps a profile from anyone: **Runs nowhere** (on no node) and **In no group**. A warning dot says when the profile did not start on a node or UDP is blocked there.

**New profile** opens the editor.

## Creating a profile

1. **New profile**. If the panel has more than one protocol, pick it under **Protocol**. The form starts from the plugin's defaults, with every secret already generated.
2. **Name**. Until you type one, it follows the port: "Hysteria2 · 443". Up to 64 characters, shown in the panel's lists and, through `{profile}` of the name template, in the server names people see (see [Subscriptions](subscriptions.md)).
3. Change the settings you need. The panel checks them as you type; a field with a problem is marked, and the preview waits until it is fixed.
4. **Right after it is created** decides what happens on the click:
   - **Put it on nodes**: every node is ticked, or only the node you came from. For a Let's Encrypt profile without a domain, the block warns about nodes whose address is an IP.
   - **Add it to groups**: the groups that have no profile of this protocol yet are ticked (without one, their people get nothing in that app); groups that already have one are not, because a second profile is usually a test or a variant. With no groups at all, the switch **Create the group "Everyone" with this profile** is offered.
5. **Create profile**. The profile is created, added to the groups, then put on the nodes one by one in the same window. A node that refuses keeps the window open with the reason and a field to fix it (another port, a domain).

The panel on the right, **What the client sees**, shows what a client would get for this profile, with secrets hidden: the share link for Hysteria2 ("Happ · URI list"), the `.conf` for AmneziaWG ("AmneziaVPN · .conf").

## Where it runs

An existing profile's page starts with **Where it runs**: one line that says whether the profile works ("Working: 2 nodes, 1 group, 14 users") or what is missing, with the button that fixes it, then three columns:

- **Nodes**: each node with the port and the state of the profile there, and the agent's error when it failed. **Put on nodes** adds more.
- **Groups**: the groups that carry it; **Edit** opens a group, **Add to a group** adds it to one more.
- **Users**: how many people get it, per group. Only active users whose app switch allows the protocol are counted.

## Putting a profile on nodes

There are two ways, with the same checks:

- from the profile: **Put on nodes**, tick the nodes (or **All nodes**), then **Put on N nodes**;
- from a node: **Profiles** → **Add profile**, pick the profile, then **Add**.

A profile can go on any node of the fleet, including one still waiting for install; not on a retired one. A connected node gets its new state at once and the profile starts within seconds; a node that is offline starts it when its agent is back.

On a node, a profile can have its own:

- **Port**. Empty means the profile's port.
- **Domain (SNI)** (Hysteria2). Empty means the profile's domain, and if the profile has none, the node address when it is a domain.

The first AmneziaWG profile on a node also asks which AmneziaWG backend the node uses, see [AmneziaWG](amneziawg.md).

### Ports and the free-port suggestion

Two profiles cannot listen on the same port of one node, and a port cannot sit inside another profile's port-hopping range. When the port is taken, the dialog puts in a free one at once and says so ("Port 443 is taken by "Main": the free port 8443 is filled in. You can type your own."), and **Take 8443** brings it back if you typed something else. For Hysteria2 the panel tries 8443, 4443, 2053, 2083, 2087 and 2096 first, then random ports between 10000 and 60000; for AmneziaWG only random ones. Hop ranges count as taken.

A port can also be held by a program outside Mistgate. The panel cannot see that before the profile starts: the profile then fails with "Port 443 is taken by another program", the doctor names the process when it can, and **Change port** opens the dialog on a free port.

### Certificates

AmneziaWG has no certificate. A Hysteria2 profile has one of two:

| Certificate | Needs | How the app checks it |
|:--|:--|:--|
| **Let's Encrypt** | A domain whose A record points to the node, and ports 80 and 443 reachable from the internet. | The usual way: the domain and the chain of trust. |
| **Self-signed (pinned)** | Nothing: it works on an IP. | By the certificate's SHA-256 fingerprint (the pin), which the subscription carries. |

The node gets Let's Encrypt certificates by itself and renews them about a month before they expire. It answers the challenge on TCP 443 (where the Hysteria2 profile also serves its masquerade site), and on port 80 when that port is free. A self-signed certificate is made on the node, valid for 10 years; the node makes a new one, with a new pin, when less than 30 days are left or when the domain (SNI) changes. A new pin reaches people with their next subscription update.

Which server name the certificate is for: the node's **Domain (SNI)**, else the profile's **Domain (SNI)**, else the node address if it is a domain. For a self-signed certificate on an IP node the IP goes into the certificate.

A Let's Encrypt profile on a node whose address is an IP, with no domain set, cannot work. The dialog says so before the click and offers two ways out:

- **Enter a domain** whose A record points to the node's IP;
- switch to a self-signed certificate: **Switch "…" to self-signed** when the profile runs nowhere yet, or **Create a self-signed profile for …** when it already runs elsewhere with Let's Encrypt (that profile stays as it is).

> **Note:** the panel says a self-signed certificate is verified with the official Hysteria2 client and with Mihomo, and not yet with Happ. Check it on your phone before you rely on it.

## The check before the click

While you fill in the add-profile dialog (and the per-node edit dialog), the panel asks the server whether the real call would pass: about 300 ms after each change it sends the same request with a "validate only" flag. The server runs every check of the real call and writes nothing. The answer is either a refusal, which the dialog words next to the field it is about and which keeps the button off, or the profile as it would be on that node (its real port and domain for the placeholders), with warnings and a free port.

| Code | When | What the dialog says or does |
|:--|:--|:--|
| `acme_needs_domain` | Let's Encrypt and no domain on an IP node | The Let's Encrypt block with **Enter a domain** and the self-signed ways. |
| `port_taken` | Another profile of the node listens there, or the port lies in its hop range | "Port 443 on de1 is already taken by the profile "Main"." A free port is filled in; **Take N**. |
| `hop_taken` | This profile's own hop range overlaps another profile on the node | The overlap, and **Open the profile**: only changing the range helps. |
| `port_in_hop` | The port lies inside the profile's own hop range | "The port must be outside the profile's hop range (20000–30000)." |
| `sni_needs_domain` | An IP typed into **Domain (SNI)** of a Let's Encrypt profile | "Let's Encrypt needs a domain here, and "…" is not one." |
| `sni_invalid` | Neither a domain nor an IP | ""…" is neither a domain nor an IP address." |
| `already_on_node` | The profile is already on this node | "This profile is already on this node." |
| `node_retired` | The node was retired | "The node is retired from the fleet." |

A warning does not stop the button. Today there is one: `warp_missing`, when the profile exits through WARP and the node has no WARP account, WARP is paused there, or the node's last report says the tunnel is down. The dialog says that the profile will not pass traffic there and links to the node's WARP card.

The same codes come back from the API and the MCP server, as "code: key=value&key=value", so scripts can read them too. See [API](../reference/api.md).

## Errors the editor explains

A field the server refuses gets a sentence next to it. The editor has its own words for these; for any other field and code it shows the server's short English text.

| Field | Code | What the editor says |
|:--|:--|:--|
| Port | `out_of_range` | The port is 1 to 65535 |
| Port hopping, start | `invalid` | The port must be outside the hopping range |
| Port hopping, start | `out_of_range` | The range starts between 1024 and 65535, clear of SSH and other well-known ports |
| Port hopping, end | `too_wide` | The range holds at most 20,000 ports |
| Obfuscation password | `length` | The password is 8 to 128 characters |
| Domain (SNI) | `invalid` | This doesn't look like a domain |

AmneziaWG client networks get the server's text: a network that overlaps another AmneziaWG profile ("overlaps the network of profile …"), or one that cannot change any more ("the client network cannot change once the profile is on a node or has devices").

## Saving changes, and what breaks existing users

The bar at the bottom counts your changes ("Changed: 3"), with **Discard** and **Save**. A saved change goes to every node the profile runs on ("Saved · rolling out to 2 nodes"); the profile restarts there, and people connected through it reconnect by themselves.

Some fields are critical: the node and the app must agree on them, so the configs people already have stop working when they change. Before saving such a change, the editor asks **Apply critical changes?** and says what will happen:

| Protocol | Critical fields | What happens |
|:--|:--|:--|
| Hysteria2 | Port, port hopping, domain (SNI), certificate, obfuscation type and password | The server stops working for every user of the profile until their app updates the subscription (up to the refresh interval, 12 h by default). The window lists the affected users. |
| AmneziaWG | Protocol version, port, client networks, S1–S4, H1–H4, header protection key, random trailers | Every device of the profile stops working and is marked outdated: each needs its config again. The button reads **Change and reissue keys**. |

For a Hysteria2 change the window gives the two ways around it: ask people to update the subscription by hand, or make a copy of the profile on a new port and remove the old one later.

Changing the port of an AmneziaWG profile on one node (in the node's **Profiles** tab) also marks that profile's devices outdated, because their configs hold the old port. So does changing a node's **Address** (in its **Settings**), for every AmneziaWG profile on that node: the configs hold the old address.

If someone else saved the profile while you were editing, saving says "This profile was changed somewhere else" with **Reload**: the panel never overwrites a newer version silently.

## The same server with and without WARP

On the profile page, under **Where it runs**, **WARP copy** (or **Copy without WARP** for a profile that already exits through WARP) makes a twin of the profile with the other exit. The window lists exactly what will be made:

- the name with " · WARP" added (or removed), and a number before it when that name is taken ("Main 2 · WARP");
- a port that is free on every node of the profile (the same candidates as the free-port suggestion above);
- new passwords and keys: nothing secret is copied; an AmneziaWG twin also gets its own client network;
- no port hopping (the two ranges would overlap);
- the same nodes and the same groups. Nodes without a working WARP are listed and unticked for a WARP twin: it would carry nothing there.

Save your changes first: the copy is made from the saved profile. People of those groups then see each node twice in the app. The server names follow the template in **Subscriptions** → **Names & texts**: by default the country, then the profile name, so the twin reads "🇩🇪 DE · Main · WARP" next to "🇩🇪 DE · Main". With a template without `{profile}` the two differ only by a number; see [Subscriptions](subscriptions.md). For AmneziaWG every person who wants the twin needs a separate device (key) for it. WARP itself is on [WARP](warp.md).

## Deleting a profile

**Danger zone** → **Delete profile** removes the profile for good, and groups lose it. A profile that is still on a node cannot be deleted: the window names the nodes. Remove it from those nodes first (node → **Profiles** → **Remove**).

## Who can do what

Creating, changing and deleting profiles, putting them on nodes, removing them and making twins are for the owner. The owner and helpers can add a profile to a group. Every admin can read profiles. See [Security](../operations/security.md).
