---
title: "M2: SSH provisioning and recovery"
description: "Planned M2 scope for provisioning nodes over SSH, managing server access, and restoring encrypted panel backups."
---

**Planned.** M2 implementation has not started. This page records the milestone scope and acceptance criteria; it does not describe features available in Mistgate today.

## Goal

From the admin UI, take a supported, empty Linux server to an online Mistgate node in five minutes or less. The current manual flow is documented in [Add a node](../../getting-started/add-node.md); M2 is intended to run that setup over SSH and make failures recoverable from the UI.

## Planned scope

### SSH provisioning wizard

- Collect the server address and SSH connection details in the UI.
- Show the SSH host-key fingerprint and require the owner to verify it before sending credentials. Stop if a previously trusted host key changes.
- Run preflight checks before changing the server: supported OS and architecture, required privileges, systemd, available disk space, and connectivity needed to reach the panel.
- Transfer and verify the matching node agent, enroll it with the panel, install and start its systemd service, then wait for the node to come online. The installed agent continues to connect outbound to the panel; SSH is only for provisioning and later access tasks explicitly started by the owner.
- Keep an installation journal with phase, progress, and redacted output. Make cancellation and retry behavior explicit, and make retries safe after partial failure.

### Server access details

- Let the owner set a root password manually or generate one while adding a server, and support changing it later.
- Store the password in the existing encrypted vault. Keep it hidden by default and reveal it only on an explicit owner action.
- Provide a server access card with IP addresses, SSH port and user, password visibility control, SSH key fingerprint, a copyable SSH command, and optional provider, plan, billing date, and notes.
- Include these access records in the encrypted panel backup. Never put passwords, enrollment tokens, or private keys in installation logs or audit details.

### Backup and recovery

- Create scheduled encrypted panel backups and store them in the owner's Cloudflare R2 bucket.
- Keep the recovery key under the owner's control and outside the panel, so restoring a backup does not depend on the panel's own secrets.
- Document and support restoring a backup to a clean panel installation.

### Node removal

Extend the existing node retirement flow so that removal also deletes saved SSH credentials, revokes the node identity, invalidates unused enrollment commands, and leaves an audit record. The current node removal behavior is described in [Add a node](../../getting-started/add-node.md).

## Acceptance criteria

- An owner can provision a supported, empty Ubuntu 22.04+ or Debian 12+ server and see it online from the UI within five minutes under normal network conditions.
- Provisioning does not change the host until preflight checks pass and the SSH host key has been verified.
- A failed or interrupted attempt shows the failed phase and useful redacted diagnostics; retry does not create duplicate nodes or leave conflicting agent state.
- Server passwords are encrypted at rest, hidden by default, and absent from logs. An owner can reveal or change a saved password from the server access card.
- A scheduled backup can be restored on a clean panel using the owner's recovery key, including the encrypted server access records.
- Removing a node revokes its panel identity and unused enrollment commands, removes its saved SSH credentials, and preserves the audit history.

## Decisions to settle before implementation

- Which SSH authentication methods and users to support, including whether provisioning requires root or supports sudo.
- How host-key verification and later host-key changes are confirmed in the UI.
- When password rotation happens during provisioning, and how to recover if rotation succeeds but a later install step fails.
- Backup retention, recovery-key rotation, and compatibility between backup formats and panel versions.
- Whether node removal deletes access records immediately or retains a redacted record for audit and recovery.
