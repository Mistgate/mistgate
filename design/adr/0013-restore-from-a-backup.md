# 0013. Restore from a backup: in the panel, and on a new server

Status: accepted, 2026-11-06.

## Context

Backups exist in two forms, both the same encrypted age archive (ADR 0012, `internal/panel/backup`): uploaded to
Cloudflare R2 on a schedule (encrypted to the owner's recovery recipient), and downloaded to the owner's computer
(to the recipient, or with a passphrase made up for the file). Restoring is a command only:
`mistgate backup restore --identity-file|--passphrase|--passphrase-file … --file … --data-dir NEW-DIR`, into a new
directory, on a server where the owner then points the service at it by hand. The two moments an owner needs a restore
are both missing from the product:

1. **The panel runs, and its data must go back** (a mistake, a bad import, a test gone wrong).
2. **The server is gone**, and a new one must become the same panel, so that the nodes and people's links keep working.

The panel cannot decrypt an R2 backup by itself: the recovery identity stays offline on purpose. And a panel cannot
replace the database it is running on: the swap needs the service stopped, which the panel self-update already does
with a root helper (`internal/panel/update/panel_update_helper.go`: a transient systemd unit or the fixed
`mistgate-panel-update.service`; it stops the panel, copies the data directory, swaps, starts, waits 45 seconds and puts
everything back if the new panel does not stay up).

## Decision

### In the running panel (Settings → Backups → **Restore from a backup**)

- **Source**: a file from this computer (uploaded, streamed to the data directory's stage folder, size-capped) or a backup
  from the R2 list (the panel downloads it from the bucket itself).
- **Key**: the recovery identity file (its contents, uploaded) for a recipient archive, or the file's passphrase. It is
  held in memory for the request that decrypts, never stored, logged, audited or returned.
- **Check first, change nothing**: the panel decrypts the archive into a stage folder and verifies it exactly as
  `backup restore` does (manifest, hashes, `master.key`, the SQLite file opens, its migrations are not newer than this
  binary). The window then shows what it is: the panel name and the version that made it, when, and a few counts from
  that database (nodes, people, profiles), next to the same counts now.
- **Then confirm**: a fresh step-up and the panel's name typed, because it replaces everything since that backup.
- **The swap is the update helper's**, in a restore mode: stop the panel; move the current data directory aside to
  `<data-dir>.before-restore-<UTC time>` (kept, not deleted); move the staged data in (ownership and modes as the
  service expects); start; wait 45 seconds; on failure stop, put the previous data directory back and start it. The
  result is reported the way a panel update's is.
- **After it**: the panel runs on the backup's data. Sessions and admins are the backup's, so the window says *you will
  sign in with the password and passkeys you had then*. The restored panel writes `backup.restored` to its audit log
  (source, file name, the version that made it); the old database got `backup.restore_started` before the swap.
- **The data that was replaced** stays in `<data-dir>.before-restore-…`; Settings → Backups lists it with its size and
  **Delete** (owner, step-up). Nothing removes it by itself.
- Owner only, fresh step-up, refused to tokens; one restore at a time, and never while a backup, a panel update or a
  node rollout runs; VPS edition only (the Cloudflare edition keeps its data in D1).

### On a new server (the installer)

- `install.sh --restore FILE` with `--identity-file KEY` or `--passphrase-file FILE` (no passphrase on the command
  line): install the binary as usual, restore into the data directory before the first start, keep the restored
  `master.key`, skip the one-time setup link, and say: *point <the panel's domain> at this server; the nodes reconnect by
  themselves*. `--dry-run` checks the archive and the key without installing.
- The panel's address and its certificates come from the backup; the domain must point at the new server for ACME and
  for the nodes.

## Consequences

- Both restore moments are in the product, and both use the same archive format and verification as `backup restore`.
- The recovery identity can now travel to the server for a restore; it is only in memory for that request.
- A restore can be undone by hand from the kept directory until the owner deletes it.

## Implementation plan

1. Go (Codex): stage + verify + summary (`PrepareRestore`), the upload route and the R2 source, the helper's restore mode
   and its request file, `ConfirmRestore`, the kept-directory listing and delete, audit, policy, the installer's
   `--restore`, tests (verification refusals, a wrong key, a too-new archive, the swap and its rollback with fakes, no
   secret in logs or audit, the stage cleaned on every path, edge refusal).
2. Admin (Claude): **Restore from a backup** in Settings → Backups (source, key, the check with its summary, the typed
   confirmation, the progress, the kept directories), strings in en, ru, zh, fa, tr.
3. Docs (Claude, en/ru/zh): operations/backups.md (both moments), getting-started/install-panel.md (`--restore`).
