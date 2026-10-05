---
title: Client apps
description: "Which app to recommend on each platform, what each one receives from the subscription, the one-tap add links and their fallbacks, and how to set the apps up in the admin and through MCP. kl!ck is the desktop app Mistgate recommends."
---

Mistgate has no client of its own. People connect with existing apps, and the panel hands each app what it reads. This page says which app fits which platform, what each one receives, how the one-tap **Add** button of the [user page](user-page.md) works, and how you set the apps up.

## Which app where

| Platform | Recommended | Also offered |
|:--|:--|:--|
| Windows, macOS | [kl!ck](#klck): Hysteria2 and AmneziaWG in one app | Happ; AmneziaVPN for keys |
| iOS, Android | Happ: Hysteria2 | AmneziaVPN for keys |
| Linux | none by default | AmneziaVPN for keys; any app on the mihomo core you add |

That is the list of a fresh panel (see [A fresh panel](#a-fresh-panel)). You can change it: any app can be added, removed, reordered and marked as recommended.

## What each app receives

One subscription link serves every app. The panel picks the format by the app's User-Agent (the rules of **Subscriptions → Who gets what**, see [Subscriptions](subscriptions.md)).

| App | Format | What is in it |
|:--|:--|:--|
| kl!ck | Mihomo YAML (kl!ck sends `User-Agent: mihomo/…`, which the default rule `mihomo` takes) | Hysteria2 servers and AmneziaWG servers, one server group, a `dns:` section from the person's DNS preset. |
| Clash Verge Rev, FlClash, Clash Meta for Android | Mihomo YAML, when the app's User-Agent contains `clash` or `mihomo` (the two default rules) | The same as for kl!ck. An app that matches no rule gets the link list: add a rule with part of its User-Agent. |
| Happ | Link list (base64) | Hysteria2 servers only: Happ cannot carry AmneziaWG. Happ alone also gets the `Routing` header with the DNS preset and the load in the server names. |
| Hiddify, v2rayNG and other apps of that family | Link list (base64), unless a rule of yours matches them | Hysteria2 servers only. Whether the app reads `hysteria2://` links depends on its version. |
| AmneziaVPN, AmneziaWG apps | No subscription: a key per device (`vpn://`, a `.conf` file or a QR code) from the person's page | AmneziaWG only. |

The AmneziaWG servers reach an app on the mihomo core only when the person has the AmneziaVPN way switched on (**Apps in the subscription** on the user card or the group) and the group has an AmneziaWG profile on a node. The first fetch gives the person's implicit device its AmneziaWG keys. The core must be new enough: the panel lists Mihomo 1.19.30 or newer for AmneziaWG 3.1 and 1.19.14 for 2.0, next to every key ([AmneziaWG](amneziawg.md)).

## One-tap add links

The **Add** button on the user page opens the app with the subscription already in it. It is the app's own link scheme with the subscription link filled into the **"Add" link template** of the app (placeholders `{url}`, `{url_enc}`, `{name_enc}`: see [Subscriptions](subscriptions.md)). When the template is empty, or the app does not understand the link, the page offers **Copy link**: paste it in the app's add-subscription screen.

These are the templates of the known apps. Each scheme is confirmed by the app's own documentation or source code, except Happ's (see the note below).

| App | Platforms | Template |
|:--|:--|:--|
| kl!ck | Windows, macOS | `klick://add?url={url_enc}&name={name_enc}` |
| Happ | iOS, Android, Windows, macOS | `happ://add/{url}` |
| Clash Verge Rev | Windows, macOS, Linux | `clash-verge://install-config?url={url_enc}&name={name_enc}` |
| FlClash | Android, Windows, macOS, Linux | `flclash://install-config?url={url_enc}` |
| Clash Meta for Android | Android | `clashmeta://install-config?url={url_enc}&name={name_enc}` |
| Hiddify | Android, iOS, Windows, macOS, Linux | `hiddify://import/{url}#{name_enc}` |
| v2rayNG | Android | `v2rayng://install-sub?url={url_enc}` |
| AmneziaVPN | all | none: a key per device, not a subscription |

- Happ's own documentation does not describe `happ://add/`. It is the form subscription pages of other panels use, and the one Mistgate has shipped by default. If the button does nothing on a phone, the person copies the link instead.
- Not offered in the picker, because their schemes are not confirmed: Streisand and Shadowrocket (leave the template empty and people paste the link). Not offered at all: sing-box, which opens only a sing-box JSON profile that Mistgate does not serve.
- The `clash://` scheme is claimed by several Clash apps, so the system may open another one. Use the app's own scheme, as above.

## kl!ck

[kl!ck](https://github.com/vbu00/klick) is an open-source desktop client for Windows and macOS by vbu00, written in Rust with Tauri around an unmodified Mihomo core. Mistgate recommends it on the desktop and credits its author: the app is not part of Mistgate.

**Why it is the desktop recommendation.** It reads the Mihomo YAML, the one format that carries both protocols: a person with Hysteria2 and AmneziaWG gets all their servers from the one subscription in the one app, with the DNS section of their preset. On Windows and macOS the alternative is Happ for Hysteria2 plus AmneziaVPN for keys, two apps and two ways to add.

**What the panel needs.** Nothing special: kl!ck fetches the subscription as `mihomo/1.19.31`, so the default rule `mihomo` serves it the Mihomo YAML. The app entry is of the kind **By subscription link**. For the AmneziaWG servers, see the switch above.

**Download.** The releases page: <https://github.com/vbu00/klick/releases/latest>. The Windows and macOS builds are released separately and the file names carry the version, so the panel links the page, not a file. At the time of writing (October 2026) the latest release is 0.4.0 with a Windows installer; the macOS port is merged in the source, and the macOS package appears on the same page when it is released.

**One-tap add, and its limit.** kl!ck handles `klick://add?url=<percent-encoded https link>&name=<optional name>` (also `klick://add/<link>`). It adds nothing by itself: it opens its **Add** screen with the subscription filled in, showing only the domain of the link, and the person confirms. The link must be `https://`. This handler was added to kl!ck after 0.4.0, so **one-tap needs a kl!ck build newer than 0.4.0**. Older builds do not know the scheme: the person copies the link from the page and adds it in kl!ck by hand.

## Set the apps up in the admin

**Subscriptions → User page → Apps on the page** is the list of cards the page shows (see [Subscriptions](subscriptions.md#apps-on-the-page)). To add an app:

1. Under the list choose a **Platform**.
2. Choose a **Known app**: kl!ck, Happ, AmneziaVPN, Clash Verge Rev, FlClash, Clash Meta for Android, Hiddify or v2rayNG. The list holds the apps that exist on that platform. **Custom app, fill in by hand** keeps the old way: you pick the kind (**Takes**) and fill the fields yourself.
3. Press **Add app**. The card is filled with the name, the kind, the download link of that platform and the "add" link template. Edit any field, add a description (80 characters), switch **Recommended** on.
4. Move the card with the arrows: the recommended app leads its platform, and among equals the first in the list does. Press **Save**.

To make kl!ck the desktop recommendation on an existing panel: add it for Windows and for macOS, switch **Recommended** on, and switch it off on Happ for those two platforms. Look at the result in the preview on the same tab.

## A fresh panel

A panel that has not saved its subscription settings yet shows these defaults:

- Windows and macOS: kl!ck first and recommended (the releases page, `klick://add?url={url_enc}&name={name_enc}`), then Happ.
- iOS and Android: Happ, recommended, with `happ://add/{url}`.
- All five platforms: AmneziaVPN for keys (from the store on phones, from the AmneziaVPN site elsewhere).
- Two rules, `mihomo` and `clash`, both to the Mihomo YAML.

The defaults are only what an unsaved panel shows. Once you have saved the settings, the panel keeps your list exactly as saved: an update never rewrites it.

## Through MCP

An agent with Operator access or higher manages the same list with `subscription_settings_get`, `subscription_app_upsert_plan` / `subscription_app_upsert_apply` and `subscription_app_remove_plan` / `subscription_app_remove_apply`. Every change to the page waits for the owner: the owner approves it in **Integrations → Waiting for you**, and the card shows each field in full. A worked example that adds kl!ck for Windows and macOS is in [MCP](../reference/mcp.md#example-add-klck-for-windows-and-macos). The prompt of the [AI agent guide](../getting-started/ai-agents.md) includes the step.
