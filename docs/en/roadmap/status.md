---
title: Status and roadmap
description: What Mistgate releases already do, what is being tested now, what comes next and what 1.0 needs, and the known limits of the current release.
---

Mistgate is in **beta**; the current release is `v0.2.0`. It runs in production, but the API, the stored settings and the node protocol may still change before 1.0. The rest of these docs describe only what the code does today; the plans are on this page. The plans have no dates: "Next" and "Later" give the order, not a schedule.

```roadmap
- status: released
  title: Released
  meta: v0.1.0 – v0.2.0
  lead: Everything below is in a release and in the docs. First what the current release brought, then the rest by admin section.
  cards:
    - title: New in the current release
      icon: tag
      release: v0.2.0
      current: true
      points:
        - "**The whole interface redrawn.** Every screen, the windows, the phone layout and the person's own page; in **Users → People**, **QR and send** opens one window with the link, the QR code and the page password. See [Client apps](../guide/client-apps.md)."
        - "**Decoy sites: a library and sites from a template.** A library of sites with drafts and versions, put on nodes or on the panel's address; **From a template** builds a site of one of ten kinds for one of seventeen countries, in that country's language; with your own free Pexels or Pixabay key it gets real photographs, served from the node. See [A site from a template](../guide/decoy-site.md#a-site-from-a-template)."
        - "**One TCP 443 for several services.** VLESS (REALITY and TLS), TrustTunnel and MTProto can share one TCP port of a node, told apart by the name the client asks for; when a port is taken, the profile window offers **Put beside on 443**, with a name and the DNS record to add. See [One 443 for everyone](../guide/shared-port.md)."
        - "**TrustTunnel and a bridge for relays (both experimental).** TrustTunnel is a VPN inside ordinary HTTPS with a real certificate, for the official TrustTunnel apps, switched on per person. When a relay's entry cannot reach its exit, the bridge lets the exit connect to the entry; a relay's **Method** is **Auto (recommended)**, **Forwarding only** or **Bridge only**. See [TrustTunnel](../guide/trusttunnel.md) and [Relays](../guide/relays.md)."
        - "**Groups and subscriptions.** A person can be in several groups: the main one decides DNS, the Mihomo template, response headers and the colour, the extra ones add profiles and nodes. The **New node** window asks **Who gets this node**: **Everyone**, **Some groups** or **Nobody yet**. Your own Mihomo templates, response headers and routing for Happ and INCY are set in **Subscriptions**; a DNS preset sends Russian sites direct by a community list. See [Users and groups](../guide/users-and-groups.md) and [DNS](../guide/dns.md)."
        - "**New on nodes.** **Find sites nearby** on the **Profiles** tab scans the node's /24 for a REALITY target of the node's own; **Measure the channel** on the **Server** tab fills the link with 8 streams for 10 seconds each way against your own nodes, only on your command; every 15 minutes each node fetches a list of sites through its WARP, and a WARP+ key is set on the **Exit** tab; a router or a server of yours inside a country can check your nodes from its own network as an own probe (experimental). See [Nodes](../guide/nodes.md), [WARP](../guide/warp.md) and [Own probes](../operations/probes.md)."
        - "**Updating from v0.1.89.** After a backup, update the panel first, then every node. A node with an old agent keeps working, but TrustTunnel, a shared TCP 443, the relay bridge, measuring the channel and the decoy-site library need the new agent. Cancelling a rollout while a step runs works the same for node and system updates, and an update no longer rolls a node back for a profile that was already failing. Do not put the v0.1.89 binary back by hand on upgraded data: restore the backup made before the update. See [Updates](../operations/updates.md)."
    - title: Profiles
      section: profiles
      points:
        - "[Hysteria2](../guide/hysteria2.md) on the official core with Salamander."
        - "[AmneziaWG](../guide/amneziawg.md) 2.0 and 3.1 in userspace or as a kernel module, several profiles per node."
        - "[VLESS](../guide/vless.md) on xray-core: REALITY over XHTTP or TCP (RAW + Vision), with the node's own site or another site as the target; TLS over XHTTP or WebSocket, direct or behind a CDN."
        - "[VLESS and Hysteria2 on one node](../guide/vless-and-hysteria2.md), TCP 443 included."
        - "[MTProto](../guide/mtproto.md), experimental: a proxy for the Telegram app with Fake TLS, one key per person."
    - title: Nodes
      section: nodes
      points:
        - "[SSH installation](../getting-started/ssh-install.md) from the admin, and through MCP with the owner's approval: a pinned host key, checks before any change, jobs that can be cancelled, retried and resumed after an interruption."
        - "[Enrolling a node by command](../operations/troubleshooting.md#enrollment-fails): `mistgate-node enroll` explains a refused TLS handshake (a CDN or a reverse proxy in front of the panel) and names the flags that fix it."
        - "Saved SSH access, encrypted, with a verified password change; the panel's own SSH key, the server's `authorized_keys` from the node card, and access for nodes installed by command."
        - "[WARP egress](../guide/warp.md) that fails closed when the WARP link drops, and [torrent protection](../guide/torrent-protection.md) on every node."
        - "[Check ports](../guide/nodes.md#ports): UDP delivery and a TCP connection to every port of the node from another node of the fleet."
        - "[Measuring the network capacity](../guide/nodes.md#measuring-the-network-capacity) of a node's link."
        - "[A decoy site per node](../guide/decoy-site.md#nodes-three-options): the built-in one, the panel's site or an archive of its own."
        - "[Relays through an entry node](../guide/relays.md): checked before they are switched on, watched every 5 minutes, a copy of the server in subscriptions; AmneziaWG servers too, with a key of their own for the path through the entry."
        - "[IPv6 entry](../guide/nodes.md#ipv6-entry): the node's servers at one of its IPv6 addresses, checked from outside before they are turned on."
        - "[Resources and load](../guide/nodes.md#load): what a node and Mistgate itself cost the server, a **Load** view with the room left for more people, a [debug capture](../operations/troubleshooting.md#debug-capture-of-one-node) of one node, a map of the fleet and the ways in as a graph."
    - title: Users
      section: users
      points:
        - "[Users and groups](../guide/users-and-groups.md): traffic quotas with periods, terms, [devices by device ID](../guide/users-and-groups.md#devices-and-the-device-limit) with a limit in three modes, speed per person and per device; a group also decides which nodes its people reach."
        - "The People tab sorted by any column, with device counts, and bulk actions on several users at once."
        - "A device that has not connected for 30 days frees its place in the limit (v0.1.88). The period is set in **Subscriptions**, from 7 to 365 days, or switched off; the device is not deleted and its keys stay valid."
    - title: Subscriptions
      section: subscriptions
      points:
        - "One [subscription link](../guide/subscriptions.md) per person: a link list for Happ and similar apps, Mihomo YAML for apps on the mihomo core, [Xray JSON and sing-box JSON](../guide/subscriptions.md#xray-json-and-sing-box-json) for apps on those cores."
        - "The [device limit for apps on the link](../guide/subscriptions.md#device-limit-for-apps-on-the-link) in the modes **Only count**, **Limit** and **Strict**."
        - "The [user page](../guide/user-page.md): per-platform instructions, QR codes, `vpn://` keys for AmneziaVPN, and the person's own devices, which they can [rename and remove](../guide/user-page.md#self-service-devices), with a button to copy the link."
        - "[DNS presets](../guide/dns.md), with country lists for \"direct\" (China: Chinese sites direct) and [ads blocked in the apps](../guide/dns.md#ads-and-trackers); on the user page, a DNS choice for the link apps and one per server for the keys."
        - "[Automatic server choice](../guide/subscriptions.md#automatic-server-choice): an **Auto** group in the Mihomo and sing-box profiles, and an `Auto` config in Xray JSON, that moves the app to a working server by itself."
    - title: Health
      section: health
      points:
        - "[Client-eye checks](../operations/health.md#client-eye-checks): every 5 minutes the panel connects to the servers the way an app does, VLESS included (REALITY and TLS, directly or through the CDN)."
        - "[Connection counters](../guide/nodes.md#profiles) from the nodes: connections, drops right after connecting, refused keys, handshakes that never complete, time online and unknown keys, per device on a person's page when attribution is available and per profile on the node. Hysteria2 and AmneziaWG event counters, plus VLESS session time and, with an agent from v0.1.79, unknown keys; for Hysteria2 also how connections end (a **frozen** path, a **quiet** client, a close), RTT and loss; counts only, kept 7 days (168 hours)."
        - "The [node journal](../guide/nodes.md#journal): the warnings and errors of each node's agent and engines for 7 days, without client addresses, for every admin and in MCP."
        - "The [node doctor](../operations/health.md#node-doctor): host checks and safe fixes, among them [**Refresh the service**](../operations/health.md#fixes), which rewrites the agent's systemd unit from the admin, a warning when the server's automatic security updates are off, and the fix **Turn on security updates** on Debian and Ubuntu."
        - "[Reachability](../operations/reachability.md), a section of its own with each node's week: probes inside Russia through Globalping, cheburcheck's volunteer probes and bsbord's mobile SIM probes (Crimea included, with a monthly cap), the antifilter block lists, a TSPU probe from your own node in Russia and the reports of kl!ck users from their own networks, each switched on separately and off by default, with an alert when a node is unreachable from the country; each node is also measured over its IPv6 address when its IPv6 works."
        - "[Response time through an open tunnel and connect time apart from it](../operations/health.md#what-the-numbers-mean), and a [notice when the panel's host loses UDP](../operations/health.md#when-the-panels-server-loses-udp)."
        - "[Alerts](../operations/health.md#alerts) in the admin and in Telegram."
        - "The [panel watcher](../operations/panel-watcher.md): a Cloudflare Worker checks the panel every minute and writes to Telegram when it does not answer."
        - "[Closing an alert](../operations/health.md#closing) until its problem goes away, and the doctor's [23 checks](../operations/health.md#the-checks), among them CPU steal, connection tracking and the agent's open files."
    - title: Updates
      section: updates
      points:
        - "[What's new](../operations/updates.md#whats-new): the release notes between your version and the available one, in the interface language."
        - "[Signed node agent updates](../operations/updates.md), one node at a time, now or on a schedule, with a health gate and automatic rollback."
        - "[System updates of the servers](../operations/updates.md#server-operating-system): security updates and a reboot of one node from the panel, which waits for the node to come back and checks its kernel and profiles before it says done, while the node refuses a reboot that is not safe and says why (v0.1.86); or one run for the whole fleet (v0.1.88): the quietest node goes first as a canary, the rest follow one at a time or in batches, a node that says \"try later\" is retried, and a real failure stops the run with the choices Retry, Skip and continue, Close. Ordinary packages can be installed as well as security updates."
        - "Panel self-update from signed GitHub releases, with a rollback when the new panel does not stay up."
        - "[Release channels](../operations/updates.md#release-channels) (stable, testing, development) for the panel and its nodes, and a backup offered before the panel updates itself."
        - "[A one-line panel install](../getting-started/install-panel.md#one-line-install) that verifies the release signature."
        - "[Reproducible releases](../operations/releases.md) signed offline from the tag."
        - "[A download window for slow links](../operations/updates.md#one-nodes-update), 30 minutes by default, and each node's download progress in the rollout."
    - title: Integrations
      section: integrations
      points:
        - "[API tokens](../reference/api.md) with the readonly, operator and admin profiles."
        - "The [MCP server](../reference/mcp.md) with plan / apply and owner approvals, now with day-to-day changes, one-call reads, probes between your nodes, diagnosis and scheduled tasks; ready prompts in the [AI agent guide](../getting-started/ai-agents.md)."
        - "[Telegram alerts](../operations/telegram-alerts.md) to the admins who link their chat: health, releases, failed backups, plans waiting for approval, sign-in security."
        - "The [assistant](../guide/assistant.md): a chat with a cloud model that prepares changes as cards, with [follow-ups](../guide/assistant.md#follow-ups) and [scheduled tasks](../guide/assistant.md#scheduled-tasks)."
    - title: Settings
      section: settings
      points:
        - "The admin, the user page, the node install page, Telegram messages and these docs in English, Russian and Simplified Chinese, the admin and the user page also in Persian and Turkish; sign-in with a passkey or a password with an authenticator code, optional Cloudflare Turnstile, an audit log. See [Security](../operations/security.md)."
        - "A code audit (v0.1.84) went over sign-in, subscriptions, the node link, MCP and the assistant; the gaps it found are closed. See [Security](../operations/security.md)."
        - "**Security fixes.** Fixes from a review of the panel and the node link (v0.1.88 and v0.1.89): update the panel first, then every node. The panel's own update channel is read from a file that root owns, `/etc/mistgate/panel-update-channel`; without it the channel is stable."
        - "A [decoy site](../guide/decoy-site.md) at the panel's address hides the admin from strangers."
        - "[Encrypted backups](../operations/backups.md) to Cloudflare R2 on a schedule, with retention, or to a file on the owner's computer; restore in the panel or on a new server with the installer."
        - "[Panel logs, retention, export and metrics](../operations/troubleshooting.md#panel-logs-retention-export-and-metrics) for the owner, and the reveal animation (on by default, switched off per device in **Settings → Interface**)."
- status: now
  title: Now
  meta: v0.1.34 – v0.2.0
  lead: "Released, and still being proven in production: first the experimental parts of v0.2.0 and the Windows app (a separate project), then what earlier releases added."
  cards:
    - title: TrustTunnel in production
      section: profiles
      text: "New in v0.2.0 and experimental. Give a key to one or two people first and check that the official app connects from their networks before you hand it out widely. See [TrustTunnel](../guide/trusttunnel.md)."
    - title: The relay bridge in production
      section: nodes
      text: "New in v0.2.0, experimental, TCP only. Existing relays got the method **Auto (recommended)**, so a relay can move to the bridge by itself after two failed checks: after the first switch, read the relay's events and check that people reconnect. See [Relays](../guide/relays.md)."
    - title: Own probes in production
      section: reachability
      text: "New in v0.2.0 and experimental. A router (Keenetic with Entware) or a server of yours inside a country checks your nodes from its own network: start with one probe and compare its answer with the other sources in the *From Russia* column before you rely on it. See [Own probes](../operations/probes.md)."
    - title: Sites from a template in production
      section: settings
      text: "New in v0.2.0. Look at the preview, and open the saved site in a browser, before you put it on a node. Photographs come from Pexels or Pixabay, and when one source refuses, the panel does not switch to the other. See [A site from a template](../guide/decoy-site.md#a-site-from-a-template)."
    - title: "The Mistgate app for Windows"
      section: settings
      text: "A separate project, [mistgate-client](https://github.com/Mistgate/mistgate-client): Hysteria2, AmneziaWG and VLESS in one app, routing by site and by program, a kill switch. It works with these panels, through the owner's switch in **Subscriptions → Who gets what**, and with plain subscription links."
    - title: VLESS in production
      section: profiles
      text: "REALITY since v0.1.34, the node's own site as the target since v0.1.35, TLS and a CDN since v0.1.36. VLESS has not been tried behind a real CDN yet: in the end-to-end test the node itself stands in for the CDN. See [VLESS behind a CDN](../guide/vless.md#vless-behind-a-cdn)."
    - title: Devices by device ID in production
      section: subscriptions
      text: "Existing panels run in **Only count**, so you see who is over the limit before you switch to **Limit**. See [How to move from Only count to Limit](../guide/subscriptions.md#how-to-move-from-only-count-to-limit)."
    - title: Xray JSON and sing-box JSON in production
      section: subscriptions
      text: "No rule hands out Xray JSON by default: add one for the apps you want, or try an app first with `?format=xray-json` on its link. See [Who gets what](../guide/subscriptions.md#who-gets-what)."
    - title: A decoy site on every node
      section: nodes
      text: "The built-in site, the panel's site or an archive of its own for each node. Nodes created before v0.1.36 stay on the built-in page until you pick another option. See [Your own decoy site](../guide/decoy-site.md#nodes-three-options)."
    - title: The country check in production
      section: health
      text: "Every source is off by default. Switch one on and watch the *From Russia* column before you rely on its alert: the Globalping probes are not the panel's own, and `listed` is a registry mark, not a measurement. See [Reachability](../operations/reachability.md)."
    - title: Relays in production
      section: nodes
      text: "New in v0.1.65. The panel checks a relay from outside the country, so a freeze that only people inside see shows up in kl!ck reports and bsbord, not in the panel's own check. See [Relays](../guide/relays.md)."
    - title: IPv6 entry in production
      section: nodes
      text: "New in v0.1.66, with its card in the admin since v0.1.67. Only on a node whose IPv6 works at the hoster. Whether people inside a country actually get through over it shows first in kl!ck reports. See [IPv6 entry](../guide/nodes.md#ipv6-entry)."
    - title: Ads blocked on the device in production
      section: subscriptions
      text: "New in v0.1.67. Happ takes the list from its own geo files, by default Loyalsoldier's, which adds about 195,000 domains: on an iPhone, check that Happ keeps running with the preset before you give it to many people. See [Ads and trackers](../guide/dns.md#ads-and-trackers)."
    - title: The security updates fix in production
      section: health
      text: "New in v0.1.67. It installs a package on the server: try it on one node first and read the doctor's result there before you run it on the others. See [Fixes](../operations/health.md#fixes)."
    - title: Nodes per group in production
      section: users
      text: "New in v0.1.68. Turning a node off for a group takes it away from everyone in it at once, even from a person who has that node ticked on their own card: a profile on it stops reaching them. See [Users and groups](../guide/users-and-groups.md#the-groups-tab)."
    - title: MTProto in production
      section: profiles
      text: "New in v0.1.69 and experimental. It has not been tried against real blocking yet: give the link to one or two people first and check that Telegram connects from their networks before you hand it out widely. See [MTProto](../guide/mtproto.md)."
    - title: The assistant in production
      section: integrations
      text: "New in v0.1.81. It sends the conversation and what its tools read to the provider you choose, so read where each provider's data goes before you connect one, and set a monthly budget. Start with **Only me** under **Who may use it**, and read a plan card's wording before you press **Apply**. See [Providers and where your data goes](../guide/assistant.md#providers-and-where-your-data-goes)."
    - title: Blocking diagnosis in production
      section: health
      text: "New in v0.1.81. `block_diagnosis` and `access_report` apply fixed rules to the nodes' counters and the country checks; where two causes look alike they say so, with low confidence. A lab of staged blocking checks seven of the classes before each release that touches them (an address, a port, UDP, a TCP freeze, a whitelist, one operator, a broken node); protocol fingerprinting and regional are tested on constructed counters only. The handshake counters need the agent v0.1.81. Read the evidence they list before you move people. See [Assistant](../guide/assistant.md#work-out-what-does-not-open)."
- status: next
  title: Next
  lead: The next items in the queue.
  cards:
    - title: Update from another node
      section: updates
      text: "A node with a poor link to the panel gets the update file from a neighbouring node of the fleet that already has it. For now there is one fallback source, GitHub. See [Download sources](../operations/updates.md#download-sources)."
    - title: Which operator a connection comes from
      section: reachability
      text: "The network (ASN) of each connection, counted per hour, never the address: an aggregate per operator, on by default, and a mode per person, off by default, both kept 7 days. It lets the diagnosis tell operators apart. Today the panel learns operators only from the country check's probes and the reports of kl!ck users. See [Assistant](../guide/assistant.md#work-out-what-does-not-open)."
    - title: A new IPv6 address in one click
      section: nodes
      text: "The IPv6 entry and the reachability checks over IPv6 are in the releases since v0.1.65. The node agent already keeps an extra address on the server's uplink; the panel's half is missing: replacing the entry's address with a new one from the server's own /64 in one click. See [IPv6 entry](../guide/nodes.md#ipv6-entry)."
    - title: One entry, several exits
      section: nodes
      text: "Today a relay forwards to one exit server, and the app chooses between whole paths (direct, or through each relay) with the Auto group. Several relays from one entry to different exits already work this way, each on its own port of the entry. Next, the entry's page creates them in one step: pick the exits, and the app's Auto group keeps choosing the fastest path, also when the panel is offline. The panel does not switch the exit of a relay itself: a path through a relay carries the keys of its exit, so apps would lose the path until their next subscription update. See [Relays](../guide/relays.md). Requested in [#47](https://github.com/Mistgate/mistgate/issues/47)."
    - title: Where your people are
      section: settings
      text: "A region in the first-run setup and in Settings: DNS presets, \"direct\" lists, recommended apps and reachability checks follow the countries you choose. China's pieces are already in (Chinese sites direct since v0.1.64; AliDNS, DNSPod and a Baidu check in the node doctor since v0.1.78); the setting itself is not."
    - title: The Cloudflare edition on par with the VPS
      icon: layers
      text: "Bring to the six-Worker edition what it does not have yet:"
      points:
        - "encrypted backups and restore;"
        - "MCP, the assistant, scheduled tasks and new API tokens;"
        - "probes, the debug capture, panel logs and the Load view;"
        - "client-eye tunnel checks;"
        - "updating the panel from the admin;"
        - "live node logs and an Own site decoy for a node."
    - title: Install from cloud-init
      section: settings
      text: "An unattended install of the panel and of a node from a cloud server's user-data, with ready examples for common clouds."
- status: later
  title: Later
  lead: Planned, but not next in the queue.
  cards:
    - title: Community routing lists
      section: subscriptions
      text: "Ready rule sets maintained by the community, updated by themselves. The first, for Russia's sites \"direct\", is in v0.2.0 as a DNS preset; the full list of local sites and a mode for mobile whitelists are still to come."
    - title: A choice of the protocol core per node
      section: updates
      text: "The protocol cores (Xray, Hysteria2) are built into the node agent, so today a node runs the core its agent was built with. If a core ever changes in a way old client apps cannot follow and that cannot be bridged on the server, the panel will offer two agent builds, one with the older core and one with the newer, and let you pick one per node, with the same signed rollout and rollback. Until then a new core ships with the compatibility old clients need."
    - title: More protocols
      section: profiles
      text: "Candidates: TUIC, NaiveProxy and MASQUE. Each plugs into the same subscription link through the protocol contract."
    - title: A Telegram bot beyond alerts
      section: integrations
      text: "Today the bot only sends alerts and links an admin's chat. Later: running the fleet from Telegram, and a bot for users. See [Telegram alerts](../operations/telegram-alerts.md)."
    - title: Subscription mirrors
      section: subscriptions
      text: "Copies of the subscription at other addresses, so that apps still get it when the panel's address is unreachable."
    - title: A shared map of blocks
      section: reachability
      text: "Panels whose owners opt in send short anonymous reports of what fails (country, big operator, kind of protocol, what failed), and the project publishes weekly open summaries anyone can mirror. No addresses, names, ports or people ever leave the panel; reports show failures only and are labelled as unverified. Off by default. Not being built yet: first a local preview of what would be sent."
    - title: Moving between the VPS and the Cloudflare edition
      icon: refresh
      text: "Moving users, nodes and subscription links either way, with the links still working. Today a fleet is set up again for the other edition. See [Moving](../getting-started/install-edge.md#moving-between-a-vps-panel-and-the-edge)."
    - title: The Cloudflare edition on a real account
      icon: globe
      tag: Preview
      text: "The first run of `mistgate edge deploy` on a real Cloudflare account: the six Workers, the router with the domain, and `--rollback`. So far the command and the panel have been tested against a simulated Cloudflare only. See [Install the Cloudflare edition](../getting-started/install-edge.md)."
- status: goal
  title: Road to 1.0
  lead: The conditions for 1.0. Until then, breaking changes are possible.
  cards:
    - title: The node protocol and the stored settings are fixed
      text: "A panel and agents of different 1.x versions understand each other, and the panel's data moves from version to version without manual steps."
    - title: Upgrades from older versions are tested
      text: "Every release passes an automated upgrade test from earlier versions: a panel with its data and the nodes connected to it."
    - title: A stable API
      text: "The API, API tokens and MCP tools without breaking changes within 1.x. See [API](../reference/api.md) and [MCP](../reference/mcp.md)."
```

## Known limits

- Client-eye checks skip VLESS when the node agent does not report the required capability. Agents before v0.1.34 have no VLESS engine; TLS profiles also require `vless-tls/1` (v0.1.36 or later).
- **Check ports** looks from one vantage point: another node of the fleet or the panel host. On VLESS ports it runs a TLS handshake with the expected name, not a REALITY handshake.
- The SSH installation logs in with the panel's own key, a password, or once with the owner's own private key, and the node card manages `authorized_keys`. Closing password login in sshd and rotating the panel's key are not there yet. See [Install a node over SSH](../getting-started/ssh-install.md).
- Backups go to Cloudflare R2 or to a file on the owner's computer. Restore is in the VPS edition (the panel and the installer); the Cloudflare edition keeps its data in D1 and has no restore yet.
- The panel does not create a Cloudflare account, a bucket or a token: you supply a bucket-scoped token with read, write and delete access.
- The Cloudflare edition is a preview, not yet tried on a real Cloudflare account: some features are not available in it yet (see [the list](../getting-started/install-edge.md#not-available-in-this-edition-yet)), and a fleet cannot be moved between it and a VPS panel.
- A device ID is what the app reports. Blocking refuses that ID; an app that reports another ID is a new device and takes a free place. To cut off a link that was passed on, issue a new link as well.
- **Speed per person** is per node, so a person on two nodes at once can get up to twice the limit. Node agents before v0.1.39 hold each device to the smaller of the two limits.
- In the Cloudflare edition the digest of what was sent to a node must fit one D1 row (2 MB), about 38,000 credentials per node. See [the limits of this edition](../getting-started/install-edge.md#not-available-in-this-edition-yet).
- sing-box JSON has no XHTTP and cannot pin a self-signed Hysteria2 certificate; Gecko and AmneziaWG go out only in Mihomo YAML. A server a format cannot carry is left out of that format. See [What works where](../guide/subscriptions.md#what-works-where).
- The country check looks from a few probes, not from every network, and the block lists cover Russia only. `listed` means the address is in a registry, not that its traffic is cut.
- Some defaults are country-specific (Yandex DNS for Russia, AliDNS and DNSPod for China, the doctor's control domains, split-DNS presets); all of them are settings.
- The assistant sends the conversation, its instructions, your notes and what its tools read to the cloud provider you connect, so that provider's data policy applies. Prices you enter are estimates, and the monthly budget is measured from them. See [Providers and where your data goes](../guide/assistant.md#providers-and-where-your-data-goes).
- The assistant, MCP, scheduled tasks, probes, the speed test, the debug capture, panel logs, panel resources and the Load view are in the VPS edition only. See [the list](../getting-started/install-edge.md#not-available-in-this-edition-yet).
- The diagnosis is only as good as the counters. VLESS keeps no attempt counter, and the kernel AmneziaWG backend cannot tell a handshake initiation from an answer; where two causes look alike, `block_diagnosis` says so with low confidence. Agents before v0.1.81 do not count why handshakes fail or what a node's machine is short of: the panel says "not known".
- Scheduled tasks follow the panel's fixed UTC offset and do not follow daylight-saving time: set the offset again when local time changes.
