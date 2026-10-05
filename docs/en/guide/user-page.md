---
title: User page
description: What a person sees when they open their link in a browser, how the page password works, and how the admin previews the page.
---

The user page is what a browser gets at a person's subscription link: a page made for that person, with their status, their traffic and simple steps to connect. It is the page you send people to; they do not need an account or the admin. Its options and the apps it recommends are set on the **User page** tab of **Subscriptions** (see [Subscriptions](subscriptions.md)).

## What the person sees

The greeting uses the optional **Name shown on the subscription page** from the user's settings. Leave it blank to use the account name shown in **Users**. This public name is separate from the account name and does not change the user's access or connection details.

The page speaks Russian or English: the browser's language when it is one of the two, else the panel's default language. A switch in the top bar changes it. The top bar carries the panel's name and logo, and the accent colour of the brand (set in **Settings** → **Interface**).

From top to bottom:

1. **The announcement**, when **Show announcement** is on. The person can close it; a new text shows again.
2. **The greeting and the status**: "Hi, Alice", the status ("Subscription active", "Subscription ended", "Traffic used up", "Subscription disabled"), the days left of the term and the traffic of the period against the quota, with the reset date.
3. **Load**: how busy each server is right now, as a level: **Low**, **Medium** or **High** (from 50% and 80%): the node's CPU use, or its channel as a share of the **Network capacity** in its **Settings** when that is set and busier. A server whose agent sends no fresh sample shows no level. A server is named by its country and location, never by the node's name in the panel; a node with neither is "Server", and a repeated name gets a number. The page never shows the rates themselves: someone who shares a node with one other person must not see when that person streams. When a server is at **High**, the card suggests a calmer one ("The … server's channel is very busy. If your connection is slow, try …").
4. **Your device**: the platform, detected from the browser (iPhone, Android, Windows, Mac, Linux) and switchable when the panel has apps for several.
5. **The ways to connect**, side by side when the person has both ("Either of the two ways will do — or both at once"):
   - **Subscription — Happ** (the names come from the apps list): install the app (with the store or download button and the card's description), then **Add to Happ**, the one-tap link from the app's template. "Won't open? Copy the link" sits right under it. "All your servers (4) appear in Happ — pick any". Other apps of the platform are listed below. A QR code of the link, for connecting a phone, when **QR for a second device** is on. "Connected with the subscription" lists the apps on the link and when they last updated.
   - **AmneziaVPN key**: the person's keys ("Your keys"), and **Add a device** when self-service is on (see below).
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

The steps come from the apps list of the **User page** tab. For the chosen platform, each app of the person's ways gets a card:

- the recommended app leads its way, with a "recommended" badge when there are several;
- the download button names the store when it can (App Store, Google Play), else "website";
- the description of the card, when set;
- the add button from the "add" link template, or "Copy link" with where to paste it ("In Happ: add a subscription → paste the link") when there is no template.

For keys the page gives the AmneziaVPN steps: on a phone "Open AmneziaVPN", "Tap "+" and paste the key", "Tap "Continue""; on a computer "AmneziaVPN → "+" → "Connection settings file" → pick the downloaded file", with the warning to add the file, not the key. "Needs AmneziaVPN 5.0.1.5 or newer" names the oldest client for the key's protocol version.

## Self-service devices

With **Devices on the page** on (the default), a person manages their own AmneziaVPN keys:

- **Add a device**: "Connection option" (the main one is named by the countries of its nodes; a WARP profile is "Spare exit (if some site won't open)"; a 2.0 profile is "For old AmneziaVPN versions (before 5.0.1.5)"), "What kind of device", and "Name (optional)". The key opens at once.
- For each device: **Show key**, **Replace key** (the old one stops working at once) and **Remove** (the VPN on it stops at once).
- The key window: **Country** (one connection per node; "Each country is a separate connection"), **Copy key**, **Download file**, the steps and a QR code. A server is named as on the **Channel utilization** card and in the person's app: by its country and location, never by the node's name in the panel; a node with neither is "Server", and a repeated name gets a number ("Germany 2"). The key's connection in AmneziaVPN carries the same name after the subscription title ("Mistgate · Germany 2").
- When an old key stops working (a critical change of the profile, a new port or address of a node), the device says "new key needed", and **Get a new key** walks the person through replacing it.

Limits: only an active person can add a device, show a key or replace one; the device limit applies ("All slots are used. Remove a device you no longer use, or message us"); and one link may make 20 such requests an hour, showing a key included. With the option off, the page lists the keys and says "Keys come from the admin — message us if you need a new one."

A person whose subscription is not active still sees their AmneziaVPN keys, and can rename and remove them; adding new ones is not possible.

## DNS per server

When the owner turns on **DNS choice on the page** (see below), a person picks the DNS for each server on their page, from the presets the owner offered on that node. The presets and the rule for which DNS applies are in [DNS](dns.md); this section covers what the page does with it.

### What the page knows about a server

For each of the person's servers the page gets:

- the country and location, never the node's name in the panel;
- whether the node is answering (a server that is not shows "not answering");
- its load level: **Low**, **Medium** or **High**, without percentages (see **Channel utilization** above);
- the name the server has in the apps on the link. When the **Server names** template (see [Subscriptions](subscriptions.md)) contains `{node}`, that name is not sent to the page, so the node's name does not leak through it;
- whether a link on the server is for the Mihomo apps only. Gecko obfuscation (see [Hysteria2](hysteria2.md)) is spoken only by kl!ck and the other apps on the Mihomo core, so Happ and the other link-list apps do not get such a connection. The page still lists the server and says "Only in kl!ck and other Mihomo apps" on its card. A person whose every server is like that gets one entry in the link-list apps that says the same, instead of an empty list.

### Choosing and applying

- The choice is made for one server at a time and written to the audit log as `page_dns_choice`: the person's name, the node's name and the preset, never the link or an address.
- It counts against the same limit as the other actions on the page: 20 changes an hour per link.
- Where it works differs by format:
  - **AmneziaVPN and AmneziaWG keys** (`.conf`, `vpn://`, QR code): the DNS is written inside the key, so the person has to press **Get a new key** again. The key itself does not change; the device is marked "new key needed" with the reason DNS until they do.
  - **The AmneziaWG proxies of a Mihomo profile** (Clash Verge Rev, FlClash and the like): the choice applies at the next subscription update.
  - **Not per server**: Hysteria2 in Mihomo and Happ have one resolver for the whole subscription, so they use one DNS for all servers (the user's, the group's or the instance's preset). The person's choice does not reach them. Other apps on the base64 format (v2rayNG, Hiddify, Streisand, Shadowrocket) get no DNS from the panel at all.
- In the owner's preview the choice cannot be changed, like every other action on the page.

### The option

**Subscriptions** → **User page** → **DNS choice on the page** (off by default; a panel set up before the option existed counts as off). Turning it off does not erase the choices people already made: they keep working until you reset them in the user card (see [Users and groups](users-and-groups.md)). The admin warns: "In Happ and for Hysteria2 in Mihomo the DNS is one for all nodes — it works per node only for AmneziaWG keys."

## The preview in the admin

**Subscriptions** → **User page** shows the page in a phone frame next to the settings. **Preview as** picks the user (from the first 50); the reload button refreshes it.

- The preview shows what is saved: save, and it reloads.
- It is the real page of that user, with their real link, served by the admin under `/preview/user-page/<user id>` behind your session. It does not ask for the page password and does not count as a fetch of the link.
- Nothing on it can write: the self-service buttons have nothing to call.
- Only admins who may see users' links can open it: the owner and helpers.

## Who can open the page

Anyone who has the link, plus the password when the page asks for one. The page is marked for search engines not to index, cannot be shown in a frame of another site, and loads nothing from anywhere else. Treat a link like a key: if it leaks, issue a new one from the user card.
