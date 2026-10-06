# 0001. One repository, two editions, one version

Status: accepted, 2026-10-06.

## Context

Mistgate runs as one Go binary on a VPS. A Cloudflare edition is wanted as an equal choice at install time — the same
features for the person who would rather not run a server — not as an add-on. Both editions share the database schema,
the admin API, the agent protocol, the web UI and every rule (access, rendering, validation). The spike showed that the
whole panel already compiles for `js/wasm` once one file is swapped.

## Decision

Both editions live in the main repository. The Cloudflare edition is a build target (`cmd/mistgate-edge`, `edge/worker`),
not a fork. One tag releases both: the VPS binaries and agent bundles as today, plus the edge bundle (wasm, Worker script,
static assets), all signed with the same release key. The version number is the same.

## Consequences

- A change to shared code is made once and reaches both editions in the same release; there is no sync step to forget.
- Build tags exist only at the seams (storage open/migrate, HTTP entry, agent transport, file storage, schedulers); shared
  code stays free of them.
- Every pull request builds both editions and runs the shared tests against both (see [0006](0006-parity-by-tests.md)).
  While the edge edition is incomplete, its CI job is informative; it becomes required once Phase 1 lands.
- Contributors to shared code see TypeScript in `edge/`; contributors to the edge see Go. Accepted.
- A separate repository would only pay off with a different release cadence or a different license, neither of which is
  planned.
