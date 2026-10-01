---
title: First users
description: The shortest path from a panel with one connected node to a person with a working connection.
---

A person gets a working connection after four things exist: a connected node, a profile on that node, a group that has the profile, and a user in that group. This page walks through them in that order. It assumes you have already [added a node](add-node.md).

## The first-run checklist

Until all four exist, the Overview shows a **First run** card with the same four steps:

1. **Node connected**
2. **Profile on the node**
3. **Group with profiles**
4. **User and link**

Each step ticks itself from what the panel already has; you never tick anything by hand. A step that is not done has a button that takes you there, and the next step's button is highlighted. When everything is done, the card folds into one line, "All set: a node, a profile, a group and a user", which **Hide** removes (on this device only).

## 1. Create a profile and put it on the node

Open **Profiles → New profile**. You can also start from the node's page: **Profiles → Add profile**.

1. **Protocol.** Pick one:
   - **Hysteria2** for the subscription apps: a subscription app such as Happ, or an app on the mihomo core such as Clash Verge or FlClash. One link carries every server.
   - **AmneziaWG** for AmneziaVPN. Every device gets a key of its own.
2. **Name.** Until you type one, it follows the port, for example "Hysteria2 · 443".
3. **Settings.** The defaults work for a start. For Hysteria2 look at one field:
   - **Certificate: Let's Encrypt** (the default) needs a domain whose A record points at the node, with TCP 80 and 443 open on it. The node's address is used, or the profile's **Domain (SNI)** when you fill it in.
   - **Certificate: Self-signed (pinned)** works with an IP address; the app checks the certificate's fingerprint, which travels in the subscription.

   Hysteria2 starts on UDP port 443 with Salamander obfuscation and a generated password. An AmneziaWG profile starts as version 3.1 on a random UDP port with generated obfuscation.
4. **Right after it is created.** This block at the bottom does the next steps in the same pass:
   - **Put it on nodes**: your node is already selected.
   - **Add it to groups**: on a new panel the group "Everyone" is ticked, because it has no profile of this protocol yet.
5. Press **Create profile**. The panel says "Profile created and put on 1 node".

The node starts the server within seconds. Its **Profiles** tab shows the port, the Domain (SNI) and the certificate. A Let's Encrypt certificate is fetched on the node in the background and appears there with its expiry date once it is issued.

The form warns you before you create the profile when Let's Encrypt cannot work on a selected node: "the address is an IP, Let's Encrypt does not work there". Pick the self-signed certificate or fill in the Domain (SNI) in that case. Details: [Profiles](../guide/profiles.md), [Hysteria2](../guide/hysteria2.md), [AmneziaWG](../guide/amneziawg.md).

## 2. Check the group

Open **Users → Groups**. The setup wizard created the group "Everyone"; if you kept the tick in step 1, the profile is already in it. Otherwise open the group, tick the profile under **Profiles in the group** and save. From the profile's page, **Where it runs → Add to a group** does the same.

A group without profiles gives no access at all. More: [Users and groups](../guide/users-and-groups.md).

## 3. Create the user

Open **Users → New user**:

| Field | Default | Meaning |
|:--|:--|:--|
| Name | | How the person appears in the admin, for example "Alice". |
| Group | | Where the person's profiles come from. |
| Quota | 100 GB | Traffic per month, reset on the 1st. 0 means unlimited. |
| Term | 30 days | Access pauses afterwards. 0 means no end. |
| Devices | 5 | How many devices can connect. |
| Apps in the subscription | what the group gives | The subscription apps and AmneziaVPN, each a switch. A switch is off when the group has no running server for that app. |
| All nodes | on | Every node, including nodes added later. Turn it off to pick nodes. |
| More → DNS | the group's preset | A DNS preset for this user only. See [DNS](../guide/dns.md). |

Press **Create**. The panel says "Alice created · link copied": the subscription link is already on your clipboard.

## 4. Send the link

The user's page has a **Subscription link** button. It shows the link and its QR code, with **Copy the link**, **Copy to send**, **Share**, **Open the page** and **New link**. Send the link in a private message: it opens a page with simple instructions.

What the person does:

1. Opens the link in a browser on the phone or computer they want to connect. Their page shows instructions for that platform, the apps to install, a button that adds the subscription to the app, a QR code for another device, and their traffic and term.
2. **Subscription app:** installs the app and adds the subscription with the button on the page, or by pasting the link into the app. The app fetches the server list and refreshes it from the same link later.
3. **AmneziaVPN:** makes a key for the device on the same page, when **Devices on the page** is on in **Subscriptions → User page**, and imports it into AmneziaVPN. When it is off, you make the key in the admin and send it.

If the page asks for a password, the page password is on: **Copy to send** puts the link and the password into one message. The page asks for it once per device; apps never need it.

How you know it works:

- The user's **Devices** list shows a device as soon as an app has fetched the link ("None yet — they appear once your friend opens the link" until then).
- The node's page and the Overview show the person online, and traffic starts to count.
- If the link window says the person "gets nothing yet", the group has no profile running on a node the person may use: go back to steps 1 and 2.

> **Note:** **New link** replaces the old link at once; whoever had the old one needs the new one. Do it when a link was shared further than you wanted, and delete the devices you do not recognise in the user's **Devices** list.

## Next

- [Subscriptions](../guide/subscriptions.md) and [User page](../guide/user-page.md): what each app gets and what the person sees.
- [Health](../operations/health.md): the client-eye checks start testing the new server right away.
- [Security](../operations/security.md) before you give links to other people.
