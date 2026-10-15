---
title: DNS
description: What a DNS preset is, how split DNS works, which preset applies to a person, the DNS a person picks for the apps on the link and per server, how each app receives it, and when a change reaches people.
---

A DNS preset decides which DNS servers a person's app asks, and how. The queries travel inside the tunnel, so the person's provider does not see them. The panel ships ready presets (one is the default for everyone), and you can make your own and give one to a user or a group. Presets live on the **DNS** tab of **Subscriptions**.

This is the DNS of the apps. The node has DNS settings of its own: see "Node DNS is something else" below.

## What a preset holds

| Part | What it is |
|:--|:--|
| **Name** and **Description** | One short line of description shows on the card. |
| **Servers** | The main resolvers, asked in this order: the first one that answers wins. Each is a server **From the catalog** or a **Custom server**. |
| **How to ask** | The preferred transport: **Plain**, **DoT** or **DoH**. |
| **Split rules** | Domains that end with given suffixes are asked on their own servers, not the main ones. |
| **Options** | **Split domains go direct, bypassing the VPN** and **IPv4 only**. |

### Servers

A server from the catalog is a provider variant ("Cloudflare", "AdGuard Family", "Yandex Safe"): the panel knows its plain, DoT and DoH addresses and picks one per app. A custom server has a fixed kind and address:

| Kind | Address |
|:--|:--|
| **Plain** | An IP address, with a port if needed: `1.1.1.1` or `1.1.1.1:53`. |
| **DoH** | An `https://` address: `https://dns.example.com/dns-query`. |
| **DoT** | A host name or an IP, with a port if needed: `dns.example.com` or `94.140.14.14:853`. |

### How to ask

Each app gets the preferred transport when it can carry it and the server has an endpoint for it; otherwise the next one down the chain DoH → DoT → plain. Custom servers keep their own kind: an app that cannot carry that kind skips the server. DoH and DoT matter mostly for the sites that go direct (the "direct" split): inside the tunnel the queries are encrypted already.

The editor shows the result under **What each app gets**, for example "Happ: DoH ✓ · Mihomo: DoH ✓ · Amnezia: Plain — a key can only hold IP addresses". The rows for Xray JSON and sing-box are there too: when DoT is preferred, Xray JSON says "Xray JSON has no DoT", and sing-box says "only the first resolver, no fallback" when the preset has more than one.

### Split rules

A split rule is a list of suffixes (type one and press Enter: `.ru`, `gosuslugi.ru`, `.рф`) and the servers for them. Suffixes are matched at a label boundary, and international names are stored as punycode.

**Split domains go direct, bypassing the VPN** decides what the split means:

- **On**: the sites of the split rules are resolved by the split servers and opened without the VPN. This is for people inside Russia, whose local sites work better, or only work, from a local address.
- **Off**: everything goes through the tunnel. Apps that cannot keep a split inside the tunnel (Happ, Mihomo) then ignore the split and ask the main servers for every domain.

### IPv4 only

Ask for A records only, for tunnels where IPv6 does not work. Happ ignores this option.

## The built-in presets

Built-in presets exist on every install. They can be edited, not deleted.

| Category | Preset | What it does |
|:--|:--|:--|
| Russia | **Russia: .ru direct** (the built-in default) | `.ru`, `.su`, `.рф`, Yandex, VK and bank domains are asked at Yandex DNS and open directly, bypassing the VPN; everything else goes through the VPN to Cloudflare and Google. For people inside Russia. |
| Russia | **Russia: all through VPN** | Everything through the VPN, Cloudflare and Google for every domain. For people abroad. |
| Russia | **Yandex DNS** | Yandex DNS for everything. |
| Regular | **Cloudflare + Google** | Cloudflare and Google for everything. |
| Regular | **Cloudflare: no filtering**, **Google: no filtering**, **DNS.SB: no filtering** | One provider, no filtering. |
| No ads | **AdGuard: no ads** | Blocks ads and trackers. |
| For kids | **AdGuard Family: no ads, no 18+**, **Cloudflare Family: no malware, no 18+**, **Yandex Family: no 18+**, **OpenDNS FamilyShield: no 18+** | Blocks adult content (and ads or malware, as the name says). |
| Security | **Quad9: malware blocking**, **Cloudflare Security: malware blocking**, **Yandex Safe: malware blocking** | Blocks known malware and phishing domains. |

**How to choose?** on the tab gives the short answer: people in Russia: **Russia: .ru direct**; people abroad: **Russia: all through VPN**; no ads: **AdGuard: no ads**.

## The DNS tab

- **Default for everyone**: the preset of everyone who has no preset of their own, on the user or on the group.
- **Presets**: one card per preset, with its badges (**built-in**, **default**, **direct** when its split goes direct) and the number of people it applies to. The chips **All**, **Russia**, **Regular**, **No ads**, **For kids**, **Security** and **Own** filter the cards.
- **New preset** opens the editor; a card opens its preset.

In the editor, **Use as the default** → **Make default** makes the preset the default. **Danger zone** → **Delete preset** removes it: the people who used it switch to the default preset with their next subscription update. The default preset and the built-ins cannot be deleted. Leaving with unsaved changes asks first.

## Which preset applies

The first that is set wins:

1. the user's own preset (user card → **Access** → **DNS**, or **More** → **DNS** when creating a user);
2. the group's preset (group form → **More** → **DNS**);
3. the default for everyone (the **DNS** tab);
4. the built-in default, **Russia: .ru direct**.

The user card says which preset applies and where it comes from: "set on the user", "from the group" or "the default preset". In the selects, the empty choice names what it inherits: "Default — Russia: .ru direct", "Like the group — AdGuard: no ads".

This is the rule when the person has picked nothing. The owner can let people pick on their page, in two places that never mix: one DNS for the apps on the link (see "DNS for the apps on the link" below) and a DNS for the AmneziaWG keys of each server (see "DNS per server").

## DNS for the apps on the link

Happ, the apps on the mihomo core, Xray JSON and sing-box apps have one resolver for the whole subscription. The owner can let a person pick it on their page from a list of presets: one DNS for all servers, in all these apps. A typical list: **Cloudflare + Google** as the default, **AdGuard: no ads**, **Russia: .ru direct**.

### Setting it up

**Subscriptions** → **User page**: turn on **DNS choice on the page**, then, right under it, in **DNS for the apps on the link**, tick the presets a person may pick (their order is their order on the page) and mark one as **default**, or leave **No default**. They are saved with the page settings (`link_dns` in the subscription settings); a real change is written to the audit log as `link_dns_options` (how many presets and the default). The owner and helpers can change it, as every other subscription setting. With nothing ticked the page offers no choice for the apps on the link, as before.

### Which DNS a person's link apps have

The first that applies wins:

1. the person's own pick, while it is still in the list;
2. the user's own preset;
3. the group's preset;
4. the default of the list;
5. the default for everyone, then the built-in default.

A preset you set on a user or a group goes before the default of the list: the person keeps your assignment until they pick themselves. Taking a preset out of the list makes the picks of it fall back to rules 2–5; the picks are kept and work again if you offer the preset again. A deleted preset leaves the list and the picks.

### On the page

At the top of **Servers** the person sees a card named by their apps: **DNS in Happ and other apps that use the link** (the app is the one the page recommends on that device). It shows what applies and **Change**, which opens the same list as the DNS of a server; the default is listed first under its name. The apps get the new DNS with their next subscription update, within the refresh interval (12 hours by default); the page says so. A person who also uses AmneziaVPN keys reads one line under the card: the keys are not affected, their DNS is on each server's card.

The pick is written to the audit log as `page_link_dns_choice`: the person's name and the preset, never the link or an address. It counts against the limit shared by all actions on the page: 20 changes an hour per link. Turning **DNS choice on the page** off hides the card and keeps the picks made.

## DNS per server

The DNS of the AmneziaWG keys can differ per server (node): the owner lets people choose it on their page, from the presets the owner picked for that node. Nothing changes until the owner turns it on and picks presets for a node.

### Setting it up

1. **Subscriptions** → **User page** → **DNS choice on the page** (`allow_dns_choice`): the switch. Off by default; a panel whose settings were saved before the option existed counts as off. See [User page](user-page.md).
2. **Node** → the **DNS choice on the user page** block: tick the presets offered to people on this node (their order is their order on the page) and mark one as **default**. Only the owner changes it; the change is written to the audit log (`node_dns_options`). A node with no offered presets gives no choice: everything works as before.

The **DNS choice on the user page** block is not the node's **DNS resolvers for this node**: that is the node's own resolver (see "Node DNS is something else" below), not what people's apps ask.

### Which DNS a person has on a node

The first that applies wins:

1. the person's own choice, when the node still offers that preset;
2. the node's default preset;
3. the usual rule: the user's own preset, then the group's, then the instance's, then the built-in one (see "Which preset applies").

### Where it works

| Format | Per server? | How it arrives |
|:--|:--|:--|
| AmneziaVPN and AmneziaWG keys (`.conf`, `vpn://`, QR code) | Yes | The DNS is written inside the key, so the person presses **Get a new key** on their page again. The key itself does not change; the page marks the device "new key needed" with the reason DNS, whenever the DNS that applies to the person on that server differs from the one in the key (their pick, the owner's offered presets or default, an edited or deleted preset). |
| The AmneziaWG proxies of a Mihomo profile (Clash Verge Rev, FlClash and the like) | Yes | At the next subscription update. |
| Hysteria2 in Mihomo | No | One resolver for the whole subscription: the DNS of the apps on the link (see above). A choice per server does not reach it. |
| Happ | No | The same: one resolver for the whole subscription. |
| Xray JSON and sing-box JSON | No | The same: the DNS of the apps on the link, one for all servers. |
| Other base64 apps (v2rayNG, Hiddify, Streisand, Shadowrocket) | No | They get no DNS from the panel at all. |

The node's block says so: "Happ, the Mihomo apps, Xray JSON and sing-box have one DNS for all nodes: offer it in Subscriptions → User page. Per node it works only for AmneziaWG keys."

### What the person does and what you see

- On their page the row is called **Key DNS**, with a key icon, and it is shown only to a person who uses AmneziaVPN keys (the app is on for them, or they hold a key), on a server that carries keys. A person who uses only the apps on the link does not see it.
- The person picks a preset for each server. The choice is written to the audit log as `page_dns_choice`: the person's name, the node's name and the preset, never the link or an address. It counts against the limit shared by all actions on the page: 20 changes an hour per link.
- On the user card, **DNS picked on the page** lists the person's pick for the apps on the link and their choices per node (read only). **Reset** removes all of them (the owner and helpers may); it is written to the audit log as `user_dns_choices_reset`. See [Users and groups](users-and-groups.md).
- Turning the switch off does not reset the choices already made: they keep working until you reset them on the user card.

## How each app receives DNS

This table is about the preset that applies to the person: for the apps on the link, the DNS of the apps on the link. A per-server choice reaches only some formats: see "DNS per server".

| App | How the preset arrives | What it can carry |
|:--|:--|:--|
| Happ | A routing profile in the `routing` header of the subscription, applied when the app refreshes it. | One resolver for the tunnel and one for direct traffic; plain DNS (port 53 only) and DoH, no DoT. Only the first main server is used. A split works only with **Split domains go direct** on, and then only the split rules with the same servers as the first one (Happ has a single resolver for direct traffic); their suffixes become direct sites. **IPv4 only** is ignored. |
| Apps on the mihomo core | The `dns:` section of the Mihomo YAML. | Plain, DoT and DoH, IPv6 addresses too. The main servers are asked through the tunnel; with **Split domains go direct** on, the split's suffixes are asked at their servers directly and their traffic goes direct. Some Mihomo-based apps ignore the section and use their own. |
| Xray JSON | The `dns` block of every config, and a routing rule `port 53 → dns-out` that hands the DNS queries of the device's apps to it. | Plain DNS (any port) and DoH, no DoT. Every main server, in order: Xray asks the next one when a query fails. The main servers are asked through the tunnel. With **Split domains go direct** on, the split's suffixes are asked at their own servers and their traffic goes direct. Only IPv4 addresses of resolvers are used and only IPv4 answers are asked for, whatever **IPv4 only** says. With no preset that Xray can carry, the config has no `dns` block and the app uses its system resolver. |
| sing-box JSON | The `dns` block of the profile, and `hijack-dns`, which hands the DNS queries that reach the tunnel to it. | Plain DNS, DoT and DoH. One main resolver: the first of the preset, no fallback; it is asked through the tunnel. With **Split domains go direct** on, the split's suffixes are asked at their own resolver directly and their traffic goes direct. IPv4 only. With no preset that sing-box can carry (or no DNS module), the profile uses a local fallback: the system resolver, which resolves outside the tunnel. |
| AmneziaVPN and AmneziaWG apps | `DNS =` in the `.conf`, the two DNS fields of the `vpn://` key. | Exactly two plain IPv4 addresses: the first two of the main servers. No split. When the preset has no plain IPv4 server, 1.1.1.1 and 8.8.8.8 are written and the key window says so. |

Without a split, Happ's direct resolver is set to the main one too, so a site a person's own Happ rules send direct still uses the preset (an ad-blocking preset stays ad-blocking).

## When a change reaches people

- **Apps on the link** (Happ, apps on the mihomo core, Xray JSON and sing-box apps) get it with their next subscription update: within the refresh interval, 12 hours by default. A person can refresh the subscription by hand.
- **AmneziaVPN** only in new keys: the DNS is written into the key, so a person gets the new servers when they import their key again (or a new one).

Every DNS select (the default, a group's, a user's) says the same under it: "When it arrives: the apps on the link (Happ) with the next subscription update; AmneziaVPN only in new keys." The brackets name the link apps of the **User page** tab.

## Node DNS is something else

Two things on the node side also involve DNS, and neither is a preset (the **DNS choice on the user page** block of a node only chooses which presets people may pick: see "DNS per server"):

- **DNS resolvers for this node** in the node's **Settings**: the addresses the node and its VPN engines resolve names with, the names in its users' traffic included. The default, **Server's own resolver**, is an empty list: the node uses whatever the server uses (some hosters allow only their own resolvers). **Yandex DNS** (`77.88.8.8`, `77.88.8.1`), **Cloudflare + Google** (`1.1.1.1`, `8.8.8.8`) and **Custom DNS** (IP addresses of your own, up to 8) are choices you make; pick Yandex DNS for a node in Russia, so Russian services such as gosuslugi.ru resolve reliably. See [Nodes](nodes.md) and [Hysteria2](hysteria2.md).
- **The server's system resolver** is the host DNS used by apt and certificate renewal. Doctor checks it and can offer to set it to the node's resolvers or, when the node has none, to Yandex DNS in Russia and Cloudflare + Google elsewhere. See [Health](../operations/health.md).

A preset, on the other hand, goes to the apps in the subscription and is the same on every node the person can use, unless the person chose a DNS for the keys of a server (see "DNS per server"): switching servers does not change it. For example, **Russia: .ru direct** sends Russian domains to Yandex DNS directly and everything else to Cloudflare and Google through the VPN, whichever node the person is on.

## Who can do what

Creating, editing and deleting presets are for the owner: a preset decides which resolvers people's traffic trusts. The owner and helpers choose the default for everyone and the presets offered for the apps on the link (both are subscription settings) and pick a preset for a user or a group. Every admin can see the presets. Choosing the presets a node offers is for the owner; resetting a person's choices on the page is for the owner and helpers; every admin can read them. MCP tokens cannot do any of this.
