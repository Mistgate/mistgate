# 0006. Parity between the editions is enforced by tests

Status: accepted, 2026-10-06.

## Context

Two editions with one version are only equal if nothing can land in one and not the other. Discipline does not scale;
CI does.

## Decision

- **Storage conformance suite**: one set of store tests runs against SQLite and against a local D1 (workerd/miniflare),
  including the transaction-shape rule of [0002](0002-database-sql-seam.md) and every migration on an empty database and
  on fixture rows.
- **API coverage check**: a test lists every RPC of `proto/mistgate/admin/v1` and `agent/v1` and fails when one is not
  served by both editions.
- **Shared end-to-end scenarios**: the same scripts run against the VPS binary and the edge build (create a user, fetch
  every subscription format, connect a fake agent, push desired state, run an update step, back up).
- **Backup round trip** (the hard requirement): a fixture panel → render every subscription format and open a page-password
  session → back up → restore into the other edition → render again and compare byte for byte (timestamps masked), check
  the page-password cookie and a device key → back up and return. In both directions, on every release.
- **Release rule**: one tag builds both editions; if either fails to build or fails these tests, there is no release.
- **Docs**: one feature table for both editions; something that does not apply to an environment says so ("not needed:
  Cloudflare issues the certificates") rather than being missing.

## Consequences

- The edge CI job is informative during Phase 1 and required afterwards.
- Moving an instance between editions is a tested operation, not a hope: subscription links, page passwords and device
  keys keep working.
