# Mistgate edition on Cloudflare: specification

Status: ACCEPTED as the plan (2026-10-06), not implemented yet. Measured facts behind it: [`SPIKE.md`](SPIKE.md).
Decisions with their reasons: [`../adr/`](../adr/). A FULL Cloudflare edition kept in parity with the VPS edition: same
features, same release, ONE version number. This document says how to get there; it is not an implementation.

Words: **VPS edition** = today's `mistgate` binary (SQLite, own listener). **Edge edition** = the same Go code built for
`GOOS=js GOARCH=wasm` and run by a thin Worker shell on Cloudflare.

---

## 1. Principles

1. One codebase, one version, one release. The edge edition is a build target, not a fork.
2. The database schema, the agent protocol, the admin API, the web UI and every rule (policy, validation, rendering) are
   shared. Only adapters differ: storage driver, HTTP entry, agent transport, secrets, scheduler, file storage.
3. Anything a Worker cannot do (UDP, long sockets with mTLS, host access) moves to the nodes in BOTH editions, so the
   feature set is identical and the panel never has a "VPS-only" feature.
4. A change that breaks the other edition fails CI. Parity is enforced by tests, not by discipline.

## 2. One repo (recommendation)

Keep the edge edition in the main `mistgate` repository; edition = build target.

Why: one version number and one tag; migrations, `proto/`, `web/` and the Go packages are the same files; a rendering fix
cannot ship to one edition and not the other; one CI, one release pipeline, one issue tracker. The spike measured that the
whole panel already compiles for `js/wasm` with ONE file swapped, so the edge edition is an adapter layer, not a rewrite.

Layout inside the main repo:

```
cmd/mistgate/              VPS entry (unchanged)
cmd/mistgate-edge/         edge entry, //go:build js && wasm: builds the same http.Handler, exposes it to the Worker
edge/worker/               TypeScript shell: fetch router, NodeLink Durable Object, Cron, Queue consumers, static assets
edge/d1driver/             database/sql driver over D1 (spike: d1driver/)
internal/panel/store/      store.go split: open_sqlite.go (!js: modernc, goose, os) and open_d1.go (js: d1driver, migration runner)
web/                       unchanged; its dist goes to Workers Static Assets, not into the wasm
```

Trade-offs against a separate repo (`mistgate-edge`):

| | One repo | Separate repo |
|---|---|---|
| Drift | none by construction | permanent risk; needs a sync bot (`wt-parity`-style) |
| Version | one | two, to be mapped |
| Contributors | must understand build tags, see TypeScript | edge people never touch VPS code |
| VPS regression risk | real: a store/refactor change can break the other build | none |
| CI cost | both builds, plus a parity matrix | two small pipelines |
| Public surface | one README/docs | two |

Mitigation for the one-repo risk: build tags only at the seams listed in section 3; a CI job builds both editions on every
PR and runs the shared test suite against both storage drivers; `edge/` has its own owners but no private copies of
shared code. A separate repo is justified only if the edge edition needs a different release cadence or different
contributors; neither is the plan.

## 3. Shared Go core, two backends

### 3.1 Storage

`store.Store{W, R *sql.DB}` and `database/sql` stay the interface; no new abstraction. Backends:

- VPS: modernc sqlite, WAL, one writer connection + read pool, goose (unchanged).
- Edge: `edge/d1driver` (spike: query, exec, `db.batch`). Optional later: a DO-SQLite driver for the areas that need real
  transactions.

Rules, enforced by a lint/CI test that runs on both drivers:

1. A transaction is either (a) a fixed list of writes, which the d1driver turns into one atomic `db.batch`; or (b) a
   guarded single statement (`INSERT ... WHERE NOT EXISTS`, `UPDATE ... RETURNING`); or (c) declared `Serialised`:
   it runs inside the Durable Object that owns that area. Reads inside a transaction followed by a branch are only allowed
   in (c). Spike count: 42 transaction sites, ~17 write-only (already case a), ~22 read-then-write to rewrite as (b) or move
   to (c). The agent hot path (`IngestStats`, `NodeHello`, `SkipSeq`, `IngestEvent`) goes to the node's DO.
2. No SQL feature outside what D1 and SQLite both accept: no `BEGIN/COMMIT/SAVEPOINT`, no `PRAGMA foreign_keys` in
   migrations (`PRAGMA defer_foreign_keys` is allowed), no `VACUUM INTO` outside the backup code, no more than 100 bound
   parameters per statement, statements under 100 KB.
3. Few round trips: ~38 ms per D1 query was measured. Each admin RPC and each subscription fetch has a query budget (target: <= 6
   sequential queries, the rest batched or cached in the isolate with a short TTL).
4. Hot reads (settings, brand, subscription settings) are cached in the isolate with the same TTLs the code already uses
   (`BrandTTL`, `MinInterval`).

### 3.2 Migrations

Same `internal/panel/store/migrations/*.sql` for both editions. goose cannot run on D1, so the edge build embeds the same
files and applies each as one batch, recording the version in `goose_db_version` (same table, so a backup moves between
editions without touching it). The spike result: with `scripts/sync-migrations.mjs` (drop goose directives and
`BEGIN/COMMIT/PRAGMA foreign_keys`) all 38 files applied to a real D1; unmodified, 37 of 38 do and only
`00033_reusable_retired_node_names.sql` fails.

Rule for new migrations (CI-checked: apply the whole directory to SQLite AND to a local D1 on an empty database and on a
database with fixture rows): no `-- +goose NO TRANSACTION`, no explicit transactions, no `PRAGMA foreign_keys`; a table
rebuild uses `PRAGMA defer_foreign_keys = on` and keeps cascade behaviour in mind. 00033 is grandfathered: the edge runner
uses the converted form of it (generated by the same script at build time, so there is still one source file).

### 3.3 Build tags and the wasm budget

Seams (everything else is shared): `store` open/migrate/snapshot; `httpserver` (listeners, TLS, ACME, SNI split) replaced by the
Worker; `backup` and `update` file IO replaced by R2; `provision` dialer adapter; `health` dialers (moved to nodes);
`cmd/mistgate/serve.go`. In the spike, only `store.go` blocked the compile; the other packages compile for `js` and fail
at run time (no listeners, no files, no `exec`), which is what the adapters replace.

Size: measured full panel 40.8 MB raw / 8.9 MB gzip against the 64 MiB Worker limit (docs: uncompressed; wrangler
accepted the upload). An earlier 96.8 MB estimate did not reproduce. Even so, a budget is part of CI: fail
the build above 50 MB so growth is noticed early. Levers, in the order of cost-benefit: (1) serve `web/dist` as static assets
and drop the embed (-2.4 MB); (2) `//go:build !js` on the AWS SDK (backup uses the R2 binding on the edge) and on `health`
dialers, `hysteria/core`, `quic-go`, `awg` tunnel code once probes live on nodes (the biggest single cut; measure
before relying on it); (3) `wasm-opt -Oz` (not installed here, typically 10-15 %); (4) split rarely used areas
(provision, MCP, backup) into separate Workers behind service bindings. The pure core (render, issue, validate) is 7.3 MB / 2.1 MB
gzip and is the fallback if the full panel ever stops fitting.

Memory is the other budget: 128 MB per isolate including WASM memory; a warm full-panel instance must stay well below it
(not measured, to be measured in phase 1 with a real request mix). Instance reuse per isolate is mandatory
(re-instantiating costs ~90 ms CPU).

### 3.4 State that must not stay in a Worker's memory

Rate limiters and counters (`auth/ratelimit.go`, `httpserver/ratelimit`, subscription `tokenState`, page-password
tries), WebAuthn challenges, MCP sessions, caches that assume a single process. Pattern: one small Durable Object per
key space (`Limiter(ip|token|admin)`, `Challenge(session)`), short TTL; where an approximate limit is enough, a
per-isolate limit is allowed and documented. The code keeps its interfaces; the edge build injects DO-backed
implementations.

## 4. One agent protocol for both editions

Goal: agents do not diverge. Feasible, and measured in the spike.

**Transport 2 ("link"):** WebSocket, binary frames, one `ConnectRequest` / `ConnectResponse` per frame, i.e. the same
messages, the same `seq`/Ack/dedupe/desired-state rules as `AgentService.Connect`. Proposed handshake (replace the
spike's JSON text frames with proto messages `LinkChallenge{nonce, audience}` / `LinkAuth{node_id, signature}` added to
`agent.proto`; additive):

1. Agent opens `wss://<panel>/<agent-path>/link/<node_id>`; the path prefix is a secret like today's secret SNI.
2. Panel sends `LinkChallenge`: 32 random bytes + the panel's host (audience).
3. Agent signs `"mistgate-agent-link/1\0" + node_id + "\0" + audience + "\0" + nonce` with its enrolment key (ECDSA
   P-256, the key whose CSR it already sent at `Enroll`), IEEE P1363 `r||s`, SHA-256.
4. Panel verifies with the public key stored at enrolment, then the normal `Hello` / `HelloAck` follows. A newer
   authenticated link for the same node closes the older one (code 4000), as "ONE STREAM, ONE OWNER" says today.

Measured: wrong key refused with 1008; good key authenticated in 181 ms; `Hello` -> `HelloAck`; duplicate `seq` dropped
by `transactionSync`; admin push reached the agent in ~140 ms; DO hibernated after an idle period while the socket stayed
up and state survived; alarm fired on time.

**Both editions serve both transports.** The VPS panel adds the WebSocket endpoint next to mTLS `Connect`; the edge panel
serves only WebSocket (mTLS with the panel's own CA is Enterprise-only on Cloudflare; Connect's bidirectional stream does
not survive Cloudflare's HTTP/2 request buffering: both measured). Agents advertise `ws-link/1` in `Hello.capabilities`
and the panel's `NodeSettings`/address message says which transport to prefer. mTLS stays for backward compatibility
during the transition (existing nodes keep working untouched) and is retired by the same date-driven mechanism the
proto already uses for the SPIFFE URI migration.

Other agent traffic: `Enroll` / `Renew` are plain unary HTTP (work as is; the enrolment token authenticates, the CSR's public
key is stored). `FetchUpdate` is a server-streaming download; on the edge the same bytes come from R2 with a signed,
short-lived request instead of the client certificate (server streaming was measured to work).

Spectrum gRPC (beta, [blog](https://blog.cloudflare.com/grpc-workers/)) is a later option to keep the gRPC stream; do not
depend on it.

Security notes: signature includes node id, audience and a fresh server nonce (no replay, no relay to another panel);
the DO rejects a connection for a retired node or a revoked key at the handshake; `Renew` rotates the stored public key;
the agent still pins the panel (for WebSocket: TLS to a public CA name, plus a pinned SPKI or the panel's CA fingerprint
from the install command, as today).

## 5. Features that cannot run in a Worker move to the nodes (both editions)

| Feature | Today | Parity plan |
|---|---|---|
| Tunnel health probes (hysteria2 / AWG client dialers, UDP) | the panel dials its own inbounds from the panel host | the agent embeds the dialers from `internal/panel/health` (`Dialers` is already injectable). Each node probes ITSELF and OTHER nodes (cross-node: path quality between datacentres); the panel schedules and stores results (same `health_*` tables). Workers cannot open UDP, and TURN is unreachable (measured). Cloudflare Containers stay optional and out of the core |
| WARP registration | panel registers | the node does it (`internal/node/warp/cfapi` already exists), reports the account/key hash; the panel keeps desired-state ownership |
| SSH provisioning | panel `x/crypto/ssh` over `net.Dialer` | VPS: unchanged. Edge: `provision`'s `contextDialer` gets an adapter over `cloudflare:sockets connect()`; works for non-Cloudflare IPs (github.com:22 banner measured), refused for Cloudflare IPs; a node behind Cloudflare is provisioned manually (install command) |
| Doctor, speedtest, bandwidth, update guard | already on nodes | unchanged |

## 6. Update and release parity

- One release produces: VPS binary + agent bundles (as today) and an edge bundle (`panel.wasm`, Worker JS, static assets,
  manifest, signature by the same release key).
- Agent updates (`update/1`): bundle files in R2 (edge) or on disk (VPS), `FetchUpdate` as above; rollout scheduling by
  Cron (1-minute granularity) and DO alarms instead of in-process tickers.
- **"Update panel" button on the edge** uploads the new signed Worker version through the Cloudflare API
  (`PUT /accounts/{id}/workers/scripts/{name}`, multipart: modules + wasm + metadata with bindings and Durable Object
  migrations) using an API token the owner gives (scope: Workers Scripts Edit, nothing else). Steps: download the bundle
  from the release server, verify the signature with the embedded release key, upload, run D1 migrations on the first
  request of the new version, keep the previous version for rollback (Workers keep versions; rollback is a deployment of
  the previous one). Wrangler's own upload of a 40 MB bundle worked in the spike; calling the API from inside the Worker
  was not tried. The token is stored vault-encrypted like every other secret and can be removed after the update.
- Install: `mistgate edge deploy` (a small CLI, or a "Deploy to Cloudflare" button) creates D1, R2 bucket, the DO
  migration, the Secrets Store entry for the master key, uploads the bundle, prints the setup URL. Same first-run setup
  token flow as the VPS edition.

## 7. Backup and migration VPS <-> edge (hard requirement)

**Requirement:** every subscription link (domain, prefix, token), page password and device key keeps working after a move
in either direction, proven by a backup round-trip test.

State to carry: the database and the master key. Derived: page-password key, pepper (`vault.Derive`). Rows: tokens, prefix and
domain settings, AWG device keys (vault-encrypted), fleet CA, node public keys, passkeys, TOTP secrets, argon2 hashes
(parameters are inside the hash string).

- Archive format stays the existing age-encrypted versioned tar.gz; manifest v2 allows `panel.sql` instead of `panel.db`
  (no SQLite engine in a Worker). edge -> VPS: dump D1 into `panel.sql`; `mistgate restore` builds the SQLite file, runs
  migrations. VPS -> edge: CLI converts `panel.db` to INSERTs, imports into an empty D1 that already ran the migrations,
  puts the master key into Secrets Store.
- Backups on the edge go to R2 (binding), encrypted with the same age recipient.
- **CI round trip (the proof):** fixture panel with users, groups, hysteria2 + AWG devices, a page password, API tokens,
  MCP plan, rollouts -> render every subscription format (URI list, Mihomo YAML, .conf, vpn://) and open a page-password
  session -> backup -> restore into the other edition (VPS binary against SQLite; edge build in local workerd/miniflare
  against a local D1) -> render again and compare byte for byte (timestamps masked) and verify the page-password cookie
  and a device key -> backup again and go back. Runs in both directions on every release.
- Domain: links survive only if the subscription host resolves to the new panel. A Workers custom domain requires the zone
  on Cloudflare (full setup); with a zone elsewhere, the owner has to move DNS or keep a proxy. State this in the installer
  before it starts.

## 8. "Change panel address for nodes" (the migration mechanism)

A feature of both editions, useful beyond migration (domain change, move to a new VPS).

- Admin RPC `SetPanelAddress{address, transport, pin}` and an agent proto message `PanelAddress{address, transport,
  ca_fingerprint | spki_pin, effective_after}` sent over the live link to every connected node (queued per node for the offline ones).
- The agent first **test-connects**: full handshake to the new address (mTLS or signed challenge, `Hello`/`HelloAck`, one
  `Ping`/`Pong`). Only on success does it persist the new address, and it **keeps the old one as fallback** with a grace
  period: if the new panel is unreachable or rejects it within N minutes, the agent returns and reports
  `Event{code: "panel_address_failed"}`.
- Migration flow: (1) install the edge edition from a VPS backup (section 7) with the nodes' public keys imported;
  (2) `SetPanelAddress` to the edge URL; nodes test-connect and move, the VPS panel sees them disappear and the edge sees
  them arrive with `Hello.instance_id`/`seq` continuity (their unacked reliable messages are resent and deduplicated by the
  edge's `last_seq` rows); (3) switch the subscription domain; (4) retire the VPS. The reverse is the same procedure.
- The edge must therefore accept nodes before the DNS cut-over: it serves the agent path on its workers.dev name or a
  temporary host until the domain moves.

## 9. Security model (differences from the VPS edition)

- Cloudflare operates the runtime: it can read memory and the D1 contents, as a VPS provider can read a disk. Application-level
  encryption stays: the `vault` (XChaCha20-Poly1305, AAD = record id) protects profile secrets, device keys and the fleet CA in
  the database; the master key lives in Secrets Store (open beta) or a Worker secret, never in D1.
- Agent trust: signed challenge (section 4); no mTLS on the edge; the CA private key still exists (vault-encrypted) for the
  VPS compatibility period.
- Admin: same auth (passkeys, TOTP, step-up, sessions in D1); rate limits in DOs; the Turnstile hook stays optional.
- Exposure: decoy site for unknown paths as static assets; secret agent path; no secret in the `workers.dev` name.
- Supply chain: edge bundle signed with the release key, verified before any API upload.
- Blast radius: a Cloudflare API token held by the panel (self-update) is scoped to one Worker's scripts and can be revoked
  after use.

## 10. Phased plan

Every step keeps the VPS edition green and unchanged in behaviour: the refactors land in small commits with the full
test suite passing on each.

**Phase 0 (done): spike.** Core and store compile, D1 driver, DO link, measurements.

**Phase 1: MVP: admin + subscriptions + agent link.**
1. Build-tag split of `store`; `open_d1.go`; migration runner; CI applies migrations to SQLite and D1.
2. Classify the 42 transactions; rewrite the ~22 read-then-write sites (guarded statements or DO); add the CI rule.
3. `cmd/mistgate-edge`: http.Handler bridge, static assets, SPA fallback, decoy, first-run setup.
4. Auth on the edge (passkeys, TOTP, step-up, sessions), DO-backed rate limiters and challenges.
5. Subscriptions, page, page password, devices (with the DO limiter).
6. Agent second transport (agent + panel on both editions), `LinkChallenge`/`LinkAuth` in the proto, `NodeLink` DO with
   desired state, stats ingest (DO SQLite -> D1 hourly), events, enrolment storing the key.
7. Install CLI. Wasm size and memory budgets in CI.

**Phase 2: operations.** Node-side health probes (self and cross-node) + panel scheduling for both editions; WARP on
nodes; doctor; update bundles in R2, rollouts on Cron/alarms; backups to R2; Telegram alerts via Cron/Queues; MCP
(stateless or DO sessions).

**Phase 3: parity and migration.** SSH provisioning via `connect()`; panel self-update through the
Cloudflare API; `SetPanelAddress`; backup archive v2; the VPS<->edge round-trip test in CI; install docs, the
domain/zone warning.

**Phase 4: hardening.** Load test with a realistic fleet; cost guards; observability; WebSocket behaviour across Cloudflare
deploys; decision on a DO-SQLite store for the transaction-heavy areas if D1 latency hurts.

Phase 1 alone gives a usable edge panel that manages a fleet.

## 11. Decisions on the open points (2026-10-06)

1. **Domain.** The edge edition requires the subscription domain's zone on Cloudflare DNS (a Worker custom domain needs
   it). The installer says so before it starts; a VPS panel whose zone is elsewhere moves its DNS first.
2. **Password hashing: one profile for both editions** — argon2id with the OWASP minimum (m = 19 MiB, t = 2, p = 1) plus a
   pepper derived from the master key with `vault.Derive`. Measured on a Worker: ~92 ms CPU; the current 64 MiB profile
   reached the 128 MB isolate limit. The parameters live in each hash string, so a hash moves between editions as is and
   an older hash is re-hashed with the new profile at the next successful sign-in. The VPS edition becomes lighter too.
   See [ADR 0005](../adr/0005-one-password-profile.md).
3. **Master key on the edge: a plain Worker secret** now (generally available); Secrets Store once it leaves beta. Never in D1.
4. **Signing: the edge bundle is signed with the same release key** as the VPS binaries and agent bundles, and verified
   before any upload (install and self-update). Release builds already carry the key; "release key: none" is printed only
   by local unsigned builds.
5. **mTLS agents stay until every node has moved to the WebSocket link, plus two releases**, then both editions retire
   mTLS by date (the same date-driven mechanism the agent proto already uses for the SPIFFE URI migration).
