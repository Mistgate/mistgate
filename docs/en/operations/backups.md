---
title: Encrypted panel backups
description: Configure Cloudflare R2 backups, keep the recovery identity offline, and restore a panel into a fresh data directory.
---

Mistgate can upload scheduled or on-demand panel backups to your Cloudflare R2 bucket. Backups are **off until the owner configures them**. Each archive is encrypted with age before it leaves the panel; Cloudflare stores ciphertext, not the database contents.

## Before enabling backups

### Create an offline recovery identity

Run this once on a trusted machine that is not the panel server:

```sh
mistgate backup keygen --identity-file ./mistgate-recovery.txt
```

The command creates a private age identity without overwriting an existing file, restricts it to owner access, and prints the matching public `age1…` recipient. Keep the identity outside the panel and R2, with a separate offline copy. Paste only the public recipient into the panel. Without the private identity, a backup cannot be decrypted.

### Create the R2 bucket and token

Create a bucket in Cloudflare R2 and an S3 API token limited to that bucket. Mistgate needs object **read, write and delete** access: the storage test lists the backups, writes a temporary object, reads it back (a restore downloads) and deletes it, and configured retention deletes expired backups. Use a dedicated token rather than an account-wide API key.

## Configure the panel

As the owner, open **Settings → Backups** and enter the Cloudflare account ID, bucket jurisdiction, bucket name, R2 access key ID and secret, plus the age recipient. The secret access key is encrypted with the panel master key and never returned by the API. A saved secret stays in place when its field is left blank.

1. Save the settings.
2. Choose **Test R2 access**. It lists the bucket, then writes a temporary object, reads it back and deletes it.
3. Set the interval from 1 to 168 hours and retention. `0` means never prune; otherwise retention must be at least 7 days. Pruning never deletes the three newest backups, however old they are, so a retention as long as the interval still leaves more than one copy.
4. Enable automatic backups and save. The first scheduled run starts within about a minute, then follows the interval. **Create backup now** starts an owner-confirmed backup immediately.

The panel takes a consistent SQLite snapshot, includes the panel data files and the effective `master.key`, creates a manifest with file hashes, encrypts the archive to the public recipient, and uploads it to R2. The private recovery identity is never stored by Mistgate. Keep a copy of it even if R2 is available.

The work happens in a temporary directory inside the data directory, so that disk needs free space for one more copy of the database plus the encrypted archive; the other files are read in place. After a failed scheduled run the next attempt waits 2 minutes, then 4, 8 and so on, never longer than the interval. Saving the settings ends the wait.

## Restore on a new panel

Download the encrypted object from R2 and copy it, the recovery identity, and the `mistgate` binary to the new panel host. Stop Mistgate if it is already running. Restore into a **new, non-existing** data directory; the command refuses to overwrite files:

```sh
mistgate backup restore \
  --identity-file ./mistgate-recovery.txt \
  --file ./backup.tar.gz.age \
  --data-dir /var/lib/mistgate-restored
```

Run the restore as the panel service account, or set the restored directory's owner before starting the service. Point the systemd unit at `/var/lib/mistgate-restored` with `serve --data-dir`. The restored data includes the database, panel CA and master key, so enrolled nodes and encrypted panel secrets can continue working.

If systemd supplies `master.key` through `CREDENTIALS_DIRECTORY`, replace that credential with the restored `master.key` before starting Mistgate. The service must use the restored key to read the restored database.

Keep the old data directory until the restored panel starts and you verify sign-in, nodes and subscriptions. Never restore an archive over a live data directory. Losing the recovery identity makes the R2 archives unusable; losing the restored master key makes the panel's encrypted secrets unreadable.
