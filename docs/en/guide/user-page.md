---
title: User page
description: What a person sees when they open their link in a browser, how the page password works, and how the admin previews the page.
---

The user page is what a browser gets at a person's subscription link: a page made for that person, with their status, their traffic and simple steps to connect. It is the page you send people to; they do not need an account or the admin. Its options and the apps it recommends are set on the **User page** tab of **Subscriptions** (see [Subscriptions](subscriptions.md)).

## What the person sees

The greeting uses the optional **Name shown on the subscription page** from the user's settings. Leave it blank to use the account name shown in **Users**. This public name is separate from the account name and does not change the user's access or connection details.

The page speaks English, Russian or Simplified Chinese: the first of them in the browser's language list (any Chinese counts), else the panel's default language. A switch in the top bar changes it, and the choice is remembered on that device. The top bar carries the panel's name and logo, and the accent colour of the brand (set in **Settings** → **Interface**).

From top to bottom:

1. **The announcement**, when **Show announcement** is on. The person can close it; a new text shows again.
2. **The greeting and the status**: "Hi, Alice", the status ("Subscription active", "Subscription ended", "Traffic used up", "Subscription disabled"), the days left of the term and the traffic of the period against the quota, with the reset date.
3. **Load**: how busy each server is right now, as a level: **Low**, **Medium** or **High** (from 50% and 80%): the node's CPU use, or its channel as a share of the **Network capacity** on its **Server** tab when that is set and busier. A server whose agent sends no fresh sample shows no level. A server is named by its country and location, never by the node's name in the panel; a node with neither is "Server", and a repeated name gets a number. The page never shows the rates themselves: someone who shares a node with one other person must not see when that person streams. When a server is at **High**, the card suggests a calmer one ("The … server's channel is very busy. If your connection is slow, try …").
4. **The steps of connecting** (a first visit; the same flow, as a sheet, is **Add a device** later). With both ways the first step is **What to connect with**: two cards, the same as in the "Add a device" sheet. **With an app** is the recommended one and chosen at the start; **With a key (AmneziaVPN)** is the other. With one way only there is no question.
   - **Your device**: the platform, detected from the browser (iPhone, Android, Windows, Mac, Linux) and switchable.
   - **With an app** (the names come from the apps list; only link apps are listed, never a key app): the app, then install it (with the store or download button and the card's description) and **Add to Happ**, the one-tap link from the app's template. "Won't open? Copy the link" sits right under it. "All your servers (4) appear in Happ — pick any". A QR code of the link, for connecting a phone, when **QR for a second device** is on. "Connected with the subscription" says when the apps on the link last fetched it. Opening the page does not update this time, and does not add a device; only an app fetch does.
   - **With a key**: install AmneziaVPN, **Add a device** (the key form opens), paste the key. When self-service is off the step says "Ask for a key".
5. **My devices**: one list with the apps on the link and the person's keys, and one **Add a device** button for both ways (see [My devices](#my-devices)).
6. **Support**: "Something not working?" with a button to the support link, when **Support button** is on and a link is set.
7. A line at the bottom: "This link is personal — don't share it. Works while the subscription is active."

A platform with no app gets "There's no app for … — pick another device above". A person who gets nothing yet sees "The server is still being set up — message the admin."

### When the subscription does not work

A person who is past the term, over the quota or disabled sees the reason instead of the steps, with the support button when there is a link:

| Status | What the page says |
|:--|:--|
| Term ended | "Internet via … is off right now. Message us to renew." |
| Over quota, with a reset | "The traffic for this month is used up — the VPN is off until 1 October. Need it sooner — message us." |
| Over quota, without a reset | "The traffic limit is used up. Message us to raise it." |
| Disabled | "The admin paused access. If that's a mistake — message us." |

The apps show the same reason as the subscription's only entry: see [Subscriptions](subscriptions.md).

## The page password

With **Password on the page** on (the default), the page asks for a password before it shows anything, and the self-service calls need it too. The subscription itself, which apps fetch from the same address, never asks for it: apps cannot type a password.

- The password looks like `k7m2-x9pq`: eight lower-case letters and digits, without the look-alikes 0, 1, l and o. Capitals, spaces and dashes are ignored when it is typed.
- It is not stored anywhere: the panel derives it from the person's link. A new link means a new password, and the old link and password stop working.
- The admin sees it in the link window next to the link (**Page password**, with its own copy button). **Copy to send** copies the link and the password together as one message:

  ```ini
  Your link: https://sub.example.com/<secret prefix>/<token>
  Page password: k7m2-x9pq
  ```

- The page asks once per browser and remembers it for about 180 days (a cookie limited to the link's path).
- Five wrong tries within 10 minutes, for one link or from one network, lock the form for the rest of the 10 minutes ("Too many tries. Try again in 7 min."). The lockout is written to the audit log with the person's name, without the link or the address.
- The form helps with the usual mistakes: a Cyrillic keyboard ("The password is in Latin letters — switch the keyboard to English") and a wrong length ("The password has 8 characters, you typed 7").

Turn the option off on the **User page** tab if you send links only to people you trust with them; then anyone with the link sees the page.

## QR codes

- **In the admin**: **Subscription link** on the user card shows a QR code of the link. Scanned with a phone camera, it opens the page.
- **On the page**: with **QR for a second device** on, the page shows a QR code of the link for another device: beside the steps on a computer, folded under "Connect another device (QR code)" on a phone.
- **For a key**: the key window shows a QR code of the `.conf` to scan in AmneziaVPN or the AmneziaWG apps. A config with a QUIC look is too long for a QR code: "The key is too long for a QR code. Download the file or copy the key."

## Instructions per platform

The steps come from the apps list of the **User page** tab. For the chosen platform, each link app gets a card (key apps never stand among them: the key way has its own steps):

- the recommended app leads its way, with a "recommended" badge when there are several;
- the download button names the store when it can (App Store, Google Play), else "website";
- the description of the card, when set;
- the add button from the "add" link template, or "Copy link" with where to paste it ("In Happ: add a subscription → paste the link") when there is no template.

For keys the page gives the AmneziaVPN steps: on a phone "Open AmneziaVPN", "Tap "+" and paste the key", "Tap "Continue""; on a computer "AmneziaVPN → "+" → "Connection settings file" → pick the downloaded file", with the warning to add the file, not the key. "Needs AmneziaVPN 5.0.1.5 or newer" names the oldest client for the key's protocol version.

## My devices

One card holds everything that takes a slot of the person's device limit:

- **An app on the link that sends a device ID** (Happ, kl!ck and the others in [Client apps](client-apps.md#which-apps-send-a-device-id)): a row of its own and a slot of its own. The row has the name (the model, or the name the person or the owner gave it; else the system, such as "iPhone") and one quiet line: "online", or "updated 5 minutes ago". Press the row to see what the app reported: the system and the app with their versions ("iOS 18.1 · Happ 3.4.2"). The apps that are online come first, then the most recently updated, so the device in the person's hand is at the top. A list of more than four rows folds to three with **Show N more**, except when all slots are used: then the person is looking for the old device to remove.
- **Apps without a device ID** (Hiddify, v2rayNG and other apps that send none, or Happ with HWID switched off): one row, "one slot for all", for all of them together. It cannot be removed or renamed; press it to see the last app. In the **Strict** mode these apps do not connect, and the row says so: "won't connect — use Happ or kl!ck".
- **Keys**: a row per AmneziaVPN key, each one slot, with **Show key** and the **⋯** menu (**Rename**, **Replace key**, **Remove**).
- The counter in the heading ("5 of 10") and, when the person has both ways and a limit, a line saying what takes a slot.

When the limit is full in the **Limit** and **Strict** modes, a quiet line under the heading says "All slots are used. Remove an old device, then refresh the subscription in the app." The app then connects at once. In **Only count** nobody is refused and the line is not shown.

The **⋯** menu of an app on the link has **Rename** (up to 40 characters; the owner sees the name too) and **Remove**. Before removing, the page shows what the app reported, so that two identical phones can be told apart. After the removal the app on that device stops connecting and its slot is free. If it refreshes the subscription again while there is a free slot, it comes back as a new device with new keys. Blocking is not offered on the page: it is for the owner, and a blocked device does not appear in the list. Without **Devices on the page**, and in the owner's preview, the **⋯** menu is not there.

A button beside **Add a device** copies the link in one tap. The person pastes it into every device: each app that sends a device ID takes a slot of its own, the others share one.

There is one **Add a device** button under the list (the "Connect another device" block that used to sit under the card is gone). With both ways it opens a sheet that asks "How will it connect?":

- **With an app** (recommended): the steps of connecting a link app: this device, the app (the link apps of the platform, from the apps list), "Add with one tap" or "Copy link", and a QR code of the page for another device when **QR for a second device** is on. The card says "Takes 1 slot" and how many are free: an app that sends a device ID takes a slot of its own.
- **With a key**: the form of a new key (see below), under the same title and key icon as its card. It takes one slot and says how many are free.

A way the person cannot use stays on the sheet as a quiet card with the reason: all slots used (and **Message** when there is a support link), the keys are issued by the owner, there is no connection option yet, or the owner's preview (which has no address for the calls). The link way keeps working in all of these. With one way only the question is skipped: a person with the link alone goes straight to its steps, one with keys alone to the key form.

## Self-service devices

With **Devices on the page** on (the default), a person manages their own devices: the apps on the link can be renamed and removed (see [My devices](#my-devices)), and the AmneziaVPN keys as follows. All changes together are limited to 20 an hour per link; past that the page says in how many minutes to try again.

- **Add a device** asks first how the device will connect (see [My devices](#my-devices)); the **With a key** way is the form: "Connection option" (the main one is named by the countries of its nodes; a WARP profile is "Spare exit (if some site won't open)"; a 2.0 profile is "For old AmneziaVPN versions (before 5.0.1.5)"), "What kind of device", and "Name (optional)". The key opens at once.
- For each device: **Show key** as the main action, and the **⋯** menu with **Rename**, **Replace key** (the old one stops working at once) and **Remove** (the VPN on it stops at once). Remove and Replace ask first, in the row.
- The key window: **Country** (one connection per node; "Each country is a separate connection"), **Copy key**, **Download file**, the steps and a QR code. A server is named as on the **Channel utilization** card and in the person's app: by its country and location, never by the node's name in the panel; a node with neither is "Server", and a repeated name gets a number ("Germany 2"). The key's connection in AmneziaVPN carries the same name after the subscription title ("Mistgate · Germany 2").
- When an old key stops working (a critical change of the profile, a new port or address of a node), the device says "new key needed", and **Get a new key** walks the person through replacing it.

Limits: only an active person can add a device, show a key or replace one; the device limit applies ("All slots are used. Remove a device you no longer use, or message us"); and one link may make 20 such requests an hour, showing a key included. With the option off, the page lists the keys and says "Keys come from the admin — message us if you need a new one."

A person whose subscription is not active still sees their AmneziaVPN keys, and can rename and remove them; adding new ones is not possible.

## DNS

When the owner turns on **DNS choice on the page** (see below), a person picks on their page:

- **one DNS for the apps on the link** (Happ, the apps on the mihomo core, Xray JSON and sing-box apps), from the presets the owner listed under the switch: a card at the top of **Servers**, named by the apps, "DNS in Happ and other apps that use the link". The apps get the new DNS with their next subscription update, within the refresh interval, and the card says so;
- **the DNS of the AmneziaWG keys of each server**, from the presets the owner offered on that node: a **Key DNS** row on the server's card, with a key icon.

The two never mix: the first does not reach the keys, the second does not reach the apps on the link. The **Key DNS** rows are shown only to a person who uses AmneziaVPN keys (the app is on for them, or they hold a key), on servers that carry keys; a person who uses both reads one line under the card that says the keys keep their own. When the owner offers nothing for the apps on the link, a person with keys reads instead that the apps on the link have one DNS for all servers, and which. The presets and the rule for which DNS applies are in [DNS](dns.md); this section covers what the page does with it.

Preset names and descriptions follow the language of the page, also when it differs from the browser's: a person who switched the page to Russian in an English browser reads the built-in presets in Russian. A preset the owner renamed or made shows its own text in every language.

### What the page knows about a server

For each of the person's servers the page gets:

- the country and location, never the node's name in the panel;
- whether the node is answering (a server that is not shows "not answering");
- its load level: **Low**, **Medium** or **High**, without percentages (see **Channel utilization** above);
- the name the server has in the apps on the link. When the **Server names** template (see [Subscriptions](subscriptions.md)) contains `{node}`, that name is not sent to the page, so the node's name does not leak through it;
- whether a link on the server is for the Mihomo apps only. Gecko obfuscation (see [Hysteria2](hysteria2.md)) is spoken only by kl!ck and the other apps on the Mihomo core, so Happ and the other link-list apps do not get such a connection. The page still lists the server and says "Only in kl!ck and other Mihomo apps" on its card. A person whose every server is like that gets one entry in the link-list apps that says the same, instead of an empty list.

### Choosing and applying

- The choice for the apps on the link is written to the audit log as `page_link_dns_choice`: the person's name and the preset. A choice for the keys of a server is made for one server at a time and written as `page_dns_choice`: the person's name, the node's name and the preset. Neither has the link or an address.
- It counts against the same limit as the other actions on the page: 20 changes an hour per link.
- Where it works differs by format:
  - **AmneziaVPN and AmneziaWG keys** (`.conf`, `vpn://`, QR code): the DNS is written inside the key, so the person has to press **Get a new key** again. The key itself does not change; the device is marked "new key needed" with the reason DNS until they do. The mark follows what the key holds against what applies to the person on that server now, whatever changed it: their pick or going back to the default, the owner changing the offered presets or the default, a preset edited or deleted. The key counts as received only when the person's own device fetches it: an admin opening the key in the panel does not clear the mark. A key made before this was tracked gets no mark until the person fetches it once.
  - **The AmneziaWG proxies of a Mihomo profile** (Clash Verge Rev, FlClash and the like): the choice applies at the next subscription update.
  - **Not per server**: Hysteria2 in Mihomo, Happ, Xray JSON and sing-box have one resolver for the whole subscription, so they use the DNS of the apps on the link: the person's own pick for them, or the usual preset. A choice per server does not reach them. Other apps on the base64 format (v2rayNG, Hiddify, Streisand, Shadowrocket) get no DNS from the panel at all.
- In the owner's preview the choice cannot be changed, like every other action on the page.

### The option

**Subscriptions** → **User page** → **DNS choice on the page** (off by default; a panel set up before the option existed counts as off). With it on, **DNS for the apps on the link** under the switch lists the presets a person may pick for them and the default; the presets each node offers for its keys are set on the node. Turning the option off does not erase the choices people already made: they keep working until you reset them in the user card (see [Users and groups](users-and-groups.md)).

## The preview in the admin

**Subscriptions** → **User page** shows the page in a phone frame next to the settings. **Preview as** picks the user (from the first 50); the reload button refreshes it.

- The preview shows what is saved: save, and it reloads.
- It is the real page of that user, with their real link, served by the admin under `/preview/user-page/<user id>` behind your session. It does not ask for the page password and does not count as a fetch of the link.
- Nothing on it can write: the self-service buttons have nothing to call.
- Only admins who may see users' links can open it: the owner and helpers.

## Who can open the page

Anyone who has the link, plus the password when the page asks for one. The page is marked for search engines not to index, cannot be shown in a frame of another site, and loads nothing from anywhere else. Treat a link like a key: if it leaks, issue a new one from the user card.
