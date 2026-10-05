---
title: DNS
description: What a DNS preset is, how split DNS works, which preset applies to a person, how each app receives it, and when a change reaches people.
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

The editor shows the result under **What each app gets**, for example "Happ: DoH ✓ · Mihomo: DoH ✓ · Amnezia: Plain — a key can only hold IP addresses".

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

## How each app receives DNS

| App | How the preset arrives | What it can carry |
|:--|:--|:--|
| Happ | A routing profile in the `routing` header of the subscription, applied when the app refreshes it. | One resolver for the tunnel and one for direct traffic; plain DNS (port 53 only) and DoH, no DoT. Only the first main server is used. A split works only with **Split domains go direct** on, and then only the split rules with the same servers as the first one (Happ has a single resolver for direct traffic); their suffixes become direct sites. **IPv4 only** is ignored. |
| Apps on the mihomo core | The `dns:` section of the Mihomo YAML. | Plain, DoT and DoH, IPv6 addresses too. The main servers are asked through the tunnel; with **Split domains go direct** on, the split's suffixes are asked at their servers directly and their traffic goes direct. Some Mihomo-based apps ignore the section and use their own. |
| AmneziaVPN and AmneziaWG apps | `DNS =` in the `.conf`, the two DNS fields of the `vpn://` key. | Exactly two plain IPv4 addresses: the first two of the main servers. No split. When the preset has no plain IPv4 server, 1.1.1.1 and 8.8.8.8 are written and the key window says so. |

Without a split, Happ's direct resolver is set to the main one too, so a site a person's own Happ rules send direct still uses the preset (an ad-blocking preset stays ad-blocking).

## When a change reaches people

- **Apps on the link** (Happ, apps on the mihomo core) get it with their next subscription update: within the refresh interval, 12 hours by default. A person can refresh the subscription by hand.
- **AmneziaVPN** only in new keys: the DNS is written into the key, so a person gets the new servers when they import their key again (or a new one).

Every DNS select (the default, a group's, a user's) says the same under it: "When it arrives: the apps on the link (Happ) with the next subscription update; AmneziaVPN only in new keys." The brackets name the link apps of the **User page** tab.

## Node DNS is something else

Two things on the node side also involve DNS, and neither is a preset:

- **DNS resolvers for this node** in the node's **Settings**: the addresses the node and its VPN engines resolve names with, the names in its users' traffic included. The default, **Server's own resolver**, is an empty list: the node uses whatever the server uses (some hosters allow only their own resolvers). **Yandex DNS** (`77.88.8.8`, `77.88.8.1`), **Cloudflare + Google** (`1.1.1.1`, `8.8.8.8`) and **Custom DNS** (IP addresses of your own, up to 8) are choices you make; pick Yandex DNS for a node in Russia, so Russian services such as gosuslugi.ru resolve reliably. See [Nodes](nodes.md) and [Hysteria2](hysteria2.md).
- **The server's system resolver** is the host DNS used by apt and certificate renewal. Doctor checks it and can offer to set it to the node's resolvers or, when the node has none, to Yandex DNS in Russia and Cloudflare + Google elsewhere. See [Health](../operations/health.md).

A preset, on the other hand, goes to the apps in the subscription and is the same on every node the person can use: switching servers does not change it. For example, **Russia: .ru direct** sends Russian domains to Yandex DNS directly and everything else to Cloudflare and Google through the VPN, whichever node the person is on.

## Who can do what

Creating, editing and deleting presets are for the owner: a preset decides which resolvers people's traffic trusts. The owner and helpers choose the default for everyone (it is a subscription setting) and pick a preset for a user or a group. Every admin can see the presets.
