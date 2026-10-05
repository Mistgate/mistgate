---
title: Subscriptions
description: "One link per person for every app: which format each app gets and how the panel decides, how servers are named, what the apps show, and the settings of the subscription."
---

Every person has one subscription link. The same link gives a subscription app its list of servers, gives an app on the mihomo core a ready config, and gives a browser the person's own page. The **Subscriptions** section decides what each app gets and what people see. It has five tabs: **Apps & formats**, **Who gets what**, **Names & texts**, **User page** and **DNS**.

## The link

A link looks like this:

```ini
https://sub.example.com/<secret prefix>/<token>
```

- The base, up to and including the secret prefix, is set by `mistgate setup` on the panel server; **Settings** → **Domains** shows it (to the owner) as **Base of the subscription links**. Without it, no links are handed out. See [Install the panel](../getting-started/install-panel.md).
- The token is the person's own secret. **Subscription link** on the user card shows the link and its QR code; **New link** replaces it, and the old link stops working at once (the page password changes with it).
- A link nobody owns answers like any other unknown path of the site, so a link cannot be found by guessing.

## Formats

| Format | What it is | Who gets it by default |
|:--|:--|:--|
| **Link list (base64)** | One share link per server in a plain list. Happ, v2rayNG, Hiddify and most other apps read it. | Any app without a rule. |
| **Web page** | The person's page: instructions, a QR code, their traffic. See [User page](user-page.md). | Anyone who opens the link in a browser. |
| **Mihomo YAML** | A ready config for Mihomo and Clash Meta with every protocol the person has. | Apps on the mihomo core (Clash Meta, FlClash, Clash Verge), by the rules `mihomo` and `clash`. |
| **Fake 404** | Pretends the link does not exist. Handy to shut out a scraper by its User-Agent. | Nobody, until you add a rule for it. |

The **Apps & formats** tab lists the apps the protocols work with and what each reads: Happ reads the link list, Mihomo / Clash Meta reads the Mihomo YAML, and AmneziaVPN does not read the subscription at all (it takes a `vpn://` key from the person's page).

**Planned:** more formats (Xray JSON, sing-box) and subscription mirrors.

## Who gets what

The **Who gets what** tab is a list of rules, top to bottom. A rule is a piece of text and a format: an app whose User-Agent contains the text (case does not matter) gets that format. The first rule that matches decides. Whatever matches nothing lands on the last line, **Everything else**: a browser gets the page, any app gets the link list.

A fresh panel has two rules: `mihomo` and `clash`, both to **Mihomo YAML**.

- **Add rule**: **User-Agent contains** and **Serve**.
- Drag a rule, or use the arrows, to change the order. The order is saved at once.
- A removed rule can be brought back from the message for a few seconds.
- Up to 50 rules; two rules cannot have the same text.

What counts as a browser: a User-Agent that starts with `Mozilla/`, has a browser engine (`AppleWebKit`, `Gecko/`, `Firefox/`, `Trident/`) and does not name a known VPN app (Happ, v2ray, clash, mihomo, hiddify, sing-box, streisand, shadowrocket, karing, flclash, nekobox, okhttp, cfnetwork). An unusual app that looks like a browser is steered with a rule.

**Try a User-Agent**: paste what an app sends, or pick an example, and the tab shows which rule takes it ("Rule 2 matches → Mihomo YAML", "Nothing matched, and it looks like a browser → web page", "Nothing matched → link list").

## What the apps receive

Every answer to an app carries, besides the servers, the headers subscription apps read:

| Header | What it carries |
|:--|:--|
| `Profile-Title` | The subscription name (**Name in the app**). |
| `Subscription-Userinfo` | Uploaded and downloaded traffic of the period, the quota (0 = unlimited) and the end of the term (0 = never). |
| `Profile-Update-Interval` | How often the app refreshes the link, in hours. |
| `Profile-Web-Page-Url` | The link itself: the app's button to the person's page. |
| `Announce` | The announcement, or the reason the subscription does not work. |
| `Support-Url` | The support link. |
| `Routing` | Happ only: the person's DNS preset as a Happ routing profile. See [DNS](dns.md). |

The Mihomo YAML carries the same headers, one proxy group with every server, and a `dns:` section from the person's DNS preset.

## Names & texts

| Setting | What it does |
|:--|:--|
| **Name in the app** | Shown at the top of the list in Happ. Empty means the panel's name (the brand). |
| **Announcement** | One or two lines above the servers; the user page shows it too. The counter says how much Happ shows: 200 characters. A longer text is cut at a word with "…" for the apps. |
| **Support link** | Opens from the "Message support" button. A Telegram link (`https://t.me/…` or `tg://resolve?domain=…`) or a web link (`https://…`). |
| **Refresh every** | 1–72 hours, default 12: how often the app downloads the subscription again. |

**Server names** is a template built from pieces:

| Piece | Becomes |
|:--|:--|
| `{flag}` | The flag emoji of the node's country. |
| `{country}` | The two-letter code of the node's country (`DE`). |
| `{node}` | The node name. |
| `{profile}` | The profile name. |

The default is `{flag} {country} · {profile}`: "🇩🇪 DE · Hysteria2". Profile names come from the panel, so protocol variants and WARP twins stay easy to tell apart. A number is added only if the full name repeats. A node without a country keeps its profile name; if the name is empty too, it falls back to the node. Happ shows a flag as the server icon only when the name starts with one.

In Happ, a node with a set network capacity also ends its name with its current load: "🇩🇪 DE · Hysteria2 · 64%". Other apps and the Mihomo profile get the name without it: they remember the chosen server by its name, and a name that changes would reset the choice at every refresh.

**How it looks in the app** draws the list as Happ shows it, with the servers of the group most people are in, or with made-up servers when there is none.

## When a subscription does not work

When a person is disabled, past their term or over their quota, the apps do not get an empty list. They get one entry, named after the reason, that never connects, and the announcement is replaced by the same reason, in the panel's default language:

| Reason | Text |
|:--|:--|
| Term ended | "Subscription ended on 29 September. Message us to renew" (without a date when unknown) |
| Over quota, with a reset | "Traffic used up, resets on 1 October" |
| Over quota, without a reset | "Traffic used up. Message us to raise the limit" |
| Disabled | "Access paused" |

In Russian: «Подписка закончилась 29 сентября. Напишите — продлим», «Трафик закончился, обнулится 1 октября», «Трафик закончился. Напишите — увеличим», «Доступ приостановлен».

For such a person the refresh interval drops to 1 hour, so a fix (term extended, quota raised) reaches the app soon. The person's page explains the same in their language.

## The user page

The **User page** tab holds the options of the page and the apps it recommends, with a live preview. It is described in [User page](user-page.md).

### Apps on the page

The page shows one card per app on each platform, in the order of this list. Several apps may share a platform. Each app has:

| Field | What it is |
|:--|:--|
| Platform and kind | iOS, Android, Windows, macOS or Linux; **By subscription link** or **AmneziaWG key**. |
| **App name** | Shown on the card. |
| **Download link** | An `https://` link: a store page or a file. |
| **"Add" link template** | A one-tap link that opens the app with the subscription. Placeholders: `{url}` is the subscription link, `{url_enc}` the same URL-encoded, `{name_enc}` the encoded subscription name. Empty: the page offers to copy the link. Scripting schemes (`javascript:`, `data:` and the like) are refused. |
| **Description on the card** | Plain text, up to 80 characters. Empty: the card says how the app connects. |
| **Recommended** | A badge on the card; the recommended app leads its platform. |

**Add app** takes a platform and a kind (**Takes**). The arrows move an app among the apps of its platform. Up to 30 apps.

A fresh panel recommends Happ on iOS, Android, Windows and macOS with the template `happ://add/{url}`, and AmneziaVPN for keys on all five platforms (from the store on phones, from the AmneziaVPN site elsewhere).

### Options of the page

| Option | Default | What it does |
|:--|:--|:--|
| **Show announcement** | on | The text from **Names & texts**. |
| **Support button** | on | Opens the support link. |
| **QR for a second device** | on | Handy when the link is on a phone. |
| **Password on the page** | on | Every person's page asks for a password, once per device. Apps fetch the subscription without one. |
| **Devices on the page** | on | People add, re-key and remove their own AmneziaWG devices on their page, within their device limit. |

Rules are saved as soon as you add, move or remove one. On **Names & texts** and **User page**, changes wait for **Save** in the bar at the bottom (**Discard** drops them). Either way they apply at the next fetch, without a restart.

## Protection of the links

- **Fetches.** One link answers at most 60 times an hour; more get "too many requests". Fetches within 10 seconds of each other get the same answer.
- **Guessing.** A network (a /24 for IPv4, a /48 for IPv6) that tries 20 unknown links within a minute gets only the fake 404 for 15 minutes, even for valid links.
- **Sharing.** When one link is used from more than 8 networks in a day, the panel writes the event "a subscription link is used from 9 networks a day" and opens the alert "A subscription looks shared". It counts networks, it never stores the addresses. Issue a new link if the old one leaked.

## Who can do what

Every admin can read these settings and try a User-Agent. The owner and helpers change them. The DNS tab is described in [DNS](dns.md).
