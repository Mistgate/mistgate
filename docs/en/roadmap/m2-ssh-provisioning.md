---
title: "M2: SSH provisioning and recovery"
description: "M2 status and scope for SSH node provisioning, server access, and encrypted panel backups."
---

**In progress: Go SSH provisioning.** The owner-only Connect API, durable SQLite jobs and events, SSH installer, and server-rendered Go wizard are implemented. Open the wizard by appending `/nodes/install` to the admin URL. It confirms the SSH host key before requesting a password, runs preflight, starts a job, refreshes progress, and supports retry. M2 remains incomplete: cancellation, persistent server-access cards, encrypted backup and restore, and node-removal integration are still planned.

## Goal

From the admin UI, take a supported, empty Linux server to an online Mistgate node in five minutes or less. The current manual flow is documented in [Add a node](../getting-started/add-node.md); M2 is intended to run that setup over SSH and make failures recoverable from the UI.

## Planned scope

### SSH provisioning

- The Go API exposes host-key discovery, SSH preflight, job creation, retry, job status, and redacted events. The Go-rendered admin page at `/nodes/install` provides the installation form, host-key confirmation, preflight summary, progress, and retry.
- Use root password authentication only in this first backend slice. Show the SSH host-key fingerprint and require owner confirmation before sending the password. Pin that exact key for every connection and stop if it changes.
- Run read-only preflight before modifying the server: Ubuntu 22.04+ or Debian 12+, amd64 or arm64, root access, systemd, disk and memory checks, and outbound connectivity to the panel.
- Transfer the matching trusted node agent, verify its signed-bundle digest, enroll it with a one-time token over stdin, install and start its systemd service, then wait for the node to connect.
- Store the root password and enrollment material sealed to the job while it is queued or running; clear them on success or failure. Retry asks for the password again. Durable status and event codes contain no command output or credentials.
- Job retry, recovery after a panel restart, and the UI progress view are implemented. Cancellation remains planned.

### Server access details

- Persistent server access records, password generation or rotation, password reveal, and the server access card are not implemented. The current worker accepts a root password for one job and clears its sealed copy at a terminal state.
- Encrypted backups do not yet include server access records. Passwords, enrollment tokens, and private keys are excluded from job events and audit details.

### Backup and recovery

- Create scheduled encrypted panel backups and store them in the owner's Cloudflare R2 bucket.
- Keep the recovery key under the owner's control and outside the panel, so restoring a backup does not depend on the panel's own secrets.
- Document and support restoring a backup to a clean panel installation.

### Node removal

Extend the existing node retirement flow so that removal also deletes saved SSH credentials, revokes the node identity, invalidates unused enrollment commands, and leaves an audit record. The current node removal behavior is described in [Add a node](../getting-started/add-node.md).

## Acceptance criteria

The M2 criteria remain unmet until cancellation, the persistent access card, backup and restore, and removal integration are implemented.

- An owner can provision a supported, empty Ubuntu 22.04+ or Debian 12+ server and see it online from the UI within five minutes under normal network conditions.
- Provisioning does not change the host until preflight checks pass and the SSH host key has been verified.
- A failed or interrupted attempt shows the failed phase and useful redacted diagnostics; retry does not create duplicate nodes or leave conflicting agent state.
- Server passwords are encrypted at rest, hidden by default, and absent from logs. An owner can reveal or change a saved password from the server access card.
- A scheduled backup can be restored on a clean panel using the owner's recovery key, including the encrypted server access records.
- Removing a node revokes its panel identity and unused enrollment commands, removes its saved SSH credentials, and preserves the audit history.

## Decisions and remaining work

- Initial provisioning uses root password authentication; SSH keys and sudo users are not supported by this backend slice.
- The owner must confirm the displayed SHA-256 SSH host-key fingerprint. Every later connection pins the same fingerprint; a changed key fails closed.
- The job password is not rotated or kept as a saved server credential. A failed job requires the owner to enter the password again.
- Add cancellation for queued and running jobs.
- Decide backup retention, recovery-key rotation, and backup-format compatibility; implement encrypted backup and restore.
- Decide whether node removal retains redacted access metadata; implement removal cleanup and audit integration.
