---
title: "M2: SSH provisioning and recovery"
description: "M2 implementation status for SSH node installation, saved server access, password rotation, backups and restore."
---

**In progress.** The Go SSH installer, MCP plan/apply workflow, encrypted server access and verified password rotation are implemented. Panel backups and restore, plus cancellation of queued or running installs, remain open. The supported install path is documented in [Add a node](../getting-started/add-node.md) and the [AI agent guide](../getting-started/ai-agents.md).

## Goal

From the admin UI or an approved AI-agent plan, take a supported Linux server to an online Mistgate node and make access recoverable without exposing a password in the UI, MCP plan, or logs.

## Implemented

### SSH provisioning

- Owner-only Connect API and Go-rendered `/nodes/install` wizard: public SSH host-key discovery, explicit fingerprint confirmation, preflight summary, durable SQLite job and redacted progress events.
- Root login or an SSH account with non-interactive `sudo -n`; the pinned SSH host key is checked before password authentication and on every later connection.
- Preflight supports Ubuntu 22.04+ or Debian 12+, amd64 or arm64, systemd, disk/memory checks and outbound reachability to the panel.
- The worker transfers the matching binary from the currently trusted signed release bundle, verifies its digest, enrolls with a one-time token over stdin, installs the systemd agent and waits for the node to connect.
- SSH credentials and enrollment material are sealed to the job while work is queued or running, then cleared on terminal completion. Failed jobs can be retried; interrupted jobs are recovered after a panel restart.

### Server access and password changes

- After the agent is online, the verified SSH login is saved as vault ciphertext bound to the node ID. The browser and MCP can list endpoint, username and host-key fingerprint, never the password.
- The owner can rotate the SSH password from `/nodes/install`; an admin-profile MCP agent can use `node_server_password_rotate_plan` and `_apply`. New passwords need at least 12 characters.
- Rotation stores an encrypted pending value before changing the host, uses `chpasswd` over the pinned connection, opens a fresh SSH session with the new password, and only then promotes it. An interrupted rotation can reconcile the old and pending credentials on retry.
- Retiring a node removes its saved SSH access in the same store transaction that retires its identity and revokes its certificates and enrollment tokens.
- Node installation and password rotation through MCP require the owner's approval. MCP plan data and results contain no passwords; the password is accepted only by the corresponding apply call.

### Signed rollout

The node updater already supports canary-first batches, a health gate and automatic rollback. Use the existing rollout flow to update installed agents; SSH provisioning installs the panel's current trusted agent bundle on new nodes.

## Still planned

### Cancellation

Allow an owner to cancel queued work and stop an active SSH worker cleanly. Cancellation must clear temporary credentials and leave a redacted terminal event; it must not claim success when the remote outcome is unknown.

### Backup and restore

- Create scheduled encrypted panel backups and store them in the owner's Cloudflare R2 bucket.
- Keep the recovery key outside the panel and support restoring the database and encrypted server access records on a clean panel.
- Define retention and backup-format compatibility before enabling unattended pruning.

## Acceptance criteria

The SSH installation and saved-access criteria are implemented. M2 is not complete until cancellation and tested backup/restore are available.

- An owner can install a supported Ubuntu or Debian server from the panel or an approved MCP plan and see the node connect.
- No host changes happen before host-key confirmation and successful preflight. The installer fails closed on a changed key or unsafe target.
- Failed and interrupted jobs expose useful stable error codes without command output or credentials; retries do not duplicate the node identity.
- Saved SSH passwords are encrypted at rest and never revealed by reads. Rotation is verified with a new SSH login before committing.
- Retiring a node deletes its saved access, revokes its identity and invalidates unused enrollment tokens.
- A backup restores the encrypted access records and panel state with the owner's recovery key.

## Decisions

- The first release supports password authentication for `root` or a non-root account with passwordless `sudo -n`; SSH private-key authentication is not part of this slice.
- Every SSH connection pins the fingerprint confirmed by the owner. Public target validation blocks private, loopback and link-local addresses.
- Password reveal is intentionally absent. The owner can rotate it and confirm that the replacement works.
- New nodes receive the latest currently trusted signed agent bundle. Existing nodes use the staged rollout system rather than being reinstalled.
