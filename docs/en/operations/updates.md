---
title: Updates
description: How node agents update themselves from bundles you sign, how to update or schedule each node, and how the panel installs GitHub releases.
---

Node agents update themselves, but only to a build signed with your release key. The panel carries the signed bundle to the nodes; every agent checks the signature against the key compiled into its own binary before it replaces itself. After a bundle is published in the latest stable GitHub Release, the panel checks every 10 minutes, verifies the signature and saves it to `dist`. It never starts a node update automatically: use the **Update** button on each node to update it now, or choose a date and time to schedule that node. Updating the panel binary and updating its agent are separate parts of one GitHub Release: a new panel version alone is not enough; the release must also include `manifest.json`, `manifest.sig` and the agent binaries. On supported systemd installations, the Updates page can also install the latest stable panel release from the official GitHub Releases, but only one whose panel manifest (`panel-manifest.json`, `panel-manifest.sig`) is signed with the release key built into the panel. It uses the root-owned helper when the panel service runs as an unprivileged user.

## How trust works

- There is one ed25519 key pair, the **release key**. The private half stays with you, offline. Both binaries carry the public half, stamped in at build time (the release workflow stamps the repository variable `MISTGATE_RELEASE_PUBLIC_KEY` into every official build). The panel also saves it in `<data-dir>/release.pub` on its first start. If the key compiled into the panel and `release.pub` differ, the panel logs an error and trusts no bundle (no rollouts) until you confirm the new key with `mistgate release trust-key`; see [Rotate the release key](#rotate-the-release-key).
- For every release you sign two manifests: the node bundle (`manifest.json`) and the panel manifest (`panel-manifest.json`). Each lists the version, the build time, an expiry date and, for every binary, its name, OS, architecture, size and SHA-256. The panel manifest is signed in its own Ed25519 context, so an agent never accepts it as its update and the panel never installs a node binary as itself.
- The agent checks, in this order: the signature against its compiled-in key, the manifest format, the expiry, that the release is newer than itself, and that the bundle has a file for its OS and architecture. Then it downloads the file from the panel over its mutual-TLS connection and checks the size and the SHA-256. Any failure leaves the installed binary untouched.
- The panel is only a courier: a compromised panel cannot make a node run code you did not sign.
- Releases are ordered by their build time (the Unix time of the source commit), not by the version string. A release with the same build time as the node is "already current"; an older one is refused as a downgrade.
- A node agent built without `RELEASE_KEY` cannot update itself. `mistgate-node version` prints its compiled key fingerprint. A panel built without a key judges bundles with `release.pub` but cannot install panel releases.

## Publish a release

For every stable tag, `.github/workflows/release.yml` runs three jobs. `node` builds both Linux node agents with Go only (no Node.js, no npm packages); `panel` builds the admin SPA with pnpm and then the two panel binaries. Both have read-only access to the repository. `publish`, the only job that may write and the only one that runs no build code, creates a **draft** release with the four binaries, `BUILDINFO` and `SHA256SUMS`. Every action is pinned to a commit SHA. A draft is not visible to panels as the latest release. The workflow requires the repository variable `MISTGATE_RELEASE_PUBLIC_KEY` and stamps it into all four binaries; it must be the public half of your key, the key in already installed agents and in `<data-dir>/release.pub`.

The private key never goes to Actions, and you do not have to trust the CI's binaries: on a machine that holds the key, you build the release from the tag yourself, and `mistgate release sign` signs only binaries that your build reproduces byte for byte. Then you upload the four manifest and signature files and publish the draft. Within 10 minutes panels download and verify the node bundle (nodes stay on their builds until you update or schedule them), and **Check GitHub** offers the panel release.

```sh
VERSION=v0.1.6
git clone https://github.com/Mistgate/mistgate.git && cd mistgate   # or git fetch --tags in your clone
git checkout "$VERSION"
(cd web && pnpm install --frozen-lockfile && pnpm build)           # the panel binary embeds the SPA
go build -o ../mistgate-signer ./cmd/mistgate                       # the signer; any version works
gh release download "$VERSION" --repo Mistgate/mistgate \
  --pattern 'mistgate-linux-*' --pattern 'mistgate-node-linux-*' --dir ../downloaded
../mistgate-signer release sign --key ~/mistgate-release.key --version "$VERSION" --expires 90d \
  ../downloaded/mistgate-node-linux-amd64 ../downloaded/mistgate-node-linux-arm64 \
  ../downloaded/mistgate-linux-amd64 ../downloaded/mistgate-linux-arm64 --out ../signed
gh release upload "$VERSION" ../signed/manifest.json ../signed/manifest.sig \
  ../signed/panel-manifest.json ../signed/panel-manifest.sig --repo Mistgate/mistgate --clobber
gh release edit "$VERSION" --repo Mistgate/mistgate --draft=false
```

`release sign` refuses unless the checkout is exactly the tag without local changes, then rebuilds every binary with `mistgate release build` and the public half of your key and compares it with the downloaded one. The last command publishes the draft: do not publish it before all four files have uploaded. If the public-key variable is missing or invalid, the workflow fails instead of publishing a release that cannot be signed.

If signing refuses because a binary does not match its rebuild, do not sign: find out why first. The usual causes are a different key in `MISTGATE_RELEASE_PUBLIC_KEY`, a tag moved after the build, or a toolchain difference (below); an unexplained difference may mean a compromised runner.

### Reproducible builds

`mistgate release build` (used by the workflow, by `make build` and by the check in `release sign`) gives byte-identical binaries for the same tag and key; a Linux build and a Windows cross-build of one commit were checked to match. It holds when:

- the Go toolchain is the one in the `toolchain` line of `go.mod`. `release build` asks for exactly that version, and a different local Go downloads it once from the Go module proxy;
- the source is a clean checkout of the tag. Untracked files (the built SPA, `bin/`) are not stamped into the binary (`-buildvcs=false`); keep the repository's `.gitattributes`, which checks every file out with LF line endings on every system;
- the flags are the fixed ones `release build` sets: `CGO_ENABLED=0`, `-trimpath`, `GOAMD64=v1`, `GOARM64=v8.0`, and `-ldflags "-s -w"` with only `Version` (the tag), `Built` (the commit time of the tag) and `ReleaseKey`;
- for the panel binaries, the SPA is built with `pnpm install --frozen-lockfile && pnpm build` (Node.js 22, the pnpm version pinned in `web/package.json`).

The check proves that the CI built what the tag says. It does not vet the tag's code or its locked dependencies: a malicious package in `go.sum` or `pnpm-lock.yaml` builds the same on your machine.

### Keep the signature short-lived

`--expires` (default `30d`) is how long both manifests can be installed; a captured old manifest stops working when it runs out. Use a short lifetime such as `90d` and renew it before it ends: run the same `release sign` command for the same tag with a new `--expires` and upload the four files again with `--clobber`. Panels refresh the node bundle of the same build without updating nodes again, and they read the panel manifest anew at every check. A panel that finds an expired panel manifest says so and does not install the release.

### 1. Make the release key (once)

```sh
mistgate release keygen --out ~/mistgate-release.key
```

It writes the private key to the file (mode 0600; an existing file is never overwritten) and prints the public key and its fingerprint (the first 16 hex characters of its SHA-256).

> **Warning:** Keep the key file offline and back it up. Without it no node can be updated from the panel: you would make a new key, build new binaries with it, and update every node and the panel by hand once.

### 2. Build with the public key

```sh
git checkout v0.1.4
RELEASE_KEY=<public key> VERSION=v0.1.4 make build
```

This builds `bin/mistgate-linux-{amd64,arm64}` and `bin/mistgate-node-linux-{amd64,arm64}` with `mistgate release build`. The panel and agents receive the same version, build time (the commit time of the checkout) and release public key. Every binary that should update itself needs the key; the official GitHub builds carry the repository's key. A panel saves its key to `release.pub` on its first start.

### 3. Sign the node binaries

```sh
mistgate release sign --key ~/mistgate-release.key --version v0.1.4 --expires 90d \
  bin/mistgate-node-linux-amd64 bin/mistgate-node-linux-arm64 --out dist/
```

- Run it in the checkout of the tag (or name it with `--source`): it refuses a checkout that is not exactly the tag or has local changes, rebuilds the binaries and refuses one that differs.
- The binaries must be named `<name>-<os>-<arch>`, as `make build` names them. `mistgate-linux-*` binaries given too go into `panel-manifest.json`.
- The build time comes from the tag's commit; a `--built` that differs is refused. A new build whose own build time differs from the manifest rolls itself back after the update (`built_mismatch`).
- `--expires` is a number of days (`90d`) or a Go duration (`2160h`); the default is `30d`. Keep it short and renew it (see [above](#keep-the-signature-short-lived)).
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
| Not checked | This installation has no usable release key: none in its build or `release.pub`, or the two differ (see [Rotate the release key](#rotate-the-release-key)). The panel cannot judge the bundle; the nodes still check it themselves. |
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

Use **Update** in a node's row to start that node's update now. Choose **Schedule** to select a future date and time. The fixed UTC offset in **Settings → System → Update time zone** is used when entering the time; UTC is the default (installations made before this default keep UTC+03:00, which was the default then). Changing this setting does not move existing schedules: each one keeps the exact instant and offset shown when it was saved.

- An update now is pinned to the bundle version the page showed (an MCP plan to the version of the plan): if the trusted bundle was replaced in between, the panel refuses and asks you to review the new version.
- Only one node rollout can run at a time. Other due schedules wait until it finishes.
- A scheduled update is pinned to the signed bundle version selected when the schedule was saved, and cannot be later than that bundle's expiry. If it cannot start at the chosen time (the node is offline, another update is running), the panel keeps trying for two hours; after that the schedule is shown as missed and never starts by itself, so an update meant for a quiet hour does not run whenever the node returns. Schedule it again or cancel it. If the bundle is replaced before then, the panel does not silently substitute the new version; review and schedule it again.
- The panel checks the signed release bundle every 10 minutes, but downloading a bundle never updates a node by itself.

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

The Updates page checks the latest stable release from `Mistgate/mistgate` at startup and every six hours. Use **Check GitHub** to check immediately (a check made in the last three minutes is answered from memory, which keeps the unauthenticated GitHub quota for the bundle download).

A release can be installed only when it carries `panel-manifest.json` and `panel-manifest.sig`, the signature verifies under the release key compiled into this panel (`release.pub` does not count: the panel's service user can write it), the manifest has not expired, and its build time is newer than this panel's. Releases are ordered by that build time, not by the version string: a panel built from a commit after the tag (for example `v0.1.6-3-gabc1234`) never sees `v0.1.6` as an update, so it is never offered a downgrade onto a database it already migrated. A release without such a manifest is shown as "not signed with this panel’s release key: it cannot be installed"; one whose manifest expired, as expired until it is signed again. The card shows the release's build date and the SHA-256 of the binary for this server. **Update panel** sends exactly that version and SHA-256, and the panel refuses if the latest signed release is another one by then.

Automatic installation is available to a root panel under systemd. A panel running as an unprivileged service user such as `mistgate` can use the fixed root helper below; the HTTP panel stays unprivileged. In either case, the owner must confirm with a fresh step-up, and an active node rollout must finish first. The panel writes the confirmed version and SHA-256 to `<data-dir>/panel-update.request` and starts the helper (a transient systemd unit `mistgate-panel-update-<time>.service` for a root panel, `mistgate-panel-update.service` otherwise). The request can only narrow what the helper installs. The helper verifies the release's panel manifest itself with the key compiled into the installed binary, requires it to name exactly the confirmed version and SHA-256 and a build newer than its own, downloads the binary and checks its size, SHA-256 and ELF architecture. Only then does it stop the panel, back up its data directory, replace the binary and start the panel.

The new panel must stay active for 45 seconds without systemd restarting it (with `Restart=on-failure`, a crash a few seconds in shows only as a restart). Otherwise the helper stops it, puts back the previous binary and the data backup, including the original file ownership, and starts the previous panel. On success the previous binary is kept as `<binary>.prev`; the data snapshot is kept next to the data directory as `<data-dir>.panel-update-backup.tar.gz`. If the helper ends without replacing the panel (a failed check or download), the Updates page shows within seconds that the update did not finish, and a rollout or another update can start again; `journalctl -u 'mistgate-panel-update*'` says why.

The data backup is taken while the panel is stopped, before the new binary first runs. If the new panel fails its 45-second check, the database it may already have migrated forward is replaced by that backup, so the previous binary never runs on a schema it does not know; whatever the new panel wrote in those seconds is lost. To go back later by hand, stop the panel, restore the data directory from `<data-dir>.panel-update-backup.tar.gz`, put `<binary>.prev` back and start it; everything changed since the update is lost.

### Enable updates for a non-root panel service

For a systemd panel unit with `User=mistgate`, install the root-owned helper unit and its narrow PolicyKit rule. The rule allows that account to start only `mistgate-panel-update.service`; it does not grant general systemd control or a root shell. The panel unit can keep `NoNewPrivileges=yes`.

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

Pushing a stable `vMAJOR.MINOR.PATCH` tag runs `.github/workflows/release.yml`, which prepares a draft release with panel binaries for Linux amd64 and arm64; panels see it after you sign and publish it (see [Publish a release](#publish-a-release)). Early commit builds such as `0.1.0-<commit>` predate the panel updater, so they can show the release but cannot install it from this page. Replace that binary once using the matching asset for the current stable release from [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest) and the manual steps below. Later builds can update from **Settings → System**: press **Check GitHub**, then **Update panel** when a newer release appears. A root systemd panel can update directly; a non-root panel needs the helper unit and PolicyKit rule above.

For a non-systemd installation or a manual fallback:

1. Build the new version with the same `RELEASE_KEY` (see above). A panel whose compiled-in key differs from `<data-dir>/release.pub` trusts no bundle until you run `mistgate release trust-key` (see [Rotate the release key](#rotate-the-release-key)); a build without a key keeps using `release.pub` for bundles but cannot install panel releases.
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

## Rotate the release key

1. Make a new key with `mistgate release keygen` and set `MISTGATE_RELEASE_PUBLIC_KEY` (or your own `RELEASE_KEY`) to its public half.
2. Every installed agent trusts only the old key: update each node by hand once with an agent built with the new key, as for an [old agent](#old-agents-update-by-hand-once).
3. A panel installs panel releases only under its compiled-in key, which is still the old one: replace the panel binary by hand (see the manual steps above) with one built with the new key.
4. The new panel logs `release key mismatch` and trusts no bundle. On the panel server run `mistgate release trust-key` (as root or the panel's service user, with `--data-dir` if yours is not `/var/lib/mistgate`); it writes the new key to `release.pub` and prints the old and new fingerprints. Restart the panel.

A `release.pub` that differs from the compiled-in key is never accepted silently, in either direction: a key swapped in the data directory does not make the panel trust another signer.

## Updates through the API and MCP

Every API token profile can read the Updates page (`GetUpdates`). Starting, pausing, resuming and cancelling a rollout and rolling a node back are refused over the plain API; an MCP agent with the admin profile can only plan them, and the owner approves each plan in the admin. See [API](../reference/api.md) and [MCP](../reference/mcp.md).
