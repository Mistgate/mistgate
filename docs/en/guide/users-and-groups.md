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
- the chips **All**, **Online now**, **Expiring soon** (within 7 days) and **Over quota**, each with its count.

| Column | What it shows |
|:--|:--|
| **Name** | The name; **No access** when the person gets nothing yet (the group's profiles run on no node, or nothing fits the switched-on apps and the chosen nodes). |
| **Status** | **Active**, **Expired**, **Over quota**, **Device limit** or **Disabled**. |
| **App** | What the person actually used in the last 7 days: **Happ**, **Amnezia** or **Both**. |
| **Group** | The group. |
| **Dev.** | Devices in use and the limit. |
| **Traffic** | Used traffic of the current period, with a bar against the quota. |
| **Term** | Days left, ∞ for no expiry. |
| **Last seen** | "now" while online, else how long ago. |
| **Node** | The node of the newest connection. |

**Device limit** is not a separate state: it is an active person whose devices reached the limit. A disabled person shows **Disabled** whatever else is true; then **Expired**, then **Over quota**.

The list loads 50 people at a time; **Show more** loads the next ones.

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
| **Quota** | 100 GB | Steps of 10 GB; 0 is **Unlimited**. Per month, resets on the 1st. |
| **Term** | 30 days | Steps of 5 days; 0 means it never expires. Access pauses afterwards. |
| **Devices** | 5 | 1–100: how many devices can connect. |
| **Apps in the subscription** | what the group gives | The link-app switch and **AmneziaVPN**. Until you touch them, they follow the group: a way the group gives nothing for starts off, with the reason under it. |
| **All nodes** | on | New nodes are added by themselves. Off: tick nodes; new nodes will not be added. |
| **More** → **DNS** | inherited | A DNS preset of the user's own; empty means the group's, or the default. See [DNS](dns.md). |

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

### Traffic

The used traffic of the current period against the quota, the reset period ("resets on the 1st"), a chart of the last 14 days, and the traffic per node and protocol.

### Devices

The counter shows devices in use against the limit. Two lists:

- **Through the subscription**: one row, **Apps on the link**, for every app on the link on every device: together they take one place in the limit. It says when the apps last fetched the link. **Disconnect** cuts them now; they connect again at their next subscription refresh.
- **AmneziaVPN keys**: one row per device with its platform, profile and last connection; **Show key** and **Delete**. **Add device** makes a new key. An **outdated** badge means the device needs its config again. See [AmneziaWG](amneziawg.md).

**Delete** disconnects the device and removes its key from every node.

### Subscription page

**Name shown on the subscription page**: the name the user page greets the person with, up to 64 characters. Empty uses the account name; the line under the field shows the greeting ("Page greeting: …"). **Save name** saves it. Anyone with the link sees this name; it changes nothing else about the user. See [User page](user-page.md).

### DNS per server

Once a person has picked a DNS for a server on their page (see **DNS choice on the page**), this section lists their choices per server (read only, for every admin). **Reset** removes all their choices, so the usual DNS applies again (their preset, the group's, the instance's, or the node's default). The owner and helpers can reset; it is written to the audit log as `user_dns_choices_reset`. Turning the option off does not remove choices already made: they work until you reset them here. See [DNS](dns.md).

### Limits

| Setting | Steps and range |
|:--|:--|
| **Quota** | 10 GB, 0–10000 GB; 0 is **Unlimited**. |
| **Term** | 5 days, up to 3650; ∞ is **Never**. "−" on ∞ sets 30 days. |
| **Devices** | 1, from 1 to 100. |
| **Speed limit** | The switch turns on 20 Mbit/s; then steps of 5 Mbit/s, 5–10000. Experimental, Hysteria2 only. |

A change is saved as soon as you stop clicking.

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

A user made in the admin resets on the 1st of every month (UTC). The API can also set a daily, weekly or rolling 30-day reset, or none; the card shows whichever applies. When the period ends, the used traffic starts again from zero and a person stopped by the quota gets access back by themselves.

## Devices and the device limit

A device is one place in the limit:

- all subscription apps of a person, on all their devices, are one device ("Apps on the link"); apps on the mihomo core that carry AmneziaWG share that one too;
- each AmneziaVPN key is one device.

When the limit is reached, a new key cannot be made, by you or by the person on their page; the subscription keeps working. Raise the limit or delete a device the person no longer uses.

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

Every admin sees users and groups. The owner and helpers create, change and delete users and groups, issue links and keys, pick DNS presets for users and groups and reset a person's DNS choices by server. Only the owner chooses which presets a node offers to people. See [Security](../operations/security.md).
