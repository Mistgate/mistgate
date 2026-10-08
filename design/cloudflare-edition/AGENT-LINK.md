# Edge edition: the agent link and the session core (phase 1, step 6b-3)

Status (2026-10-08): steps 1a, 1b, 2 and 3 and their review follow-up are merged; steps 4a and 4b are implemented in this
worktree. Steps 4c-6 are designed below, not implemented. Read with [`README.md`](README.md) (the edition plan) and
[ADR 0007](../adr/0007-event-driven-agent-session.md) (the event-driven session core).

## 1. Design in one paragraph

The NodeLink Durable Object (one per node) is thin TypeScript: it holds the agent's WebSocket and the session state as
an opaque string, and never reads a frame. Every handshake step and every session step runs in Go, in the panel wasm
the Worker already loads, reached from the object through the `PanelLink` loopback entrypoint (`ctx.exports`). The
whole session state is one small serialisable value (`SessionState`). Anything large lives in the database: what was
sent to a node (`node_sent`), and, from step 4, the live view (`node_live`). Every change lands in both editions: the
VPS runs the same core with the same tables.

Why not Go inside the Durable Object: a cold panel start costs ~254 ms CPU plus several D1 reads; an object hibernates
after 10 s idle (stats arrive every 10 s) and may lose module globals; objects can share an isolate, and a pending Go
timer would keep all of them awake. A second slim wasm would double the build and the size budget.

## 2. Decisions

### Owner (2026-10-07)
1. Agents enrolled against an edge panel are **link-only** (`enroll --link-url …`): mTLS with an SNI gate cannot work
   behind Cloudflare. The one-time enrolment token travels over public TLS and the agent checks the CA pin itself.
   Cloudflare already holds the database and the master key, so this adds no new trust. Existing VPS agents are
   unchanged.
2. Until phase 2 the edge edition has no agent self-update (needs R2) and no log streaming; the UI says "not available
   in this edition yet".

### Architecture
- **Ordering.** Every NodeLink entry point (message, alarm, close, RPC method) runs inside `blockConcurrencyWhile`:
  one event at a time, in arrival order.
  - The block has a 25 s budget that all its Go calls share; the object resets at 30 s.
  - A failed step closes the socket with 1011, and frames queued behind it are dropped.
- **State before frames.** The object writes the returned state (`storage.kv`) before `ws.send`. The output gate holds
  the sends until the write is durable: a frame the agent sees is never ahead of the state that produced it.
- **Ownership.** The node id is the object's own name (`ctx.id.name`).
  - An accepted LinkAuth bumps a stored generation and closes the other authenticated sockets with 4000.
  - Frames from older generations are dropped.
  - Nothing is stored before a socket authenticates.
- **Alarms.** One alarm per object: the earliest of the session's own alarm (never closer than 1 s), handshake
  deadlines and a pending desired-state poke. An idle session asks for no alarm except its liveness deadline:
  - an ack tick runs only while an ack is owed;
  - the certificate recheck runs on the session's events once due;
  - the first bandwidth measurement is a pending request the core sends itself.
- **Routing.** Go stays the only router. On the edge the link path answers 204 with `X-Mistgate-Link: <node id>`, and
  the Worker forwards the original upgrade to that node's object.
- **Admin → agent.** `NodeLink.ask(requestId, frame, deadlineAt)` takes the caller's absolute deadline, so a request
  that reaches the object after the caller gave up is never sent. A later step's `replies` resolve it.
- **Delta base.** The `node_sent` digest holds a spec hash per inbound and a short hash per credential. It is a delta
  base only when its revision and hash equal what the session last sent. A reconnect whose Hello matches the digest
  continues with deltas. A step whose database writes committed but whose returned state was lost ends in a full
  resend: the agent's base check is the safety net.
- **Store on D1.** D1 has no interactive transactions, so every agent store path is a fixed atomic batch with guards:
  - IngestStats: one read batch, then one write batch guarded on the instance and `last_seq` it read; the inbound
    certificates and AWG device touches ride in it;
  - IngestEvent, NodeHello, CreateEnrollment and RetireNode: one batch each;
  - Enroll: read, sign, then a guarded batch.
- **Constraint.** A step must never await a call into the same node's NodeLink (deadlock until the block budget runs
  out); fan-out goes through `waitUntil`.

## 3. Phases

| Step | What | Status |
|---|---|---|
| 1a | Idle session asks for no alarm; Pending and the L3 health memo in `SessionState`; JSON rehydration test | merged |
| 1b | Sent state as a digest in `node_sent`; deltas from the digest; deltas survive a reconnect | merged |
| 2 | NodeLink Durable Object, PanelLink loopback, shared handshake (`LinkChallenge`/`linkAccept`), marker routing | merged |
| 3 | Agent store paths as atomic batches, safe on D1 (stats 2 D1 calls, event 1, Hello 1) | merged |
| — | Review follow-up: delta base also checks the hash; IngestStats write batch with a fixed statement count; state round-trip test over every field; duplicate SQL | merged |
| 4a | Write the `node_live` projection (sidecar kept, compared by a test) | implemented (2026-10-08) |
| 4b | Every reader on the projection; delete `SessionSidecar` | implemented in this worktree (2026-10-08) |
| 4c | `ask`/`retire`/`drop` seam, `Config.Remote`, remove `SessionState.Capabilities` | designed (§4) |
| 5 | Stateless edge driver `(*Fleet).Link(ctx, LinkIn) LinkOut`; link-only agents (Enroll under the link prefix, renew over the link); Go DO emulator test with the real agent | planned |
| 6 | Wire up on Cloudflare (ops, cron, `scheduled()`, `Remote` callback), local end-to-end run with a real agent, measurements, ADR amendments | planned |

Every step: green gate (`go test -p 2 ./...`, wasm build, worker and web tests), race (fleet, agent, store), bridge
query budgets unchanged (Happ 5, Mihomo 7, page 5/3/4, AWG configs 7), a manager review.

## 4. Step 4 design: the live view as a projection, and the admin seam

### 4.1 Table `node_live` (migration after `00052_node_sent`)

```sql
CREATE TABLE node_live (
    node_id   TEXT PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
    session   INTEGER NOT NULL,              -- SessionState.OwnerGeneration
    drift     INTEGER NOT NULL DEFAULT 0,
    sample_at INTEGER NOT NULL DEFAULT 0,    -- when the panel took the host sample, 0 = none
    rx_bps    INTEGER NOT NULL DEFAULT 0,
    tx_bps    INTEGER NOT NULL DEFAULT 0,
    cpu_pct   INTEGER NOT NULL DEFAULT 0,    -- rounded 0..100
    users     TEXT    NOT NULL DEFAULT '{}', -- user id -> unix start of the newest session on this node
    live_json TEXT    NOT NULL DEFAULT '{}'  -- the admin view
);
```

- **No `seen_at`.** `node.last_seen_at` already plays that part: NodeHello, IngestStats and IngestEvent write it in the
  same batches.
- **Hot columns.** The sample, rx/tx, cpu and users are columns so that subscriptions never parse `live_json`.
- **No client IPs.** Session reports contain no client address, and the panel stores none in its live session view.
- **Session id = `OwnerGeneration`.** It is unique per process on the VPS; on the edge the open event carries the
  object's generation. It is a small integer: D1 refuses integers above 2^53-1.

**Connected** = the row exists, `node.state = 'active'`, and `node.last_seen_at >= now - liveness_timeout_s`. One SQL
for both editions. Retired nodes disappear at once. After a crash or an interrupted takeover, a row looks online until
liveness (90 s).

### 4.2 When it is written (all inside existing batches)

| Event | Where | Statement |
|---|---|---|
| Hello | NodeHello gets `session` | `INSERT OR REPLACE INTO node_live (node_id, session) …`: online right after Hello, a new session always wins |
| Stats (not a duplicate, newer than the last batch) | IngestStats gets a pure `Live` builder called inside the guarded attempt | guarded `UPDATE` of all columns where `node_id` and `session` match; `Live.Apply = false` skips the row |
| ApplyResult | NodeApplied gets `session, drift`; the core computes drift before writing | `UPDATE node_live SET drift = ? WHERE node_id = ? AND session = ?` |
| Session end | EventDisconnected → NodeDisconnected(session) as a batch | update `last_disconnected_at` and delete the row, both guarded by session |

The session guard means a late batch of a superseded session cannot overwrite the row, and its end cannot delete the
new session's row. The `Live` builder must be pure: `retryGuarded` may call it more than once. The core's `LastEndUnix`
orders batches within a session, and Hello resets the row for a new session.

### 4.3 `live_json`

The admin view, ported from today's `applyCoreSnapshot` without mutation:
- metrics: cpu, softirq, load1, ram, disk, rx/tx, uptime;
- health: inbound, run state, detail;
- online sessions: user, device, protocol, inbound, since;
- per-user download rate and the total upload rate.

Everything comes from the batch itself (every agent batch carries the host metrics): IngestStats never reads the
previous row, which would cost up to 1.5 MB per batch on D1.

Bounds:
- 2000 sessions and 64 health entries;
- agent-supplied inbound ids clipped to 64 bytes and details to 256; user, device and protocol values from the database
  are kept as stored.

`users` and `user_down_bps` include the resolved values from the bounded StatsBatch input; session and health arrays
have the explicit view limits above.

Sanitising (the agent may be hostile):
- NaN and Inf become 0;
- rates are clamped to 2^50 and cpu to 0..100.

Without this, one bad value would fail every batch of the node, forever. Measured `live_json` size is ~0.49 MB with
honest values and ~1.3 MB for hostile values because JSON escapes `<` as `\u003c`; `users` adds about 44 B per online
user and is unbounded. `buildFleetLive` leaves the previous row in place when `len(usersJSON)+len(liveJSON)` exceeds
1.5 MB, while the stats accounting still commits (D1's row limit is 2 MB).

### 4.4 Readers

- **Store.** One read, `NodeLive(ctx, now, nodeID, view)`: `""` means all nodes, `view` adds `live_json`, and
  `n.agent_caps` comes in the same JOIN.
- **Subscriptions.** `SubscriptionData` adds one statement to its existing read batch: the node rows plus
  `json_extract(users, '$."<user>"') IS NOT NULL` for the device-online flag. The active batch grows from 7 to 8
  statements and the inactive batch from 4 to 5. The bridge query budgets stay fixed: Happ 5, Mihomo 7, page 5/3/4,
  and AWG configs 7.
- **Every reader of the live session moves to the projection.** They are grepped from `f.session(`, `view.Load`,
  `.Live`, `.caps`, `Online*`, `NetworkUsage`, `CPUUsage` and `AgentConnected`:
  - **fleet:** status, Overview, ListNodes (one read per request), GetNode, the node down sweep, OnlineUsers,
    OnlineByInbound, rollout gates (drift from the row);
  - **access:** load and online on the subscription page; the `NetworkUsage`, `CPUUsage` and `AgentSession` sources
    are deleted. AWG device creation reads `agent_caps` and `last_seen_at` from the existing node rows;
  - **subs:** reads the store directly;
  - **health:** `Live(ctx)` once per pass; an error cancels the pass instead of raising false NODE_DOWN alerts;
  - **update, warp, provision.**
- **Capabilities** always come from `node.agent_caps`. `LastSeenUnix` comes from the database (about 10 s steps).

### 4.5 The admin seam

- **One reply effect.** `EffectCommandResult` and `EffectDoctorReport` merge into `EffectReply{RequestID, Reply}`: the
  agent's whole frame, nil = the request is gone. Pending-request expiry produces `EffectReply{nil}`.
- **Request kind from the frame.** It is derived through an allowlist:
  - RunDoctor → doctor; LogRequest → log;
  - RestartInbound, ApplyFix, UpdateAgent, RollbackAgent, MeasureBandwidth, PrepareAwgKernel, UdpCount, UdpSend → command;
  - Retire → special;
  - anything else, or any request before Hello, is refused with a nil reply.
- **Retire.** The core sends the Retire frame, replies at once and sets `RetireAt = now + 5 s`. At that alarm the core
  closes the session ("node retired"). RetireNode no longer waits for the close.
- **Fleet.** `Fleet.ask(ctx, nodeID, wait, build)` and `command(...)`:
  - VPS: the live session, with one `replies` map instead of `cmds` and `docs`. A reply that arrived just before a
    drop still counts.
  - Edge: `Config.Remote.Ask(ctx, nodeID, requestID, frame, deadline)` and `Close(ctx, nodeID, reason)`. nil and a
    remote timeout map to "no answer", anything else to "link lost". The JavaScript side comes in step 6.
  - The offline and "agent too old" checks read the projection in both editions, with the same error texts.
- **Re-enrol** calls `f.drop(ctx, id, reason)`: on the VPS it cancels the session; on the edge it calls `Remote.Close`.
- **Logs.** `StreamLogs` with `Remote != nil` answers Unimplemented.
- **Capabilities.** `session.caps` is gone. Capability checks read `node.agent_caps`; `SessionState.Capabilities`
  remains until the 4c seam removes it.

### 4.6 Sidecar removal

These are deleted:
- `SessionSidecar`, `LiveSnapshot`, `onlineSess`, `applyCoreSnapshot`;
- the sidecar parameter of `Step` (it becomes `Step(ctx, state, event)`);
- `SidecarVersion`;
- the adapter's `sessionView`, `publishView` and `coreSidecar`.

ADR 0007 then says that the live view is the `node_live` projection, written inline by the core.

### 4.7 Rounds

| Round | Changes | Proof | Risks |
|---|---|---|---|
| 4a | migration, `NodeLive`, the four writes, the `Live` builder; the sidecar stays | SQLite tests; fake D1 smoke run by the lead (Hello replaces an old session, a foreign session's stats update or end is a no-op, duplicate seq writes nothing, retired/stale filters, NaN/2^63 values do not fail a batch); projection = sidecar after a stats series; `node_live` in the rehydration test; 2001 sessions → 2000 | the builder must stay pure under retries |
| 4b (implemented 2026-10-08) | every reader moves (§4.4); the sidecar is deleted | admin/status/health/update/warp/access tests; online right after Hello, offline after close; a stale row → the sweep raises `node_down`; a `Live` error opens no alerts; subscription load and online from rows. The lead still runs `scripts/test-edge-entry.ps1` for bridge query budgets; race tests and a production database subscription diff remain separate checks. | fake clocks moved past liveness without stats now see offline |
| 4c | the seam (§4.5) | allowlist, a request before Hello is refused, the Retire sequence; the existing VPS command/doctor/update/bandwidth/retire/re-enrol tests; a fake `Remote` (frame, request id, deadline, error mapping, no call when offline or too old, StreamLogs) | completeness of the allowlist; RetireNode no longer waits |

After the release that carries step 4: an end-to-end check as the test user (load % and "online" on the subscription
page).

## 5. Notes for steps 5 and 6

- **Open event.** It carries the object's generation (the `node_live` session id). A new session starts from a clean
  `SessionState` and carries over only `Poison`; a stale `SentRevision` would otherwise disable the Hello shortcut.
- **Retire on the edge.** It also deletes the node's NodeLink storage (`ctx.storage.deleteAll()`), or its state lives
  forever.
- **Where Go runs.** `ctx.exports.PanelLink` most likely runs on the object's own thread and isolate. A cold panel start
  then lands inside an object's block, and the 128 MB is shared with NodeLink and Limiter. Measure this in step 6;
  ADR 0007 says "outside the object's request context".
- **D1 batch size.** The IngestStats write batch is a fixed eight statements (json_each, including the guarded
  `node_live` UPDATE, whether or not the projection applies); before step 6, still measure the largest batch on real D1
  (parameter size, rows read).
- **NodeApplied batch size.** It currently has 2 + inbounds statements, with the number of inbounds set by the agent;
  move those updates to `json_each` before step 6.
- **Projection size.** A StatsBatch may name up to 65 536 users, so the `users` column can exceed D1's 2 MB row limit;
  the 1.5 MB valve in section 4.3 keeps the previous row when that would happen. `live_json` keeps the 2000 highest
  per-user download rates (ties by user id), like the 2000 sessions. The admin view only shows top consumers.
- **Billing.** Each stats frame costs about 3 billed row writes on the object (state, alarmAt, setAlarm).
- **Resolved in 4b.** AWG device creation reads `agent_caps` and `last_seen_at` from the existing node rows, so the
  awg/1 check no longer depends on the unimplemented `CapabilitySource`.
