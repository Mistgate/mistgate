# Architecture decisions

Short records of decisions that shape Mistgate, with the reason and what follows from them. A decision is changed by a
new record that supersedes the old one, not by editing history.

| # | Decision | Status |
|---|---|---|
| [0001](0001-one-repo-two-editions.md) | One repository, two editions (VPS and Cloudflare), one version | Accepted |
| [0002](0002-database-sql-seam.md) | The storage seam is the `database/sql` driver; transactions follow three shapes | Accepted |
| [0003](0003-one-agent-protocol.md) | One agent link for both editions: WebSocket with a signed challenge | Accepted |
| [0004](0004-worker-gaps-move-to-nodes.md) | What a Worker cannot do moves to the nodes, in both editions | Accepted |
| [0005](0005-one-password-profile.md) | One password hashing profile for both editions | Accepted |
| [0006](0006-parity-by-tests.md) | Parity between the editions is enforced by tests | Accepted |

The plan they serve: [`../cloudflare-edition/README.md`](../cloudflare-edition/README.md).
