---
title: Updates
description: How node agents update themselves from bundles you sign, how a staged rollout checks and rolls back nodes, and how the panel installs GitHub releases.
---

Node agents update themselves, but only to a build signed with your release key. The panel carries the signed bundle to the nodes; every agent checks the signature against the key compiled into its own binary before it replaces itself. On root systemd installations, the Updates page can also install the latest stable panel binary from the official GitHub Releases.

## How trust works

- There is one ed25519 key pair, the **release key**. The private half stays with you, offline. A keyed panel build stores the public key in `<data-dir>/release.pub`; node agents carry it in their binaries. Generic GitHub panel builds preserve and use the saved key.
- For every release you sign a manifest: the version, the build time, an expiry date and, for every binary, its name, OS, architecture, size and SHA-256.
- The agent checks, in this order: the signature against its compiled-in key, the manifest format, the expiry, that the release is newer than itself, and that the bundle has a file for its OS and architecture. Then it downloads the file from the panel over its mutual-TLS connection and checks the size and the SHA-256. Any failure leaves the installed binary untouched.
- The panel is only a courier: a compromised panel cannot make a node run code you did not sign.
- Releases are ordered by their build time (the Unix time of the source commit), not by the version string. A release with the same build time as the node is "already current"; an older one is refused as a downgrade.
- A node agent built without `RELEASE_KEY` cannot update itself. `mistgate-node version` prints its compiled key fingerprint. The panel uses `release.pub` for bundle verification, even after a generic GitHub panel update.

## Publish a release

### 1. Make the release key (once)

```sh
mistgate release keygen --out ~/mistgate-release.key
```

It writes the private key to the file (mode 0600; an existing file is never overwritten) and prints the public key and its fingerprint (the first 16 hex characters of its SHA-256).

> **Warning:** Keep the key file offline and back it up. Without it no node can be updated from the panel: you would make a new key, build new binaries with it, and update every node and the panel by hand once.

### 2. Build with the public key

```sh
RELEASE_KEY=<public key> make build
```

This builds `bin/mistgate-linux-{amd64,arm64}` and `bin/mistgate-node-linux-{amd64,arm64}` and stamps into both the release key, the version (`git describe --tags --always --dirty`) and the build time (`git log -1 --format=%ct`). The first panel build for an installation must have the key so it can save the public half to `release.pub`; later GitHub panel releases intentionally omit installation-specific keys and reuse that saved file.

### 3. Sign the node binaries

```sh
mistgate release sign --key ~/mistgate-release.key \
  --version "$(git describe --tags --always)" \
  --built "$(git log -1 --format=%ct)" --expires 30d \
  bin/mistgate-node-linux-amd64 bin/mistgate-node-linux-arm64 --out dist/
```

- The binaries must be named `<name>-<os>-<arch>`, as `make build` names them.
- `--built` must be the build time stamped into the binaries: build and sign from the same commit. A new build whose own build time differs from the manifest rolls itself back after the update (`built_mismatch`).
- `--expires` is a number of days (`30d`) or a Go duration (`720h`); the default is `30d`. After that the panel and the nodes refuse the manifest, so a captured old manifest cannot be replayed for long.
- The command writes `dist/manifest.json` and `dist/manifest.sig`, copies the binaries next to them, reads the result back and verifies it, then prints the version, the expiry, every file with its size and the key fingerprint.

### 4. Put the bundle on the panel

```sh
scp dist/* panel.example.com:/var/lib/mistgate/dist/
```

The panel reads `<data-dir>/dist` and notices a change within a minute; **Read the folder again** on the Updates page reads it at once. Only regular files count (a symbolic link is not followed). Replace the whole bundle at once; changing it while a rollout runs pauses the rollout.

The **Release bundle** card shows the result:

| Status | Meaning |
|---|---|
| Signature verified | The signature matches this panel's release key, the manifest is valid and every file is present with the right size and checksum. Only such a bundle can be rolled out. |
| Failed the check | Something is wrong; the card says what (see below). |
| Not checked | This installation has no release key in its build or `release.pub`, so the panel cannot judge the bundle. The nodes still check it themselves. |
| No bundle | `<data-dir>/dist` has no `manifest.json`. |

| Reason | What to do |
|---|---|
| No release bundle in the panel's data directory | Copy the bundle to `<data-dir>/dist`. |
| The signature file (manifest.sig) is missing | Copy `manifest.sig` too. |
| manifest.json is not a valid release manifest | Sign again; do not edit the manifest. |
| The manifest uses a newer format than this panel understands | Update the panel first. |
| The signature does not verify with this panel's release key | The bundle was signed with another key, or the panel was built with another key. |
| The manifest has expired; sign a new one | Sign again with a later `--expires`. |
| A file listed in the manifest is missing | Copy every file of the bundle. |
| A file differs from the manifest (size or checksum) | The copy is incomplete or the file was changed: copy it again. |

The panel serves the bundle's files only to agents with a valid node certificate, through the agent endpoint, at most four downloads at a time, in resumable chunks.

## The Updates page

Everyone can view the page. Starting, pausing, resuming and cancelling a rollout, rolling a node back and **Read the folder again** need the owner and a fresh step-up (see [Security](security.md)).

- **This panel**: its version, build date and release key fingerprint (or "none: this build cannot check bundles"), with the steps to update the panel.
- **Release bundle**: the bundle in `<data-dir>/dist`.
- **Nodes**: every node with its agent version and build date, its state and its last update. The owner gets **Update** and **Roll back** per row.

| Node state | Meaning |
|---|---|
| Up to date | Its build is at least as new as the trusted bundle's (without a trusted bundle, as the panel's own build). |
| Update available | It can update itself and runs an older build. |
| Updating… | A rollout step of this node is in progress. |
| Rolled back | Its last update ended in a rollback; it runs the previous build. |
| Update failed | Its last update failed on the node. |
| Update by hand | Its agent cannot update itself (see "Old agents" below). |
| Offline | The node is not connected; the last known build is shown. |

"No guard against repeated crashes" under a node means its service file has no crash-loop guard: only the agent's own 5-minute self-rollback protects an update there. Run `mistgate-node install` with a new binary on it once to add the guard.

## Rollouts

### Start

**Update all (canary first)** updates every node in the "Update available" state; **Update** in a node's row updates one node. The dialog says which node goes first and how big the batches are.

- **Canary.** The first node is the one with the fewest people online, then the fewest profiles, then by name. It is updated alone.
- **Batches.** After the canary the rest go in batches: 1 node at a time while fewer than 5 nodes are to be updated, otherwise 2. A batch starts only when every node of the earlier ones is decided.
- One rollout runs at a time.
- A rollout ships the bundle as it was at the start. A node that is offline, cannot update itself or is already up to date when its turn comes is skipped.

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

- **Pause**: no new step starts. A step already in progress finishes and goes through its gate. Nodes already updated stay updated.
- **Resume**: the remaining waiting steps go on. Failed and rolled-back steps stay as they are; update those nodes with a new rollout after fixing the cause. If the bundle on disk changed, resume is refused: cancel and start again.
- **Cancel rollout**: waiting nodes are skipped; a node being updated finishes and is checked; updated nodes stay on the new version.

A rollout also pauses by itself:

| Pause reason | What happened |
|---|---|
| a node did not take the update | It rejected the update (the reason is shown). |
| a node did not pass the check after updating | It failed the gate or did not come back; where possible it was put back on the previous version. |
| the release bundle changed | The bundle in `<data-dir>/dist` is now another release. Cancel and start again. |
| the panel restarted during a step | The panel does not know how the step ended. Check the node, then resume or cancel. |

The first, second and fourth also raise the alert "The node update stopped" until you resume, cancel or start a new rollout.

A finished rollout is **Done**, **Cancelled** or **Finished with problems** (at least one node failed or was rolled back). The page shows the active rollout, or the last one. Finished rollouts are kept: always the last 20, older ones for 90 days.

Common step errors:

| Error | Meaning |
|---|---|
| The node rejected the signature | The node's agent trusts another release key. |
| This node's agent was built without a release key | Update it by hand. |
| The release is older than what the node already runs | Sign a newer build. |
| The bundle has no file for this node's system and architecture | Add the binary for its architecture (for example `arm64`). |
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

The Updates page checks the latest stable release from `Mistgate/mistgate` at startup and every six hours. Use **Check GitHub** to check immediately. The panel only downloads the matching Linux amd64 or arm64 asset after confirming its GitHub SHA-256 digest and ELF architecture.

Automatic installation is available when the panel runs as root under systemd. The default unit is `mistgate.service`; set `MISTGATE_UPDATE_SERVICE` or pass `--update-service` if yours has another name. The owner must confirm with a fresh step-up, and an active node rollout must finish first. A detached systemd helper stops the panel, makes a backup of its data directory, replaces the binary and starts the panel. If the service does not stay active, it restores both the previous binary and the database backup. On success the previous binary is kept as `<binary>.prev`; the data snapshot is kept next to the data directory as `<data-dir>.panel-update-backup.tar.gz`.

Pushing a stable `vMAJOR.MINOR.PATCH` tag runs `.github/workflows/release.yml` and publishes panel binaries for Linux amd64 and arm64. Until the first stable release is published, the Updates page reports that no release is available.

For a non-systemd installation or a manual fallback:

1. Build the new version with the same `RELEASE_KEY` (see above), or keep the installation's existing `<data-dir>/release.pub`. Without either, the panel cannot check bundles or start rollouts.
2. Do not update the panel while a rollout is running: pause it or let it finish. A restart in the middle of a step pauses the rollout ("the panel restarted during a step").
3. Copy the new binary next to the installed one, then stop the panel, back up the data directory, replace the binary and start it again. Here the binary is `/usr/local/bin/mistgate` and the systemd unit is `mistgate.service`, as in [Install the panel](../getting-started/install-panel.md); use your own names:

```sh
scp bin/mistgate-linux-amd64 panel.example.com:/usr/local/bin/mistgate.new
# on the panel server, as root
systemctl stop mistgate
tar czf /root/mistgate-backup-$(date +%F).tgz -C /var/lib mistgate
mv /usr/local/bin/mistgate.new /usr/local/bin/mistgate
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

Every API token profile can read the Updates page (`GetUpdates`). Starting, pausing, resuming and cancelling a rollout and rolling a node back are refused over the plain API; an MCP agent with the admin profile can only plan them, and the owner approves each plan in the admin. See [API](../reference/api.md) and [MCP](../reference/mcp.md).
