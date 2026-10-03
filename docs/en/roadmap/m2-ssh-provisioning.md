---
title: "M2: SSH installation and recovery"
description: "M2 status and design for node installation over SSH, saved access, cancellation, and encrypted panel backups."
---

**Implemented.** M2 covers provisioning a node from the owner panel or an approved MCP plan, safely retaining and rotating SSH access, cancelling install jobs, and backing up and restoring panel data. The user-facing paths are documented in [Add a node](../getting-started/add-node.md), the [AI agent guide](../getting-started/ai-agents.md), [Encrypted backups](../operations/backups.md), and [Panel updates](../operations/updates.md).

## SSH node installation

- The owner can choose SSH installation from **Nodes → Add node**. The Go-rendered page is at `/nodes/install` under the configured secret admin prefix. MCP exposes the same install through `node_install_plan` and owner-approved `node_install_apply`.
- The first release supports root or password authentication for an account with non-interactive `sudo -n`, Ubuntu 22.04+ or Debian 12+, amd64 or arm64, and systemd.
- The panel obtains the host-key fingerprint before asking for the password. The owner confirms the exact key, then reviews OS, architecture, systemd, resources and panel reachability before confirming any host changes.
- The worker installs the matching agent from the trusted release bundle, enrolls with a one-time token over stdin, starts the systemd service and waits for the agent. The page reports progress and redacted errors.
- Active installs reserve node names. Retired nodes and terminal jobs retain history without reserving their names. A job may enroll its own node, while conflicting manual or SSH installs are rejected.

## Credentials and recovery

- Passwords are sealed to the install job while it is queued or running. Terminal completion clears temporary job credentials. After the agent connects, the verified SSH password is encrypted at rest and readable only by the owner through an operation that does not return the password.
- Password rotation stores an encrypted pending value before changing the host, verifies a fresh SSH login with the new password, then promotes it. A retry reconciles an interrupted rotation.
- The owner can cancel queued work or request cancellation of a running worker. The job ends with a redacted event; if the remote result may be partial, the panel says to inspect the server before retrying.
- Retiring a node revokes its panel identity and unused enrollment tokens, removes saved SSH access, and preserves its history.

## Encrypted backups and restore

- Backups are optional and owner-managed. Mistgate snapshots SQLite and the panel data, encrypts the archive to the configured age recipient before upload, and stores it in Cloudflare R2.
- The owner controls the schedule and retention, can test R2 access and start an on-demand backup. The private age identity is created separately and must stay outside the panel and R2.
- `mistgate backup restore` decrypts and validates an archive, then restores it into a new, non-existing data directory. It refuses traversal, corrupted data and overwrites. If systemd supplies `master.key` as a credential, that credential must be replaced with the restored key before startup.

## Acceptance criteria

M2 is complete when these paths are available in the panel release and pass the repository test suite:

- An owner can install a supported server from the UI and see its agent connect; the manual enrollment command remains available.
- An MCP agent must show the host key and install plan, receive explicit confirmation and owner approval, and send the SSH password only in the apply call.
- Host changes start only after key confirmation, successful preflight and install confirmation. Saved credentials never appear in reads, plans or logs.
- Cancellation, worker restart recovery, failed retries and duplicate names preserve durable history and do not create conflicting live nodes.
- Encrypted backups upload to R2, scheduled runs and retention respect saved settings, and restore validates the archive without overwriting an existing directory.

## Decisions and limits

- SSH private-key authentication is not included in this release; use a password for root or passwordless-sudo access.
- The panel does not create a Cloudflare account or bucket. The owner supplies a bucket-scoped S3 API token with read, write and delete permissions.
- Restoring requires the offline age identity, the encrypted archive, the `mistgate` binary and an empty target data-directory path. Restore is a CLI operation; it does not replace the running service configuration automatically.
