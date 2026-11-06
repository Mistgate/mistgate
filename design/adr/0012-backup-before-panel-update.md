# 0012. A backup to download before the panel updates itself

Status: accepted, 2026-11-06.

## Context

The owner updates a VPS panel from the Updates page with one button (**Update panel**). The update itself is careful
(docs/en/operations/updates.md): the panel copies its data while it is stopped, before the new binary's first start, and
rolls back to the old binary and that copy if the new one does not run for 45 seconds. What it does not do is put a copy
in the owner's hands. Home routers (Keenetic, for one) ask before a firmware update and offer to download the settings
to the computer, with a file name that says what it is. The maintainer wants the same.

What already exists and is reused:

- `internal/panel/backup`: a consistent SQLite snapshot, the other files of the data directory and the live
  `master.key`, a manifest of hashes, encrypted with age (`CreateEncryptedArchive(ctx, dataDir, snapshotDB, masterKey,
  appVersion, recipient, created, dst io.Writer)`), uploaded to R2 by the scheduled backups; `mistgate backup restore
  --identity-file … --file … --data-dir NEW` restores into a new directory (`RestoreEncryptedArchive` takes age
  identities). The archive holds every secret of the panel, so it never leaves the server unencrypted.
- The update mutations are owner-only with a fresh step-up (`useStepUp` in the admin).

## Decision

- **A window before the update.** **Update panel** opens *Update the panel to vX?*: what happens (the panel restarts for
  about a minute, nodes and people's connections keep working, the data is copied on the server and the panel rolls back
  by itself if the new version does not start), then *A copy on your computer* with **Download backup**, then **Update**
  (and **Cancel**). The download is a recommendation, not a gate: **Update** works without it, but the window says when
  this browser last downloaded one ("downloaded 2 min ago") so the owner sees it was done.
- **The file.** `mistgate-backup-<panel>-<version>-<UTC yyyy-mm-dd-hhmm>.tar.gz.age`, where `<panel>` is the panel's
  brand name or host, reduced to `[a-z0-9-]`, and `<version>` the running version (the backup is of this version's data;
  restoring it needs a binary at least this new). It is the same archive format as the R2 backups, so the same restore
  command reads it.
- **Encryption, never plain.**
  - With scheduled backups configured, the archive is encrypted to the owner's recovery recipient (the `age1…` / `age1pq1…`
    key in **Settings → Backups**), and restores with the identity file the owner already keeps.
  - Without it, the window asks for a passphrase (at least 12 characters, typed twice) and encrypts with age's scrypt
    recipient. `mistgate backup restore` learns `--passphrase` (asks on the terminal, or reads a file given with
    `--passphrase-file`; never a command-line value).
  - The passphrase goes to the server only in the request body of that one download and is not stored or logged.
- **The endpoint.** A plain HTTP download (a POST under the admin API with the session, the CSRF check and a fresh
  step-up, answering `Content-Disposition: attachment; filename=…`), streaming `CreateEncryptedArchive` into the response:
  no copy of the archive is left on the server; the SQLite snapshot is made in the data directory's stage folder and
  removed afterwards, as the scheduled backup does. One download at a time (the backup service's lock); owner only;
  tokens refused; audit event `backup.download` with the file name, size and whether a recipient or a passphrase was used.
- **Editions.** VPS panel only, like the panel update itself; the Cloudflare edition has no binary to swap and keeps its
  own backup story.
- **Also from Settings → Backups** (**Download a backup now**), so it is not only reachable in the update window.

## Consequences

- An owner who updates has, in one click, a copy that the existing restore command reads, named so it can be found later.
- The server never holds a second copy of the archive, and never sends one unencrypted.
- `restore` grows a passphrase path; the R2 backups keep using recipients only.

## Implementation plan

1. Go (Codex): the streaming download handler, filename rules, passphrase/scrypt encryption, `restore --passphrase`
   / `--passphrase-file`, step-up and policy, audit, tests (archive round trip with both kinds, no file left behind,
   refusal without step-up or for non-owners, filename sanitising).
2. Admin (Claude): the update window and **Download a backup now** in Settings → Backups, strings in en, ru, zh, fa, tr.
3. Docs (Claude, en/ru/zh): operations/updates.md (the window), operations/backups.md (the downloaded copy, the
   passphrase restore).
