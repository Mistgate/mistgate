---
title: Updates
description: How node agents update themselves from signed bundles, how to update a node now or on a schedule, what protects an update, and how the panel updates itself.
---

Node agents update themselves, but only to a build signed with the release key. The panel carries the signed bundle to the nodes, and every agent checks the signature against the key compiled into its own binary before it replaces itself. The panel never updates a node on its own: you press **Update** in a node's row to update it now or to schedule it for a quiet hour. The panel itself can install a newer official release from the Updates page, again only when its signature verifies.

This page is for running a fleet. Making the release key, signing releases and publishing them is in [Releases and signing](releases.md).

## How trust works

- There is one Ed25519 key pair, the **release key**. Its private half stays offline with whoever signs the releases; both binaries carry the public half, stamped in at build time. The official binaries carry the project's key. The panel also saves the key in `<data-dir>/release.pub` on its first start.
- Every release has two signed manifests: the node bundle (`manifest.json` with `manifest.sig`) and the panel manifest (`panel-manifest.json` with `panel-manifest.sig`). Each lists the version, the build time, an expiry date and, for every binary, its name, OS, architecture, size and SHA-256. The panel manifest is signed in its own Ed25519 context, so an agent never accepts it as its update and the panel never installs a node binary as itself.
- The agent checks, in this order: the signature against its compiled-in key, the manifest format, the expiry, that the release is newer than itself, and that the bundle has a file for its OS and architecture. Then it downloads the file from the panel over its mutual-TLS connection and checks the size and the SHA-256. Any failure leaves the installed binary untouched.
- The panel is only a courier: a compromised panel cannot make a node run code that was not signed with the release key.
- Releases are ordered by their build time (the Unix time of the source commit), not by the version string. A release with the same build time as the node is "already current"; an older one is refused as a downgrade.
- A node agent built without a release key cannot update itself; `mistgate-node version` prints the fingerprint of the key it carries. If the key compiled into the panel and `release.pub` differ, the panel logs `release key mismatch` and trusts no bundle until the new key is confirmed: see "Rotate the release key" in [Releases and signing](releases.md).

## Where the bundle comes from

The panel keeps the node bundle in `<data-dir>/dist`.

- **From GitHub.** A panel with a release key checks the latest stable release of `Mistgate/mistgate` when it starts and every 10 minutes after that. When the release carries a node bundle that verifies with the panel's key and is newer than the bundle the panel has, the panel downloads it, checks every file and replaces `dist` in one step. A release signed with another key is ignored and `dist` stays as it is. Downloading a bundle never updates a node by itself.
- **By hand.** A panel built with your own key gets the bundle you sign and copy into `dist`: see "Use a bundle of your own" in [Releases and signing](releases.md). The panel reads `dist` again within a minute; **Read the folder again** reads it at once.

The **Release bundle** card on the Updates page shows the result:

| Status | Meaning |
|---|---|
| Signature verified | The signature matches this panel's release key, the manifest is valid and every file is present with the right size and checksum. Only such a bundle can be rolled out and used by the [SSH installation](../getting-started/ssh-install.md). |
| Failed the check | Something is wrong; the card says what (see below). |
| Not checked | This installation has no usable release key: none in its build or `release.pub`, or the two differ. The panel cannot judge the bundle; the nodes still check it themselves. |
| No bundle | `<data-dir>/dist` has no `manifest.json`. |

| Reason | What to do |
|---|---|
| No release bundle in the panel's data directory | Wait for the GitHub download, or copy your bundle to `<data-dir>/dist`. |
| The signature file (manifest.sig) is missing | Copy `manifest.sig` too. |
| manifest.json is not a valid release manifest | Sign again; do not edit the manifest. |
| The manifest uses a newer format than this panel understands | Update the panel first. |
| The signature does not verify with this panel's release key | The bundle was signed with another key, or the panel was built with another key. |
| The manifest has expired; sign a new one | A newer signature is needed: the maintainers renew it on GitHub, or you sign again with a later `--expires`. |
| A file listed in the manifest is missing | Copy every file of the bundle. |
| A file differs from the manifest (size or checksum) | The copy is incomplete or the file was changed: copy it again. |

The panel serves the bundle's files only to agents with a valid node certificate, through the agent endpoint, at most four downloads at a time, in resumable chunks. Only regular files in `dist` count (a symbolic link is not followed). Replace a hand-made bundle at once, as a whole; changing it while a rollout runs pauses the rollout.

## The Updates page

Everyone can view the page. Updating, scheduling, pausing, resuming and cancelling, rolling a node back and **Read the folder again** need the owner and a fresh step-up (see [Security](security.md)).

- **This panel**: its version, build date and release key fingerprint (or "none: this build cannot check bundles"), the latest GitHub release and **Check GitHub** / **Update panel**.
- **Release bundle**: the bundle in `<data-dir>/dist`.
- **Nodes**: every node with its agent version and build date, its state and its last update. The owner gets **Update** (or **Schedule** when an update is already scheduled) and **Roll back** per row.

| Node state | Meaning |
|---|---|
| Up to date | Its build is at least as new as the trusted bundle's (without a trusted bundle, as the panel's own build). |
| Update available | It can update itself and runs an older build. |
| Updating… | An update of this node is in progress. |
| Rolled back | Its last update ended in a rollback; it runs the previous build. |
| Update failed | Its last update failed on the node. |
| Update by hand | Its agent cannot update itself (see "Old agents" below). |
| Offline | The node is not connected; the last known build is shown. |

"No guard against repeated crashes" under a node means its service file has no crash-loop guard: only the agent's own 5-minute self-rollback protects an update there. Run `mistgate-node install` with a new binary on it once to add the guard.

## Update a node

### Now, with the running rollout, or later

**Update** in a node's row opens a dialog with two choices:

- **Update now** starts the update of this node at once. While another update runs with the same release, the choice reads **Add to current rollout**: the node becomes the next stage and starts after the current one passes its checks. A paused rollout, or one with another release, has to be resumed, finished or cancelled first.
- **Choose a date and time** saves a schedule. The time is entered in the fixed UTC offset of **Settings → System → Update time zone**; UTC is the default (installations made before this default keep UTC+03:00, which was the default then). Changing the setting does not move existing schedules: each keeps the exact instant and offset shown when it was saved. The earliest time is a minute from now; **Cancel schedule** removes it.

Each update is pinned to a release:

- An update now is pinned to the bundle version the page showed (an MCP plan to the version of the plan): if the trusted bundle was replaced in between, the panel refuses and asks you to review the new version.
- A schedule is pinned to the signed version selected when it was saved and cannot be later than that bundle's expiry. If it cannot start at the chosen time (the node is offline, another update is running), the panel keeps trying for two hours; after that the row says the schedule was missed, and it never starts by itself, so an update meant for a quiet hour does not run whenever the node returns. Schedule it again or cancel it. If the bundle is replaced before then, the panel does not substitute the new version: review it and schedule again.

### One node's update

1. The panel sends the signed manifest. The agent checks it, downloads its file, keeps its current binary as `<binary>.prev`, puts the new one in its place and restarts itself in place (systemd sees nothing). The node is offline for a few seconds; the admin shows it as updating, not as an outage.
2. The panel waits up to 10 minutes for the agent's answer (the download included), then up to 3 minutes for the node to come back with the new build.
3. The **gate** runs for up to 5 minutes after the node is back. It passes when all of this holds:
   - the new build has committed: it connected and applied its configuration without a failed profile;
   - the configuration the node applied is the panel's;
   - no profile that worked before the update has failed (a profile that stays failed for 30 seconds fails the gate at once);
   - every profile the panel can check has passed a [client-eye check](health.md) since the node came back (the panel starts a round at once).
4. When the gate passes, the step is done. When it fails, the panel sends the node a rollback (the previous binary comes back), the step ends as "rolled back" and the rollout pauses.

### The agent's own safety net

The agent does not rely on the panel to undo a bad update:

- A new build that has not connected and applied its configuration within 5 minutes of starting puts the previous binary back and restarts (`not_committed`).
- A new build whose own build time differs from the manifest rolls back at once (`built_mismatch`).
- The service file counts the starts while an update is pending; at the third start in a row it restores the previous binary before the agent runs (`crash_loop`).
- The previous build reports the outcome when it connects again.

### Pause, resume, cancel

A running update shows its progress at the top of the page, with **Pause**, **Resume** and **Cancel rollout** for the owner.

- **Pause**: no new step starts. A step already in progress finishes and goes through its gate. Nodes already updated stay updated.
- **Resume**: the remaining waiting steps go on. Failed and rolled-back steps stay as they are; update those nodes again after fixing the cause. If the bundle on disk changed, resume is refused: cancel and start again.
- **Cancel rollout**: waiting nodes are skipped; a node being updated finishes and is checked; updated nodes stay on the new version.

A rollout also pauses by itself:

| Pause reason | What happened |
|---|---|
| a node did not take the update | It rejected the update (the reason is shown). |
| a node did not pass the check after updating | It failed the gate or did not come back; where possible it was put back on the previous version. |
| the release bundle changed | The bundle in `<data-dir>/dist` is now another release. Cancel and start again. |
| the panel restarted during a step | The panel does not know how the step ended. Check the node, then resume or cancel. |

The first, second and fourth also raise the alert "The node update stopped" until you resume, cancel or start a new update.

A finished rollout is **Done**, **Cancelled** or **Finished with problems** (at least one node failed or was rolled back). The page shows the active rollout, or the last one. Finished rollouts are kept: always the last 20, older ones for 90 days.

Common step errors:

| Error | Meaning |
|---|---|
| The node rejected the signature | The node's agent trusts another release key. |
| This node's agent was built without a release key | Update it by hand. |
| The release is older than what the node already runs | A newer signed build is needed. |
| The bundle has no file for this node's system and architecture | The bundle lacks the binary for its architecture (for example `arm64`). |
| The node could not download the file from the panel | Look at the node's link to the panel. |
| The node cannot write next to its program file | Check the disk of the node; running `mistgate-node install` once writes a current service file. |
| The node did not answer the update command in 10 minutes | Look at the node. |
| The node did not come back within 3 minutes | Look at the node and its logs. |
| The client-eye check failed after the update | See [Health](health.md); the node is back on the previous version. |

### Roll back by hand

**Roll back** in a node's row puts the node's previous binary (`<binary>.prev`) back and restarts the agent. It works outside a rollout too. A node that was never updated has no previous version and refuses. If the node is part of an active rollout, its step is marked "rolled back" and the rollout pauses.

The **Last update** column says who rolled a node back: "Rolled back by you", "The panel put the previous version back: …" (the gate), or "The previous version came back on a command of the panel".

## Old agents: update by hand once

A node shows **Update by hand** when its agent cannot update itself: it predates self-update, it was built without a release key, or it runs under the first version of the service file (whose program directory is read-only). **How to update** in its row gives two commands, for example:

```sh
scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node
ssh root@de1.example.com 'chmod +x /root/mistgate-node && /root/mistgate-node install'
```

`install` copies the binary to `/usr/local/bin/mistgate-node`, writes the current hardened service file (with the crash-loop guard) and restarts the agent. After that the node updates from the Updates page. Without a bundle on the panel, take `mistgate-node` of the same release as the panel.

Older agents keep working with a newer panel: changes to the agent protocol are additive. A feature an old agent lacks is reported as "agent too old" where it is needed.

## Update the panel

The Updates page checks the latest stable release of `Mistgate/mistgate` at startup and every six hours. **Check GitHub** checks at once (a check made in the last three minutes is answered from memory, which keeps the unauthenticated GitHub quota for the bundle download).

A release can be installed only when it carries `panel-manifest.json` and `panel-manifest.sig`, the signature verifies under the release key compiled into this panel (`release.pub` does not count: the panel's service user can write it), the manifest has not expired, and its build time is newer than this panel's. Releases are ordered by that build time, not by the version string: a panel built from a commit after the tag (for example `v0.1.6-3-gabc1234`) never sees `v0.1.6` as an update, so it is never offered a downgrade onto a database it already migrated. A release without such a manifest is shown as "not signed with this panel’s release key: it cannot be installed"; one whose manifest expired, as expired until it is signed again. The card shows the release's build date and the SHA-256 of the binary for this server. **Update panel** sends exactly that version and SHA-256, and the panel refuses if the latest signed release is another one by then.

Automatic installation is available to a root panel under systemd. A panel running as an unprivileged service user such as `mistgate` can use the fixed root helper below; the HTTP panel stays unprivileged. In either case, the owner must confirm with a fresh step-up, and an active node rollout must finish first. The panel writes the confirmed version and SHA-256 to `<data-dir>/panel-update.request` and starts the helper (a transient systemd unit `mistgate-panel-update-<time>.service` for a root panel, `mistgate-panel-update.service` otherwise). The request can only narrow what the helper installs. The helper verifies the release's panel manifest itself with the key compiled into the installed binary, requires it to name exactly the confirmed version and SHA-256 and a build newer than its own, downloads the binary and checks its size, SHA-256 and ELF architecture. Only then does it stop the panel, back up its data directory, replace the binary and start the panel.

The new panel must stay active for 45 seconds without systemd restarting it (with `Restart=on-failure`, a crash a few seconds in shows only as a restart). Otherwise the helper stops it, puts back the previous binary and the data backup, including the original file ownership, and starts the previous panel. On success the previous binary is kept as `<binary>.prev`; the data snapshot is kept next to the data directory as `<data-dir>.panel-update-backup.tar.gz`. If the helper ends without replacing the panel (a failed check or download), the Updates page shows within seconds that the update did not finish, and a rollout or another update can start again; `journalctl -u 'mistgate-panel-update*'` says why.

The data backup is taken while the panel is stopped, before the new binary first runs. If the new panel fails its 45-second check, the database it may already have migrated forward is replaced by that backup, so the previous binary never runs on a schema it does not know; whatever the new panel wrote in those seconds is lost. To go back later by hand, stop the panel, restore the data directory from `<data-dir>.panel-update-backup.tar.gz`, put `<binary>.prev` back and start it; everything changed since the update is lost.

### Enable updates for a non-root panel service

For a systemd panel unit with `User=mistgate`, install the root-owned helper unit and its narrow PolicyKit rule. The rule allows that account to start only `mistgate-panel-update.service`; it does not grant general systemd control or a root shell. The panel unit can keep `NoNewPrivileges=yes`. The helper unit updates only the standard layout: the binary `/usr/local/bin/mistgate`, the data directory `/var/lib/mistgate` and the service `mistgate.service`. A non-root panel installed elsewhere refuses **Update panel** with an error that names these paths; update it by hand.

```sh
install -o root -g root -m 0644 deploy/systemd/mistgate-panel-update.service /etc/systemd/system/mistgate-panel-update.service
install -o root -g root -m 0644 deploy/polkit/60-mistgate-panel-update.rules /etc/polkit-1/rules.d/60-mistgate-panel-update.rules
```

Review the helper unit before loading it. Its `ExecStart` must name the installed binary, data directory and panel unit. The default panel unit is `mistgate.service`; if yours differs, set `--update-service` on the panel and `--service` in the helper unit to the same value. Set `ReadWritePaths` to the data directory's parent and the binary's directory; the helper needs the parent to atomically rename the data directory during rollback. If the service user is not `mistgate`, change `subject.user` in the PolicyKit rule to that exact account. Keep both files owned by root and not writable by the panel user.

```sh
systemctl daemon-reload
systemctl show --property=LoadState --value mistgate-panel-update.service
```

The last command should print `loaded`. The host also needs PolicyKit installed and its authorization service running for the panel user to start the helper. The helper is started on demand by the panel and should not be enabled or started manually. Check its result with `journalctl -u mistgate-panel-update.service` if the panel reports that an update could not be scheduled.

### Early builds and manual updates

Early commit builds such as `0.1.0-<commit>` predate the panel updater: they can show a release but cannot install it. Replace such a binary once with the matching asset of the current stable release from [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest), with the manual steps below. Later builds update from **Settings → System**: press **Check GitHub**, then **Update panel** when a newer release appears.

For a non-systemd installation, a panel built with your own key, or a manual fallback:

1. Use a binary with the same release key. A panel whose compiled-in key differs from `<data-dir>/release.pub` trusts no bundle until you run `mistgate release trust-key` (see [Releases and signing](releases.md)); a build without a key keeps using `release.pub` for bundles but cannot install panel releases.
2. Do not update the panel while a rollout is running: pause it or let it finish. A restart in the middle of a step pauses the rollout ("the panel restarted during a step").
3. Copy the new binary next to the installed one, then stop the panel, back up the data directory, replace the binary and start it again. Here the binary is `/usr/local/bin/mistgate` and the systemd unit is `mistgate.service`, as in [Install the panel](../getting-started/install-panel.md); use your own names:

```sh
scp mistgate-linux-amd64 panel.example.com:/usr/local/bin/mistgate.new
# on the panel server, as root
systemctl stop mistgate
tar czf /root/mistgate-backup-$(date +%F).tgz -C /var/lib mistgate
mv /usr/local/bin/mistgate.new /usr/local/bin/mistgate
chmod 0755 /usr/local/bin/mistgate
systemctl start mistgate
journalctl -u mistgate -n 50
```

4. The database is migrated when the panel starts; nothing else is needed. The log line `mistgate starting` shows the new version, and so do **This panel** on the Updates page and `mistgate version`.

What happens around the restart:

- The node agents reconnect by themselves (they retry with a growing pause of 1 to 60 seconds). Their profiles keep running while the panel is away.
- During the first 90 seconds after the start, the panel opens no "Node is unreachable" alert.
- Admin sessions survive the restart.

> **Warning:** Migrations only go forward. An older panel binary is not guaranteed to work with a database a newer one has migrated. To go back, stop the panel, restore the data directory from the backup taken before the update, put the old binary back and start it. Changes made after the backup are lost.

## Updates through the API and MCP

Every API token profile can read the Updates page (`GetUpdates`). Starting, pausing, resuming and cancelling a rollout, scheduling and rolling a node back are refused over the plain API; an MCP agent with the admin profile can only plan them, and the owner approves each plan in the admin. An MCP `rollout_start` updates exactly one node, pinned to the version in the plan. See [API](../reference/api.md) and [MCP](../reference/mcp.md).
