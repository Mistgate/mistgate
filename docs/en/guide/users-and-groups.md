---
title: Users and groups
description: How to create and manage users (limits, term, traffic, devices, access), what a group gives, and how to move people between groups.
---

A user is a person who gets access. Each user is in one group, and the group decides which profiles they get. On top of that, every user has their own limits (traffic quota, term, device limit), their own app switches and their own choice of nodes. The **Users** section has two tabs: **People** and **Groups**.

## What a person gets

A person gets a server when all of this holds:

- the person is active (not disabled, not past their term, not over their quota);
- the profile is in the person's group;
- the profile is on a node the person may use (all nodes, or the ones picked for them);
- that profile on the node is switched on and running or starting;
- one of the person's app switches allows the protocol.

Any change to these is pushed to the nodes at once.

![An example: Alice is active, in the group Family with the profiles Hysteria2 and AmneziaWG, with the Happ switch on and AmneziaVPN off; she gets Hysteria2 on de1, not Hysteria2 on nl1, which is switched off there, and not AmneziaWG, which none of her apps reads](../diagrams/who-gets-what.svg)

## Two ways to connect

| | By subscription link | By AmneziaWG key |
|:--|:--|:--|
| Apps | The subscription apps (Happ and other apps that read share links, apps on the mihomo core) | AmneziaVPN and the AmneziaWG apps |
| Protocol | Hysteria2 (and AmneziaWG for apps on the mihomo core) | AmneziaWG |
| What the person adds | One link for every device | A key of its own on each device |
| Updates | The app refreshes the link by itself | A key is imported again by hand |
| Switch on the user | the switch named after the link apps ("Happ", or the app names set in Subscriptions) | **AmneziaVPN** |

Both are offered on the person's page side by side. At least one switch stays on. See [Subscriptions](subscriptions.md), [AmneziaWG](amneziawg.md) and [User page](user-page.md).

## The People tab

The header counts people and how many are online. Above the list:

- **Name or group**: a search by the user's name or group name;
- the chips **All**, **Online now**, **Expiring soon** (within 7 days), **Over quota** and **Over device limit**, each with its count. **Over device limit** shows only while someone is over their limit (or while it is selected): it is the list to look at before you switch the [device limit](subscriptions.md#device-limit-for-apps-on-the-link) from **Only count** to **Limit**.

| Column | What it shows |
|:--|:--|
| **Name** | The name; **No access** when the person gets nothing yet (the group's profiles run on no node, or nothing fits the switched-on apps and the chosen nodes). |
| **Status** | **Active**, **Expired**, **Over quota**, **Device limit** or **Disabled**. |
| **App** | What the person actually used in the last 7 days: **Happ**, **Amnezia** or **Both**. |
| **Group** | The group. |
| **Dev.** | Devices in use and the limit: amber when the limit is full, red ("6/5") when the person is over it. |
| **Traffic** | Used traffic of the current period, with a bar against the quota. |
| **Term** | Days left, ∞ for no expiry. |
| **Last seen** | "now" while online, else how long ago. |
| **Node** | The node of the newest connection. |

**Device limit** is not a separate state: it is an active person whose devices reached the limit. A disabled person shows **Disabled** whatever else is true; then **Expired**, then **Over quota**.

The list is paged, 50 people to a page (25 or 100 on request): under the card there is "Showing 51–100 of 137", the page buttons and the rows per page. A filter or a search pages its own results; the page number is kept in the address.

### Bulk actions

Tick people in the list (or all on the page) and a bar appears: "Selected: 3".

- **Extend 30 days** moves each term 30 days forward from the later of today and the current end. People without an expiry date are skipped: extending would give them one.
- **Disable** cuts the people off at once. The message has **Undo** for a few seconds.
- **Cancel** clears the selection.

The API also enables, deletes and resets the traffic of many users in one call. See [API](../reference/api.md).

## Creating a user

**New user** opens the form:

| Field | Default | What it is |
|:--|:--|:--|
| **Name** | | Required, unique (case does not matter), up to 64 characters. |
| **Group** | "Everyone" if there is such a group, else the first | The line **Gets** under it says what a person of this group gets: the subscription and AmneziaVPN keys, on how many nodes. A red line says when they would get nothing. **Edit group** opens the group. |
| **Quota** | 100 GB | Steps of 10 GB; 0 is **Unlimited**. The hint under it names the period. |
| **Resets** | **Monthly** | **Daily**, **Weekly**, **Monthly** or **Never**. See [Quota periods](#quota-periods). |
| **Term** | 30 days | Steps of 5 days; 0 means it never expires. Access pauses afterwards. |
| **Devices** | 5 | 1–100: how many devices can connect. |
| **Apps in the subscription** | what the group gives | The link-app switch and **AmneziaVPN**. Until you touch them, they follow the group: a way the group gives nothing for starts off, with the reason under it. |
| **All nodes** | on | New nodes are added by themselves. Off: tick nodes; new nodes will not be added. |
| **More** | folded | **DNS**: a preset of the user's own, inherited when empty (see [DNS](dns.md)). **Speed limits**: **Speed per person** and **Speed per device**, both unlimited by default (see [Limits](#limits)). |

When there is no group yet, the form shows the group form inline. **Create** makes the user and opens the link window; the link (and the page password, when pages ask for one) is copied for you to send. If the person would get nothing yet, the window says why before the link goes out.

Gigabytes are decimal: 100 GB is 100,000,000,000 bytes.

## The user card

The header shows the name and status, the apps, the group and the term ("until 12 October · 14 d"), with three buttons: **Disable** (or **Enable**), **Extend 30 days** (off for a user without expiry) and **Subscription link**.

Under it, a strip explains a stop and offers the quick fix:

| Stop | Strip | Button |
|:--|:--|:--|
| Term ended | "The term ended 3 days ago — access is paused." | **Extend 30 days** |
| Quota used up | "The 100 GB quota is used up — access is paused until it resets or you raise it." | **+50 GB** |
| Device limit | "Device limit (5) reached — a new device can't connect." | **+1 device** |

In the **Only count** mode nothing is refused, so this strip is not shown. A person over the limit gets a quiet line under the heading of **Devices** instead: "The limit only counts for now: apps over it still connect", with a link to **Limit mode**.

### Traffic

The used traffic of the current period against the quota, the reset period ("resets every week") and when the next reset comes, in your time, a chart of the last 14 days, and the traffic per node and protocol: the five biggest rows, **Show all** for the rest.

### Devices

The heading shows the devices in use against the limit ("3 of 5"): amber when it is full, red when the person is over it. The panel counts devices, one place each; what a device is and how it appears is in [Devices and the device limit](#devices-and-the-device-limit). The card has these parts:

- **Apps on the link**: a row for every app that sends a device ID. The row shows the name (the model, or the name you gave it) and a quiet line: "online" with the servers it is connected to ("de1, nl1"), or how long ago the app fetched the link. Hover the row for the system and the app with their versions ("iOS 18.1 · Happ 3.4.2"); the **⋯** menu also shows since when, the traffic of the device over all time and its fingerprint. One device on two servers at once is a sign that its config was copied to another device; the row says so on hover.
- **Apps without a device ID**: one row for every app on the link that does not send one (Hiddify, v2rayNG and the like, or Happ with HWID switched off). They share one place. In the **Strict** mode there is no such place and these apps cannot connect.
- **AmneziaVPN keys**: one row per device with its platform, profile and last connection; **Show key** and, in the menu, **Delete…**. **Add device** makes a new key. An **outdated** badge means the device needs its config again. See [AmneziaWG](amneziawg.md).
- **Blocked (N)**: folded; see below.

The **⋯** menu of an app on the link:

- **Rename…**: a name of your own, shown here and on the person's page.
- **Delete…**: the app disconnects, its keys stop working and the place is free. If the app fetches the link again and there is a place, it comes back as a new device with new keys.
- **Block…**: as **Delete…**, and the device ID cannot register again, even when the app fetches the link again, until you unblock it. The person's other devices keep working. Only an app with a device ID can be blocked; the shared place and AmneziaVPN keys cannot. The device ID is what the app reports: an app that reports another one is a new device and takes a free place like any other. If the link was passed to someone else, block their device and issue a **New link** as well.

**Blocked (N)** lists the blocked devices. **Unblock** lets the device in again; it gets new keys at its next refresh of the subscription. The menu of the shared place has **Disconnect…**: the apps lose the connection now and connect again at their next refresh.

**Delete** on an AmneziaVPN key disconnects the device and removes its key from every node.

### Subscription page

**Name shown on the subscription page**: the name the user page greets the person with, up to 64 characters. Empty uses the account name; the line under the field shows the greeting ("Page greeting: …"). **Save name** saves it. Anyone with the link sees this name; it changes nothing else about the user. See [User page](user-page.md).

### DNS picked on the page

Once a person has picked a DNS on their page (see **DNS choice on the page**), this section lists their pick for the apps on the link and their choices per server (read only, for every admin). A pick that is no longer offered says what applies instead. **Reset** removes all their choices, so the usual DNS applies again (their preset, the group's, the default of the list for the apps on the link or the node's default, the instance's). The owner and helpers can reset; it is written to the audit log as `user_dns_choices_reset`. Turning the option off does not remove choices already made: they work until you reset them here. See [DNS](dns.md).

### Limits

| Setting | Steps and range |
|:--|:--|
| **Quota** | 10 GB, 0–10000 GB; 0 is **Unlimited**. |
| **Resets** | **Daily**, **Weekly**, **Monthly** or **Never**; saved as soon as you pick. A person given a rolling 30-day period through the API or MCP also has **Every 30 days**. |
| **Term** | 5 days, up to 3650; ∞ is **Never**. "−" on ∞ sets 30 days. |
| **Devices** | 1, from 1 to 100. |
| **Speed per person** | Steps of 5 Mbit/s, 0–10000; 0 is **Unlimited**. All the person's devices together, on one node. Experimental. |
| **Speed per device** | The same steps. Each device on its own. Experimental. |

A change is saved as soon as you stop clicking.

The speed limits apply to Hysteria2 and VLESS. AmneziaWG applies neither.

- **Speed per person** is the total of the person's devices on **one node**. A person connected to two nodes at once can get up to twice the limit, because the nodes do not coordinate. This is the former single speed limit: before devices had keys of their own, all the apps shared one credential, so it was already a limit per person.
- **Speed per device** caps each device.
- Both can be set. A byte passes both limits, so a device runs at the smaller of its own limit and what is left of the person's.
- Sharing the person's limit needs an agent that reports `user-rate-limit/1`. A node with an older agent holds each device to the smaller of the two limits, and the Agent card on the node's **Server** tab says so until the agent is updated (see [Nodes](nodes.md#agent-version-and-drift)).

### Access

- **Group**: picking another group first asks the server what changes and shows it: "Alice loses: "AWG 3.1" (2 AmneziaVPN keys stop working)", "Gets: …". **Change group** confirms. An AmneziaVPN key belongs to one profile, so the keys of a lost profile stop working.
- The group's profiles, in the two ways, each a link to its page.
- **DNS**: the user's own preset, or empty for the group's or the default. The line under it says which applies and where it comes from.
- The app switches.
- **All nodes**, or per-node checkboxes. A node where nothing fits the person is greyed out; "Hysteria2 only" marks a node with one protocol. A node's name opens its **Profiles** tab, where its own port and domain are set.

### Danger zone

- **Reset traffic** sets the used traffic of the current period to zero. The period and its end stay, the history stays, and a person stopped by the quota can connect again. The counter cannot be restored.
- **Delete user** removes the person for good: their keys are dropped from every node, their connections are cut, the link stops working and their traffic history is deleted. Type the name to confirm.

## Quota periods

The quota starts again at 00:00 UTC: every day, every Monday or on the 1st of every month. **Never** keeps counting for the whole term; only **Reset traffic** sets it to zero. A new user resets every month unless you pick another period; the API and MCP can also set a rolling 30-day period. The card shows the period and the next reset. When the period ends, the used traffic starts again from zero and a person stopped by the quota gets access back by themselves.

## Devices and the device limit

A device is one place in the limit.

- An app on the link that sends a device ID (HWID) is one device per ID. Which apps do: [Client apps](client-apps.md#which-apps-send-a-device-id).
- All apps on the link that send no device ID share one place, **Apps without a device ID**.
- Each AmneziaVPN key is one device.

### How a device appears

- A device is created by the first fetch of the subscription by an app, one per device ID. Opening the user page, the admin's preview and creating a user never create one: a new person shows "0 of 5" until an app fetches the link, and the first connection waits a few seconds until the nodes have the new keys.
- Each device has its own keys for Hysteria2, VLESS and AmneziaWG in Mihomo, its own online status and its own traffic. A config copied to another device keeps working until you delete or block that device, but it shows (one device on two servers at once), and removing it leaves the person's other devices alone.
- The device ID itself is not stored: the panel keeps a hash of it, mixed with the person, so the same phone in two subscriptions is not linked. The card shows the first six characters of the hash as a fingerprint, to tell two identical phones apart.
- On upgrade nobody has to reconnect. The first app with a device ID takes over the person's old shared device and keeps its keys. Other apps with an ID get keys of their own at their next refresh. Apps without an ID stay on a shared place.

### The limit and the mode

What happens when the limit is full depends on the mode in **Subscriptions → Who gets what → Device limit for apps on the link**: **Only count**, **Limit** or **Strict**. See [Subscriptions](subscriptions.md#device-limit-for-apps-on-the-link). In short: **Only count** refuses nobody, **Limit** refuses a new device over the limit, **Strict** also refuses apps without a device ID.

- A link device that stays silent for 30 days gives its place to a new app with a device ID, when a place is needed. This happens in **Limit** and **Strict** only. An AmneziaVPN key is never replaced. The event "a new device took the place of one silent for 30 days" is written.
- A key cannot be made when the limit is full, by you or by the person on their page. Raise the limit or delete a device the person no longer uses.
- A device you delete is cleaned up from the database after 90 days. A blocked device is kept, so the block holds.
- The refused app is told why: see [What the app shows when a device is refused](subscriptions.md#what-the-app-shows-when-a-device-is-refused).

A person can also delete and rename their own devices on their page, when **Devices on the page** is on; see [User page](user-page.md#my-devices). Blocking is for the owner and helpers.

## The Groups tab

A group is a set of profiles that its people get. Each group is a card: its name, how many people are in it (a link to the filtered list), its profiles in the two ways with the number of nodes where they run, and its DNS preset when it has one. **Edit** and **Delete** are on the card.

With no group at all, the tab offers **Create "Everyone" with all profiles** and **New group**.

### Creating and editing a group

- **Name**, up to 64 characters.
- **Profiles in the group**: every profile is ticked for a new group; untick what the group should not give.
- **More** → **DNS**: the group's preset; empty means the instance default.

Editing a group changes access for everyone in it ("3 users are in this group; the change applies to all of them"). Taking a profile away from a group with people first shows what they lose, with the AmneziaVPN keys that stop working, and the button turns into "Take it away from 3 people".

Profiles can also be added to a group from the profile's page (**Add to a group**).

### Moving people between groups

- One person: user card → **Access** → **Group**, with the preview of what they lose and get.
- Everyone of a group: delete the group and move its people (below). There is no bulk group change in the list.

### Deleting a group

- An empty group goes at once: its profiles stay, only the group is removed.
- A group with people asks where they go ("3 people are in the group. Where to move them?"), with "Everyone" preselected when it exists. **Move 3 people and delete** moves them and deletes the group in one step.
- With no other group, there is nowhere to move them: make another group first.

## Who can do what

Every admin sees users and groups. The owner and helpers create, change and delete users and groups, issue links and keys, pick DNS presets for users and groups and reset the DNS a person picked on their page. Only the owner chooses which presets a node offers to people. See [Security](../operations/security.md).
