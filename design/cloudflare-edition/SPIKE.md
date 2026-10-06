# Mistgate on Cloudflare: feasibility spike

Date: 2026-10-06. Main repo read: `mistgate-public` @ `2130f1f` (never edited). Everything below was measured on real
Workers unless it says "docs" or "not measured". Numbers come from `wrangler tail` (`cpuTime` / `wallTime` per
invocation, integer ms) and from clients on a residential line, so client-side latency includes ~100 ms of network.

**Verdict in one paragraph.** A full Cloudflare edition is feasible without Containers. The whole panel (`cmd/mistgate`)
compiles to `GOOS=js GOARCH=wasm` once ONE file (`internal/panel/store/store.go`: modernc sqlite, goose, `os`) is
swapped; it is 40.8 MB raw / 8.9 MB gzip and uploads, instantiates and starts on a Worker. The real `store` package
(186 methods, `database/sql`) already runs over a D1 `database/sql` driver written in this spike. The agent link works as
a Durable Object per node with WebSocket hibernation, a signed challenge instead of mTLS and the REAL agent proto. What
does NOT work: mTLS with the panel's own CA (Enterprise only), Connect bidirectional streams (Cloudflare buffers HTTP/2
request bodies: measured), UDP from Workers, and TURN from a Worker (`connect()` refuses Cloudflare IPs). Those move to the
nodes. Main engineering cost is not the compile, it is (a) storage transactions (D1 has no interactive ones), (b) the
HTTP/listener bridge, (c) the second agent transport and (d) per-isolate in-memory state (rate limits, challenges).

Resources created and deleted: see the last section.

---

## 1. Go to WASM on Workers

### 1.1 What compiles

`go build` for `GOOS=js` and `GOOS=wasip1`, Go 1.27.1 (the repo's toolchain), per panel package:

| Package | js/wasm | blocker |
|---|---|---|
| `internal/plugin`, `protocols`, `protocols/hysteria2`, `protocols/awg` (+`vpnkey`, `mimicry`), `pagepass`, `vault`, `httpserver/ratelimit`, `gen/...`, `web` (embed), `node/awg/awgcfg` | OK as is | none |
| `store` | FAILS | `modernc.org/sqlite` -> `modernc.org/libc` ("build constraints exclude all Go files"); also `goose`, `os.OpenFile`/`Chmod`/`VACUUM INTO` |
| `access`, `auth`, `subs`, `subsettings`, `dns`, `instance`, `fleet`, `health`, `httpserver`, `mcp`, `provision`, `update`, `warp`, `backup` | FAIL only transitively | every one imports `store` |
| all of the above with `store.go` replaced | OK | nothing else blocks the compile |
| `cmd/mistgate` with `store.go` replaced | OK, 40.8 MB | `net.Listen`, `os/exec`, file IO compile but do nothing useful at run time |

So the single hard compile blocker is the sqlite driver. `store.Store` is `struct{ W, R *sql.DB }` and every one of
the store files use plain `database/sql`; there is no other storage abstraction to cut, and none is needed:
**the "storage interface" already exists and is `database/sql`**. Two backends = two drivers (modernc for the VPS
build, `d1driver` for the edge build). cgo, reflection-heavy deps and `quic-go` did NOT block (they compile; they
simply cannot run: no UDP, no sockets in the js runtime).

TinyGo is not installed and was not installed. Not measured. It would not help the panel anyway: it would have to
build `yaml.v3`, `encoding/json` on structs, protobuf reflection, go-webauthn; the std-Go build already fits.

`wasip1` works too (same sizes) but is a worse fit: Workers has no WASI, so a JS shim is needed, and a Go reactor
(`-buildmode=c-shared` + `//go:wasmexport`) threw `RangeError: Maximum call stack size exceeded` on the SECOND call in the
same instance (2 of 2 tries, V8 stack; GC runs on the native stack). A fresh instance per request works (27-31 ms CPU
each). `js/wasm` with `wasm_exec.js` is the recommended target; it also lets Go call D1 and await promises.

### 1.2 Sizes (`-trimpath -ldflags "-s -w"`, `scripts/build-wasm.mjs`)

| Artifact | raw | gzip -9 | brotli q9 |
|---|---:|---:|---:|
| core (render + issue + validate: hysteria2, awg, vpn:// keys, Mihomo YAML, vault.Derive, argon2), `GOOS=js` | 7,343,302 | 2,053,475 | 1,715,954 |
| same core, `GOOS=wasip1` reactor | 7,124,132 | 1,996,214 | 1,667,085 |
| REAL `store` package (186 methods) + D1 driver, `GOOS=js` | 5,143,669 | 1,414,820 | 1,181,875 |
| WHOLE `cmd/mistgate` (store.go overlaid), `GOOS=js` | 41,009,725 | 8,861,175 | 7,260,830 |
| same, `GOOS=wasip1` | 40,921,810 | n/m | n/m |

Limit: the current docs say "64 MiB" for Free and Paid, uncompressed, and no separate compressed limit
([Workers limits](https://developers.cloudflare.com/workers/platform/limits/)); the "10 MB compressed" figure is not
in those docs today. Measured: `wrangler deploy` of the 40 MB module was accepted ("Total Upload: 40069 KiB / gzip:
8736 KiB", "Worker Startup Time: 20 ms"). 64 MiB leaves 23 MiB of headroom for the full panel; the web UI (2.4 MB,
75 files) is embedded in that build today and would move to static assets on the edge.
(An earlier estimate said 96.8 MB for the full panel; it did not reproduce: `-s -w` or not, js or wasip1, I get
40.8-41.0 MB. A build without the overlay does not exist, so the difference is probably in what was included.)

### 1.3 Cold start and CPU on a deployed Worker (`mg-spike-core`, `mg-spike-full`)

| What | CPU | Notes |
|---|---:|---|
| Cold isolate, core js/wasm: instantiate + Go runtime init + first full call | 119, 134 ms | client total 365-380 ms vs 125 ms warm (the difference is the cold start) |
| Cold isolate, core wasip1: instantiate + first call | 122 ms | |
| Warm call: issue hysteria2 credential + AWG device (X25519 keys, 3.1 profile defaults) and render URI list, 2x Mihomo, .conf, vpn:// | 5.5 ms (n=200: 1108 ms) | dominated by key generation and YAML |
| wasip1, fresh instance per call | 27-31 ms | cold 131 ms |
| Whole panel, `mistgate version`: first request in a cold isolate | 254 ms | includes compiling the 40 MB module on the isolate |
| Whole panel, re-instantiated on a warm isolate | 84-93 ms | an instance kept for the isolate's life avoids this |
| `vault.Derive` (HKDF) first call incl. Go init | 57 ms | |

"Worker Startup Time" reported by wrangler is 12 ms (core) and 20 ms (full): instantiation is lazy and happens inside the
request, so the 1 s global-scope limit is not at risk. Memory limit is 128 MB per isolate including WASM memory.

Password hashing, same Go instance, `golang.org/x/crypto/argon2`:

| Profile | CPU per hash | Go memory (`runtime.MemStats.Sys`) | Result |
|---|---:|---:|---|
| (a) panel today m=64 MiB, t=3, p=2 | 470-750 ms (first calls 529, 566; four parallel requests 557-746) | 104 -> 168 -> 194 MiB, drops back to 66 MiB when the isolate is recycled | ran in ~20 of 20 calls, no 1102 seen. It exceeds 128 MB on paper, so I would not rely on it |
| (b) OWASP light m=19 MiB, t=2, p=1 | 92-94 ms (first calls 148, 188) | 21-130 MiB (grows because the Go heap does not shrink) | fine |
| (c) WebCrypto PBKDF2-SHA256 100k iterations | 19 ms | n/a | works |
| (c) PBKDF2 at 600k / 1M / 10M | 0 ms | n/a | rejected: `NotSupportedError: Pbkdf2 failed: iteration counts above 100000 are not supported` |

`p=2` does not help: WASM is single-threaded. Recommendation: on the edge keep m=64 MiB but run hashing in a Worker
used for nothing else and set `cpu_ms`, or step down to m=19 MiB/t=2 (hash string carries its parameters, so a VPS->edge
migration keeps verifying old hashes). Login is rare (one admin). Secrets Store read:
`secrets_store_secrets = [{ binding, store_id, secret_name }]` and `await env.BINDING.get()`
([docs](https://developers.cloudflare.com/secrets-store/integrations/workers/); the CLI calls it "open beta"; I did not
create a store). The master key would sit there; the pepper is `vault.Derive("pepper")` of it, which runs in WASM today.

---

## 2. Agent link (Durable Object per node)

`worker/hub` + `agentlink/` (Go client). One DO per `node_id` (`idFromName`), WebSocket Hibernation API, binary frames =
the REAL `ConnectRequest` / `ConnectResponse` of `proto/mistgate/agent/v1/agent.proto` (TypeScript decoded with the
main repo's own generated `agent_pb.ts`, copied by `scripts/sync-gen.mjs`).

Handshake in the spike: the DO sends a text frame with a 32-byte nonce; the client replies with `node_id` and an ECDSA
P-256 / SHA-256 signature (IEEE P1363, what WebCrypto verifies) over `"mistgate-agent-link/1\0" + node_id + "\0" +
nonce`; the DO verifies it with the node's registered public key (in production: taken from the enrolment CSR, so the
key the agent already has) and only then accepts protobuf frames. Auth state lives in `serializeAttachment` so it survives
hibernation.

Measured run (`go run ./agentlink`, output kept in the log of this session):

- A connection signed with a foreign key: closed with 1008 "auth failed". Good key: authenticated in 181 ms.
- `Hello` -> `HelloAck{acked_seq, server_time, settings}`; `StatsBatch` seq 1,2,2,3 -> Acks 1,2,2,3; the DO stored
  `last_seq = 3` and counted 3 batches: duplicate seq was dropped inside `ctx.storage.transactionSync` (the panel's
  "dedupe + apply + store last_seq in ONE DB transaction" maps one to one).
- Admin `push` of `Ping(nonce)` reached the connected agent in 148 ms (HTTP call + WS frame, measured at the client); agent
  answered `Pong`, the DO stored it.
- Idle 25 s with a text keepalive ("ping" -> "pong" via `setWebSocketAutoResponse`, which does not wake the object): the
  DO was evicted and rebuilt (constructor counter 1 -> 2, in-memory age reset to 0 ms), the socket stayed connected,
  SQLite state (`last_seq`, instance) was intact, and the next `push` (nonce 2) reached the agent in 137 ms.
- DO alarm: `setAlarm(now + 1500)` fired at +1500 ms (millisecond precision).
- "One stream, one owner": a newer authenticated socket closes older ones with 4000 (implemented, not exercised by a second client).

Docs ([WebSocket hibernation](https://developers.cloudflare.com/durable-objects/best-practices/websockets/)): no duration
charge while hibernation-eligible; `serializeAttachment` max 16,384 bytes; only server-side sockets hibernate; incoming
WebSocket messages are billed 20:1 ([pricing](https://developers.cloudflare.com/durable-objects/platform/pricing/)); received
message limit 32 MiB ([DO limits](https://developers.cloudflare.com/durable-objects/platform/limits/)).

What does NOT work, measured:

- **Connect bidi stream over HTTP/2 to a Worker.** A Go `net/http` HTTP/2 client sent one line every 2 s on a streaming
  request body and a Worker echoed lines. The response headers arrived only after the request body ended (16.1 s for a
  16 s upload) and all eight echoes were delivered at the same millisecond. No full duplex: the current
  `AgentService.Connect` cannot run on a Worker unchanged. Server streaming (one-way) works: 5 chunks arrived at
  0.2/1.1/2.1/3.1/4.1 s, then 1 MiB. That covers `StreamLogs` (the only server-streaming admin RPC; 132 RPCs in 18
  services) and `FetchUpdate`.
- **mTLS with the panel's CA.** The Workers mTLS binding is for OUTBOUND client certificates
  ([docs](https://developers.cloudflare.com/workers/runtime-apis/bindings/mtls/)). Inbound client certificates on your own CA
  are "BYOCA", Enterprise only ([docs](https://developers.cloudflare.com/ssl/client-certificates/enable-mtls/)); I did not try
  it. The signed challenge replaces mTLS.
- **Cloudflare Spectrum gRPC** ([blog](https://blog.cloudflare.com/grpc-workers/)): inbound TCP `connect()` handler and
  full-duplex gRPC from Containers, "private beta"; it would keep the agent's gRPC stream but is beta and not testable
  here. Not measured. The WebSocket route works today.

---

## 3. D1

Applied to a remote D1 database (`mg-spike-d1`, deleted), file by file with `wrangler d1 execute --remote --file`
(`scripts/d1-remote-apply.mjs`), 38 migration files (numbered 00001 to 00046, with the same gaps as in the repo):

| Run | Result |
|---|---|
| Originals, only the `-- +goose Down` half cut off | 37 of 38 applied. Fails: `00033_reusable_retired_node_names.sql`: "To execute a transaction, please use the state.storage.transaction() ... APIs instead of the SQL BEGIN TRANSACTION or SAVEPOINT statements" (the file has `-- +goose NO TRANSACTION`, `PRAGMA foreign_keys = OFF; BEGIN IMMEDIATE; ... COMMIT;` twice, a table rebuild and four triggers) |
| `scripts/sync-migrations.mjs` output (drops goose directives and `BEGIN/COMMIT/PRAGMA foreign_keys`, adds `PRAGMA defer_foreign_keys = on` when the file turned FKs off) | 38 of 38 applied: 47 tables, 94 indexes, 4 triggers |

Not a problem: `STRICT`, `WITHOUT ROWID`, generated columns, `ALTER TABLE ... RENAME`, triggers, JSON functions all went in.
Caveat: the files ran on EMPTY tables. Table rebuilds with `foreign_keys=OFF` (00014, 00031, 00033) on a database that has
data are the classic hazard (D1 keeps foreign keys on; `ON DELETE CASCADE` still fires with `defer_foreign_keys`,
[docs](https://developers.cloudflare.com/d1/sql-api/sql-statements/)). That only matters for future upgrades of a populated
edge database: the rule for new migrations must be "write it so it also runs without BEGIN/COMMIT/PRAGMA foreign_keys".

Limits that matter ([D1 limits](https://developers.cloudflare.com/d1/platform/limits/), docs): 10 GB per database on
Paid, 30 s per query, 100 bound parameters per query, 100 KB per statement, 2 MB per row, 1,000 queries per Worker
invocation on Paid, 6 simultaneous connections per invocation.

Measured from a Worker (`/d1`, three runs): one `SELECT 1` 36-41 ms, an insert 49-57 ms, a batch of 20 inserts 52-56 ms,
20 sequential selects 758-773 ms (38 ms each), `exec("BEGIN; ...; COMMIT")` refused with the same "use
state.storage.transaction()" error. `db.batch([...])` IS atomic: a failing second statement rolled the first one back
(verified). So D1 latency (~38 ms per round trip here) is the number that shapes the design: a request must use few,
batched queries. D1's location can be hinted at creation.

**Transactions in the store (grep, `store/*.go`): 42 transaction sites in 16 files.** Classified by reading the
function bodies (reads/writes inside the transaction):

- ~17 write-only sites (reads=0): `PutSetupToken`, `ResetPasswordLogin`, `Delete` (dns), `RenewCert`, `NodeApplied`,
  `PutDoctor`, `RollupDaily`, `SetNodeOptions`, `SetSettings`, `ReplaceWarpAccount`, `createNodeProvisionJob`,
  `FinishCancelledNodeProvisionJob`, `updateNodeProvisionJobFromState`, `CompleteNodeProvisionJob`,
  `AddNodeToRunningRollout`, `createRollout`, ... These become one `db.batch()`: the d1driver does exactly that
  (Tx buffers `Exec`, `Commit` = one batch) and `SetSettings` ran through it on a real Worker in 48-129 ms.
- ~22 read-then-write sites: `createFirstAdmin`, `DeletePasskey`, `RecordLoginFailure`, `CreateEnrollment`, `Enroll`
  (3 reads), `NodeHello`, `RetireNode`, `IngestStats` (154 lines, 3 reads + 3 writes, the hot path), `OpenAlert`,
  `InsertProbeCredIdx`, `CreateMCPPlan`, `decideMCPPlan`, `BeginApply`, `TakeMCPPlanOwnerSecret`, `CreateAPIToken`,
  `RevokeAPIToken`, `ClaimNodeProvisionJob`, `RequestCancelNodeProvisionJob`, `RequeueNodeProvisionJobs`, ... These cannot be a
  batch as written. Options, in order of preference: (1) rewrite as one guarded statement (`INSERT ... WHERE NOT EXISTS`,
  `UPDATE ... RETURNING`) so the batch is the transaction; (2) serialise through one Durable Object that owns the writes of
  that area (a DO has synchronous SQLite and `transactionSync`, measured: 1000 inserts in one `transactionSync` inside a
  request, rollback on throw verified, `BEGIN` refused); (3) accept it, for admin-only paths with one writer.
  The agent hot path (`IngestStats`, `NodeHello`, `SkipSeq`, `IngestEvent`) naturally lives in the node's DO (section 2).
- Reads inside a transaction in the current d1driver run immediately and OUTSIDE the batch: a read-modify-write is not safe
  on it, so the driver must stay behind the rule above.

DO SQLite for comparison (measured inside a DO request: 200 selects + 1000 inserts in one `transactionSync`): wall time
inside the object is below the 1 ms clock resolution; the 195 ms client round trip is network. A single "panel DO"
holding the whole database would remove the D1 latency and give real transactions, at the price of one global
object (single location, 1,000 req/s soft limit, 10 GB) and of calling synchronous JS from Go; not tried.

---

## 4. UDP health probes without Containers

- **TURN from a Worker: no-go, measured.** `connect()` to `turn.cloudflare.com` on 5349, 443 and 3478 fails at once
  ("proxy request failed, cannot connect to the specified address"), as do `stun.cloudflare.com:3478`, `1.1.1.1` and
  `example.com:443`: the docs say "Outbound TCP sockets to Cloudflare IP ranges are blocked"
  ([TCP sockets](https://developers.cloudflare.com/workers/runtime-apis/tcp-sockets/)). So a Worker cannot open TLS to
  Cloudflare TURN at all; TURN credentials (and the Realtime TURN key the owner would have had to create) would be useless.
  Nothing for the owner to create. (TURN itself: UDP 3478/443, TCP 3478/80, TLS 5349/443, $0.05/GB egress unless used with
  the SFU, [docs](https://developers.cloudflare.com/realtime/turn/).) A TURN server on one of OUR non-Cloudflare nodes
  would be reachable over TCP, but that is more machinery than the next point.
- **Baseline, recommended: nodes probe.** The panel's dialers are injectable (`health.Config.Dialers`, `Dialer`
  interface) and use `net.ListenUDP`; they belong in the agent (`internal/node`), and the panel (VPS or edge) only
  schedules and stores results. Nodes probing OTHER nodes (cross-node) also measures the path between datacentres, which
  the panel never could.
- **Containers, optional data point** (`mg-spike-udp`, image `docker.io/library/python:3.13-alpine` pulled by
  wrangler without local Docker, entrypoint script passed from the Container class): STUN binding to
  `stun.l.google.com:19302` and `stun.cloudflare.com:3478` succeeded; DNS over UDP to `1.1.1.1:53` succeeded (from
  egress IPs `74.125.250.129`, `162.159.207.0`, `1.1.1.1` as the peers saw them). A QUIC version-negotiation probe to
  1.1.1.1, www.google.com and www.cloudflare.com on 443 got no reply, and the same packet got no reply from my own PC
  either, so the probe itself is not valid: UDP/443 from Containers is NOT established. Instance start took 19-25 s
  (including the image); after a redeploy a new instance id hit "Maximum number of running container instances
  exceeded" until `max_instances` was raised. Containers stay out of the core design.

---

## 5. Everything else: can it run on Workers / DO / D1 / R2 / Cron / Queues

Rough effort S = days, M = 1-2 weeks, L = 3+ weeks, for one engineer with an agent pipeline. Verdict: go / go with
limits / no-go.

| Subsystem | Verdict | What changes | Effort |
|---|---|---|---|
| Subscription rendering (hysteria2 URI, Mihomo YAML incl. AWG, .conf, vpn://), policy, validation, `vault` | go | already runs (section 1); same packages | S |
| Store over D1 (`database/sql`) | go with limits | d1driver done (query, exec, batch); 42 transaction sites to classify and ~22 to rewrite or move into a DO; ~38 ms per query means batching and caching | M |
| Migrations | go with limits | same SQL files; goose cannot run there; runner applies each file as a batch; new-migration rule and CI check (section 3) | S |
| Admin RPC (Connect unary, 132 RPCs / 18 services) | go | it is plain HTTP; run the existing `http.Handler` behind a fetch-to-`ServeHTTP` bridge (nothing in the repo does this yet; not built here) | M |
| `StreamLogs` (server streaming) | go | response streaming measured | S |
| Admin SPA | go | `web/dist` 2.4 MB, 75 files as Workers Static Assets (requests free, [pricing](https://developers.cloudflare.com/workers/platform/pricing/)); SPA fallback and CSP headers move from `httpserver/spa.go` to the Worker | S |
| Auth: passkeys, TOTP, step-up, sessions | go with limits | `go-webauthn` and `auth` compile; sessions already in the DB; challenges, login-failure counters and the rate limiters (`auth/ratelimit.go`, `httpserver/ratelimit`, `subs` token state, ~48 `sync`/`map` matches across 14 files of `auth`) are per-process: move to a DO (or D1 counters; the Workers rate-limit binding is not measured); argon2 see 1.3 | M |
| Subscription page, page password, device self-service | go with limits | `pagepass` (HMAC) and `subs` compile; `tokenState` rate limits and the 10 s cache per token are in memory: one DO keyed by token, or accept per-isolate limits | M |
| Agent link | go | section 2; second transport in the agent; new handshake messages in `agent.proto`; enrolment stores the public key | L |
| Fleet (desired state, delta/full, stats ingest, events, doctor) | go with limits | logic is transport-independent; per-node DO holds session state; hot-path rows in DO SQLite, flushed to D1 hourly by an alarm | M-L |
| SSH provisioning (`provision`, `x/crypto/ssh`) | go with limits | `contextDialer` is injectable; `connect()` to a non-Cloudflare IP works: github.com:22 returned the `SSH-2.0-...` banner in 270-281 ms; Cloudflare-fronted hosts (gitlab.com, 1.1.1.1) are refused; sockets cannot be created in global scope; the SSH handshake itself over a JS-backed `net.Conn` was not tried | M |
| Health probes | no-go on Workers, go on nodes | section 4 | M |
| Updates / rollouts of node agents | go | bundle in R2; `FetchUpdate` is server streaming (works); rollout scheduling by Cron (1-minute granularity, [docs](https://developers.cloudflare.com/workers/configuration/cron-triggers/)) and DO alarms; file IO in `update` becomes R2 | M |
| Panel self-update | go with limits | upload a signed bundle through the Workers API with the owner's token (wrangler's own upload of 40 MB worked); not tried from inside a Worker | M |
| Backups | go | R2 binding replaces the S3 client; `age` encryption is pure Go; the snapshot (`VACUUM INTO`) becomes a D1 export or a table dump; same archive format (section 6) | M |
| MCP (`StreamableHTTPHandler`) | go with limits | plain HTTP; the SDK keeps sessions in memory: stateless mode or a DO; not tried | M |
| Telegram alerts | go | `fetch`, Cron/Queues | S |
| Secrets vault | go | master key in Secrets Store (open beta) or a Worker secret; envelope encryption is the same Go `vault` | S |
| Cron / alarms | go | cron minimum 1 minute, UTC, changes take up to 15 min to propagate; DO alarm precision measured at the millisecond, one alarm per object, retries with backoff ([docs](https://developers.cloudflare.com/durable-objects/api/alarms/)) | S |
| TLS, ACME, decoy site, secret SNI | changes | Cloudflare terminates TLS; the secret-SNI agent endpoint becomes a secret path; the decoy site becomes static assets served for unknown paths | S |
| WARP registration | move to nodes | `internal/node/warp/cfapi` already exists on the node side | S |

Outbound TCP `connect()`: works ([docs](https://developers.cloudflare.com/workers/runtime-apis/tcp-sockets/)): no UDP, no port 25,
no Cloudflare IP ranges, no loopback, sockets not in global scope; an open socket keeps a DO in memory up to 15 minutes.

Platform limits used above, from the docs (WebFetch summaries of the official pages, cross-checked against the
measurements wherever I could): Worker size 64 MiB; startup 1 s; CPU 30 s default / 5 min configurable (`cpu_ms`), cron
15 min; memory 128 MB; subrequests 10,000 on Paid; 6 concurrent outbound connections; request body 100 MB (Free/Pro); DO:
10 GB SQLite per object, 1,000 req/s soft limit per object, 32 MiB received WebSocket message.

---

## 6. Migration between editions

State of a panel = the database plus the master key (everything else is derived: `vault.Derive(label)` gives the page
password key and similar; AWG device keys, the fleet CA key and subscription tokens are rows, vault-encrypted with that
master key). The existing backup is an age-encrypted tar.gz (manifest, `panel.db` snapshot, data directory files,
master key), `internal/panel/backup`. Same archive for both directions, with `panel.sql` (a dump) allowed instead of
`panel.db` because there is no SQLite engine inside a Worker:

- edge -> VPS: dump D1 (export API or a table walk), write `panel.sql`; `mistgate restore` loads it into a new SQLite file
  and runs migrations.
- VPS -> edge: `panel.db` is converted to INSERTs by the Go CLI, imported to D1 (import API / `d1 execute --file`) in a
  clean database after the migrations ran; the master key goes into Secrets Store.
- A hard requirement (links, page password and device keys must keep working) is testable as a
  round trip in CI: fixture panel -> render every subscription format and open a page-password session -> backup ->
  import in a local workerd/miniflare edge build -> render again, compare bytes -> reverse.
- Subscription links: domain, prefix and token are data, so they survive. But a custom domain on a Worker needs the zone
  on Cloudflare; a CNAME-only setup is not available on Free/Pro. If the owner's subscription domain is not on
  Cloudflare, links cannot move without moving DNS.

Details in `SPEC-DRAFT.md`.

---

## 7. Cost on Workers Paid, 5 nodes and 50 users

Prices from the docs ([Workers](https://developers.cloudflare.com/workers/platform/pricing/),
[DO](https://developers.cloudflare.com/durable-objects/platform/pricing/),
[D1](https://developers.cloudflare.com/d1/platform/pricing/)): $5/month minimum; 10 M requests then $0.30/M; 30 M CPU ms then
$0.02/M; DO 1 M requests then $0.15/M, 400k GB-s then $12.50/M GB-s, no duration while hibernation-eligible; DO SQLite 25 B rows
read and 50 M rows written included; D1 25 B reads / 50 M writes / 5 GB included.

My estimate (arithmetic on those prices and my measurements, not a measurement of a running install):

- Agent links: one `StatsBatch` per node per 10 s = 259k messages/node/month, 1.3 M for 5 nodes; at the 20:1 ratio that is ~65k
  billable DO requests. Duration: each wake ~tens of ms. A few thousand GB-s of the 400k included.
- DO writes: ~5 rows per batch is 6.5 M rows/month of 50 M included.
- Subscriptions: 50 users with 5 devices refreshing hourly = 180k requests/month at ~10-15 ms CPU = ~2.5 M CPU ms of 30 M included.
  Re-instantiating the panel per request (90 ms) would cost 16 M CPU ms: reuse the instance per isolate.
- D1, R2 (backups, bundles), cron, static assets: inside the included amounts.

Expected bill: the $5 base plus cents. Cost is not a driver; a runaway loop is the risk (set Worker CPU and subrequest guards).

---

## 8. Blockers and open points

1. Transactions: ~22 read-then-write sites need a rewrite or a DO. This is the main refactor in the shared code.
2. Listener/HTTP bridge: the panel's `serve` is built around `net.Listen`, TLS and ACME; the edge entry needs its own `main`
   that builds the same `http.Handler` and answers `fetch` events. Not built in the spike.
3. Agent transport: WebSocket + signed challenge in the agent; Connect bidi cannot run (measured).
4. Per-isolate in-memory state (rate limits, challenges, caches): needs a DO.
5. Memory: 128 MB per isolate; argon2 at 64 MiB is at the edge of it.
6. Domain: Workers custom domains need the zone on Cloudflare.
7. Not measured: the full panel serving a request; go-webauthn at run time; MCP sessions; SSH handshake over `connect()`;
   Secrets Store; self-update from inside a Worker; long-lived (hours) WebSocket behaviour across Cloudflare deploys;
   TinyGo; wasm-opt; QUIC from Containers.

---

## 9. Cloudflare resources

Created, all under `mg-spike-*`, workers.dev only, no custom domain, route or DNS change:

- Workers: `mg-spike-core`, `mg-spike-hub` (Durable Object class `NodeLink`, SQLite-backed), `mg-spike-full`, `mg-spike-store`,
  `mg-spike-udp` (Durable Object class `Probe`) + Containers application `mg-spike-udp-probe`.
- D1: `mg-spike-d1` (created twice; the first copy deleted to rerun the migration test from empty).

Deleted at the end: all five Workers (`wrangler delete --force`), the container application (`wrangler containers delete`),
the D1 database (`wrangler d1 delete`). No KV namespace and no R2 bucket were created. Nothing that was not named
`mg-spike-*` was touched. No secret was used or created.

## 10. Reproduce

```
node scripts/build-wasm.mjs            # core js + wasip1 + store; add --full for the whole panel
node scripts/sync-gen.mjs              # agent_pb.ts from the main repo
node scripts/sync-migrations.mjs       # goose -> D1-safe SQL (original in ../mistgate-public, converted in migrations/)
go test ./core                         # the pure core on the host
cd worker/hub && npx wrangler deploy   # then: go run ./agentlink -base https://mg-spike-hub.<subdomain>.workers.dev
```
