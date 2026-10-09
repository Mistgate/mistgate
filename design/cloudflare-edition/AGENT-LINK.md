# Edge edition: the agent link and the session core (phase 1, step 6b-3)

Status (2026-10-09): steps 1a-5c are merged (step 5 in §6: 5a `cbb8405`, 5b `dc72654`, 5c `34f7e7f`). Step 6 is
designed in §7 and being implemented. Read with [`README.md`](README.md) (the edition plan) and
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
  - RenewCert (an agent renewing its certificate over the link): one 4-statement guarded batch (the guard, the new
    certificate row, the older ones scheduled for revocation, `node.cert_serial`); the guard is described in §6.2;
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
| 4a | Write the `node_live` projection (sidecar kept, compared by a test) | merged (2026-10-08) |
| 4b | Every reader on the projection; delete `SessionSidecar` | merged (`c44e0b3`, 2026-10-08) |
| 4c | `ask`/`retire`/`drop` seam, `Config.Remote` | merged (2026-10-09) |
| 5 | Stateless edge driver `(*Fleet).Link(ctx, LinkIn) (LinkOut, error)`; link-only agents (Enroll under the link prefix, renew over the link); Go DO emulator test with the real agent | merged (§6; `cbb8405`, `dc72654`, `34f7e7f`, 2026-10-09) |
| 6 | Wire up on Cloudflare (ops, cron, `scheduled()`, `Remote` callback), local end-to-end run with a real agent, measurements, ADR amendments | designed (§7); 6a implemented |

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
  - Edge: `Config.Remote.Ask(ctx, nodeID, requestID, frame, deadline)` returns the agent's whole
    `ConnectRequest` reply and an error; `Close(ctx, nodeID, reason)` returns an error. The deadline is absolute. A nil
    reply or a `context`/Connect deadline error maps to "no answer"; any other remote error maps to "link lost" (the
    step 6 adapter turns NodeLink's `timeout` into `context.DeadlineExceeded`; error text is never matched). On the edge
    the core answers nil both when it queues Retire and when it refuses it, so `AgentNotified` there means "the link
    took the request". The JavaScript side comes in step 6.
  - The offline and "agent too old" checks read the projection in both editions, with the same error texts.
- **Re-enrol** calls `f.drop(ctx, id, reason)`: on the VPS it cancels the session; on the edge it calls `Remote.Close`.
  A remote close error is logged and does not undo the completed re-enrolment.
- **Logs.** `StreamLogs` with `Remote != nil` answers Unimplemented.
- **Capabilities.** `session.caps` is gone. Capability checks read `node.agent_caps`; `SessionState.Capabilities`
  was removed in 4b. The request-kind allowlist above was checked against every production admin request builder,
  including the UDP count/send requests; session control frames are not admin requests.

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

- **Open event** and **Retire on the edge**: designed in §6.1 (open carries the generation and keeps only `Poison`;
  retire returns `Forget`, and the object deletes its storage).
- **Where Go runs.** `ctx.exports.PanelLink` most likely runs on the object's own thread and isolate. A cold panel start
  then lands inside an object's block, and the 128 MB is shared with NodeLink and Limiter. Measure this in step 6;
  ADR 0007 says "outside the object's request context".
- **D1 batch size.** The IngestStats write batch is a fixed eight statements (json_each, including the guarded
  `node_live` UPDATE, whether or not the projection applies); before step 6, still measure the largest batch on real D1
  (parameter size, rows read).
- **NodeApplied batch size.** Resolved: one inbound-result UPDATE reads from `json_each`; every call is exactly three
  statements (node row, session-guarded `node_live` drift, and inbound results). The store caps input at 256 results and
  a 1 MiB JSON parameter.
- **Projection size.** A StatsBatch may name up to 65 536 users, so the `users` column can exceed D1's 2 MB row limit;
  the 1.5 MB valve in section 4.3 keeps the previous row when that would happen. `live_json` keeps the 2000 highest
  per-user download rates (ties by user id), like the 2000 sessions. The admin view only shows top consumers.
- **Billing.** Each stats frame costs about 3 billed row writes on the object (state, alarmAt, setAlarm).
- **Resolved in 4b.** AWG device creation reads `agent_caps` and `last_seen_at` from the existing node rows, so the
  awg/1 check no longer depends on the unimplemented `CapabilitySource`.

## 6. Step 5 design: the stateless edge driver and link-only agents

Status (2026-10-09): rounds 5a (the driver), 5b (the emulator test) and 5c (link-only agents) are merged. §6.1 answers the §5 notes "Open event" and "Retire on the edge".
Lead decisions on the open questions are in §6.5.

### 6.1 The driver (`internal/panel/fleet/link_driver.go`)

```go
type LinkKind string // "open" | "frame" | "alarm" | "desired" | "request" | "closed" (panellink.ts LinkEvent.kind)

// LinkIn is one NodeLink event, filled from the object's name, storage and clock.
type LinkIn struct {
	NodeID       string    // ctx.id.name
	State        string    // the previous LinkOut.State; "" before the node's first session
	Kind         LinkKind
	At           time.Time // the object's Date.now()
	Generation   uint64    // open: the generation the accepted LinkAuth stored = OwnerGeneration = node_live.session
	CertSerial   string    // open: from LinkAccept
	CertNotAfter time.Time // open
	Frame        []byte    // frame: one ConnectRequest; request: one ConnectResponse
	RequestID    string    // request
	DeadlineAt   time.Time // request: the caller's absolute deadline
}

type LinkOut struct {
	State   string      // written by the object before it sends Frames
	Frames  [][]byte    // ConnectResponse, in order
	Close   *LinkClose  // after Frames; the object then runs the closed step
	AlarmAt time.Time   // zero = no session alarm
	Replies []LinkReply // Frame nil = refused, expired or gone
	Forget  bool        // the node is retired: delete all storage and the alarm after this block
}
type LinkClose struct { Code uint16; Reason string } // Reason at most 123 bytes
type LinkReply struct { RequestID string; Frame []byte }

func (f *Fleet) Link(ctx context.Context, in LinkIn) (LinkOut, error)
```

`Link` returns an error (the object's existing "close 1011, write nothing" path, `edge/worker/src/nodelink.ts`) when the
state does not decode, `Generation` is 0 on open, a non-open event arrives on an empty or Disconnected state, the core
returns an error, or one call needs more than 4 core steps. Folding errors into `LinkOut` would let the object save a
half-applied state.

One call runs one or more `SessionCore.Step`:

| Kind | Core | Notes |
|---|---|---|
| open | a fresh `SessionState{NodeID, OwnerGeneration: Generation, PeerCert*, Poison: old.Poison}`, then EventOpen | only Poison carries over; HelloDeadline becomes AlarmAt |
| frame | EventHello while `InstanceID == ""`, else EventAgentFrame; an accepted Hello is followed by EventDesiredChanged | HelloAck before DesiredState, as on the VPS; a bad proto closes with 1008 |
| alarm | EventAlarm | |
| desired | EventDesiredChanged; nothing before Hello | the VPS pokes only sessions registered after Hello; a poke between LinkAuth and Hello must not send state before HelloAck |
| request | EventAdminCommand{RequestID, DeadlineAt, frame}; a bad proto gets a nil reply | refusals and Retire are answered in the same call |
| closed | EventDisconnected; nothing on an empty or Disconnected state | NodeDisconnected is guarded by session; `Forget` = `RetireAt` was set |

Effects run inside the call:
- EffectPrepareDesired: `f.prepareDesiredState` inline, then EventDesiredPrepared. No event can arrive during a
  preparation inside one call, so there is one round per trigger and `Preparing` is never stored. A real call needs at
  most 3 steps (Hello, desired, prepared); the cap of 4 turns a future core loop into a 1011.
- EffectReply → `Replies`. EffectUsage and EffectConnectEvents inline, as on the VPS. EffectWarpAttention →
  `dispatchWarpAttention` (own goroutine). EffectLogChunk is dropped (log streaming is Unimplemented on the edge).

Close and alarm: a transition that closes keeps none of its frames and ends the call. `AlarmAt` is the last
transition's NextAlarm. Close codes: CloseConflict → 4000, CloseInternal → 1011, everything else → 1008 (the agent
ignores codes). The reason is clipped to 123 bytes (`store.Clip`): above that WebSocket `close()` throws and the socket
would stay open after its closed step.

Admin requests: `Fleet.ask` → `Remote.Ask` → `NodeLink.ask` (waiter in memory, waits outside the serial block) →
request step. The agent's CommandResult or DoctorReport arrives as a frame step; its `EffectReply` becomes
`LinkOut.Replies`, which the object resolves after writing the state and sending the frames. The core's alarm expires a
request with a nil reply; the object's own timer rejects at the deadline ("timeout" → `context.DeadlineExceeded` in the
step 6 adapter). A 1011 or an object reset rejects every waiter with "link lost". `Remote.Close` → `NodeLink.close(4000,
reason)` → closed step.

Retire: `notifyRetire` sends a request; the request step queues Retire, replies nil at once and sets `RetireAt`; the
agent closes the socket or the alarm closes it with 1008 "node retired"; the closed step sees `RetireAt` and returns
`Forget`; the object calls `deleteAll()` and `deleteAlarm()` after the block. `store.RetireNode` commits before the frame
is sent, so `RetireAt` always means "retired". A node retired while offline keeps `gen` and its closed state in the
object (under 300 B): an accepted ceiling.

Implementation notes (5a), decisions the design left open:
- **One call keeps the alarm.** `AlarmAt` starts as the alarm of the state as it was (`nextSessionAlarm`) and every core
  step overwrites it. A step that does nothing (desired before Hello, an undecodable request) must still return the
  alarm: the object deletes `alarmAt` when `LinkOut.AlarmAt` is zero, which would lose the Hello deadline.
- **Closed is the exception to "non-open event on an empty or Disconnected state is an error".** The table says the closed
  step does nothing there, and the object runs it once per socket, so it returns the state unchanged. Every other kind
  (frame, alarm, desired, request) on such a state is an error. A state that names another node, an empty node id and an
  unknown kind are errors too.
- **A closing transition drops its effects, not only its frames**, like `session.step` on the VPS (a pending request then
  fails with "link lost" through the object's waiters, not with a nil reply). Earlier transitions of the same call keep
  their frames (HelloAck stays when the desired step closes).
- **Bad bytes.** An undecodable frame closes with 1008 ("invalid ConnectRequest frame"); an undecodable request frame gets
  a nil reply and no step.
- **A state the driver cannot use** (undecodable, or naming another node) fails every kind but `open`: open logs a
  warning (node id and a reason, no content) and starts clean without Poison, so a new LinkAuth repairs a corrupt object.
- **A call past its context writes nothing**: `finish` returns the context error, so the object keeps its old state.
- **A request already past its deadline** (`At >= DeadlineAt`) gets a nil reply and is never stepped or sent.
- **Preparation errors** go to the core as `EventDesiredPrepared{Err}` (retry in 3 s through the alarm), as on the VPS;
  `Preparing` is cleared on the way out of a closing call too.
- **Shared with the VPS adapter** (`agent.go`): `Fleet.runSharedEffect` (usage, WARP attention, connect events) and
  `Fleet.preparedEvent` (read the desired state, build the core event). Not shared: the Hello-then-desired order, reply
  delivery, log chunks and the preparation trigger (goroutine on the VPS, inline here).
- **`LinkHandler` / `LinkMarker`.** `app.Build` already mounts `LinkHandler` under the link prefix; it now returns
  `LinkMarker` when `Config.Remote` is set. `LinkMarker` stays exported for the edge entry test hook.

Object storage is unchanged from step 2 (kv: `gen`, `live`, `state`, `alarmAt`, `poke`; attachment: nonce, deadline,
audience, generation). Poison lives inside `state`.

D1 calls per event: open 0; Hello about 6 + k (NodeHello batch, Node, connect-event read and insert,
NodeWithSentDigest, k access reads, NodeDesired); stats 2; alarm and request 0, plus 1 CertStatus when the 30 s recheck
is due; closed 1.

### 6.2 Link-only agents

Panel:
- Enrol route: `<link prefix>/mistgate.agent.v1.EnrollmentService/Enroll`, a Connect unary POST with the same
  EnrollRequest/EnrollResponse and the same `enrollmentService.Enroll` (limiter, one-time token, P-256 CSR,
  `store.Enroll`, re-enrolment calls `f.drop`). `LinkHandler` checks this path before `/link/<id>`;
  `httpserver.agentLinkRequest` admits this one extra path. Renew is not routed here (it needs a node identity).
- One mount for both editions: `LinkHandler` answers with the marker when `Remote != nil`, else serves the socket. Step 6
  then only stores `link_prefix` on the edge and sets `Remote`.
- Install command: when `Remote != nil`, `CreateEnrollment` returns `… enroll --link-url wss://de1.example.com/<prefix>/
  --ca-sha256 <fp> --token <t> && … install`, built from a new `Config.LinkURL` (`wss://` + PublicURL host + LinkPrefix).
- mTLS enrolment is already refused on the edge (no agent SNI → `agentMiddleware` 404); a test pins this.
- Renew over the link: proto, additive: `ConnectRequest.renew` (RenewRequest) and `ConnectResponse.renew`
  (RenewResponse). A new `agentFrame` case runs `parseCSR`, `ca.issueNode`, `st.RenewCert(cert, fromSerial, now, oldCertGrace)` and
  sends the answer. RenewCert is one guarded 4-statement batch on D1 (§2 "Store on D1"). A bad CSR closes with
  InvalidArgument, a refusal (below) with FailedPrecondition, a store error with Internal. The session keeps the old serial; its recheck ends the
  session when the grace runs out and the agent comes back on the new certificate (as an mTLS stream does today). An old
  panel ignores the frame; the agent times out and retries in an hour.
- Renew guard (both paths, one place: the RenewCert batch). The certificate that presented the request (`fromSerial`: the
  mTLS peer certificate, or the session's `PeerCertSerial`) must still be valid for the node (known, not expired, not
  revoked; a certificate inside its renewal grace counts as valid, so a retry after a lost answer works) and the node must
  not be retired; and the node may get at most 4 certificates (enrolment or renewal) per hour. Without the guard a renew
  frame already queued when the owner re-enrolled the node (the edge `drop` waits behind frames) would commit a
  certificate for an old, possibly stolen key and schedule the re-enrolment certificate for revocation. Refusal is
  `store.ErrRenewRefused`: FailedPrecondition on both paths, nothing written.
- Renewal grace is `oldCertGrace` = 3 hours. A renewal whose answer is lost leaves the agent with the old key only; it
  asks again on the next renewLoop tick (an hour, 20 s without a session), so the old certificate must outlast that tick.
  Re-enrolment and retirement still revoke every certificate at once.

Agent (`cmd/mistgate-node`, `internal/node/agent`):
- `enroll --link-url wss://de1.example.com/<prefix>/ --ca-sha256 … (--token | --token-stdin)`; `--link-url` cannot be
  combined with `--panel` or `--sni`. The agent POSTs over public TLS (system roots), then runs the existing checks: the
  returned CA's hash equals the pin, the leaf is ours and chains to it. Public TLS only protects the token; trust in the
  panel comes from the pin and then from LinkAccept.
- `Meta.Link` is saved as `"link"` in agent.json (Panel and SNI empty); `linkOnly := meta.Link != ""`.
- A link-only agent always dials the link: no mTLS, no hold period, `linkAdvertised` ignored; it keeps the reconnect
  backoff on `errLinkNotEstablished` (no hot loop against a dead panel).
- Renewal sends `ConnectRequest.renew` on the current session and waits up to 2 minutes; with no session it retries at
  the next hourly tick (10 days of slack).
- `capabilities()` leaves out `update/1` for link-only agents (FetchUpdate is mTLS; owner decision 2).
- VPS agents are unchanged: `Meta.Link` is empty, every branch behaves as today.

Implementation notes (5c), decisions the design left open:
- **Proto.** `ConnectRequest.renew = 18` (RenewRequest), `ConnectResponse.renew = 27` (RenewResponse); the Go code only is
  regenerated (`go tool buf generate` with the Go plugins of `buf.gen.yaml`). `web/src/gen/.../agent_pb.ts` is the same
  output (the lead ran it; `web/src/gen/.../agent_pb.ts` is current).
- **Enrol route.** `Fleet.LinkHandler` answers the enrol POST first (a Connect unary handler over the same
  `enrollmentService.Enroll`, request cap 16 KiB), then the link socket (VPS) or the marker (edge). `httpserver.agentLinkRequest`
  admits exactly `/mistgate.agent.v1.EnrollmentService/Enroll`; any other method on it, and Renew, are 404 and become the decoy.
- **Install command.** `fleet.Config.LinkURL` (`wss://` + PublicURL host + `LinkPrefix`, built in `app.Build`) is used only when
  `Remote != nil`: then `CreateEnrollment` needs the link address instead of `PanelAddr`. SSH provisioning still builds the mTLS command.
- **Renew in the core.** `Fleet.renewNodeCert` is shared by the mTLS `Renew` and the new `agentFrame` case. A bad CSR closes
  InvalidArgument with the CSR error text (never the bytes), a store error Internal ("internal error"); the session state keeps
  `PeerCertSerial`. A renew frame is not a request kind (it is `ConnectResponse.renew` going to the agent): the 4c allowlist test lists it with the
  panel-sent frames.
- **Agent.** `New` takes `meta.Link` as `Config.LinkURL` (the flag or environment link of `run` is overridden). `linkOnly()` is
  `meta.Link != ""`: `session()` always calls `linkSession`; `connectLoop` backs off on `errLinkNotEstablished` like on any lost
  connection. `capabilities()` leaves out `update/1` and `update-guard/1` (the guard only makes sense with the update). The enrol
  client for `--link-url` follows no redirect (the token is in the body). A renewal whose answer is lost with the session leaves
  the agent on the old key: the old certificate is valid for the 3-hour grace, so the next try (renewLoop, hourly) works; scenario 7b
  covers it. A link-only agent that finds no session retries after 20 s, not an hour. The enrol client reads at most 1 MiB, the
  panel's enrol route at most 32 KiB; a transport error is printed without the URL (its path is the secret prefix); a pin
  mismatch says the token is used up.

### 6.3 The Go NodeLink emulator test

Files: `internal/node/agent/nodelink_emu_test.go` (the emulator) and `internal/node/agent/edge_link_test.go` (the
scenarios), next to `link_test.go`; driver unit tests with raw frames in `internal/panel/fleet/link_driver_test.go`.

The emulator implements `fleet.Remote` and plays the Worker and NodeLink:
- Worker: `<prefix>/…` goes to `f.LinkHandler()` with `Remote` set; a 204 with `X-Mistgate-Link` hands the upgrade to that
  node's object (like `forwardLink`); the enrol POST is answered directly.
- Serial block: one goroutine per object draining a FIFO, events in arrival order; `ask` waits outside the block. A block
  budget (25 s, shortened in tests): `f.Link` runs raced against what is left; a late result is thrown away.
- fetch: `LinkChallenge` outside the block; inside: accept, attachment `{nonce, deadline, audience, generation: 0}`,
  challenge, arm.
- message: from a closed socket dropped; generation 0 → `LinkAccept`, `gen++`, other authenticated sockets closed 4000,
  `live = gen`, accept frame, open step; generation == `live` → frame step; else dropped.
- run: write the state, send the frames, apply the replies, set `alarmAt` (≥ now + 1 s), then close if asked. An error or
  a spent budget closes 1011, rejects waiters "link lost", writes nothing. `Forget` clears storage and the timer.
- One alarm timer re-armed after every block from `alarmAt`, handshake deadlines and `poke`, like `arm()`.
- Clock: `time.Now()` plus an offset, also `fleet.Config.Now`; `advance(d)` and `runAlarm(node)`.
- Own-node guard: `f.Link` gets a ctx tagged with its node; `Remote.Ask`/`Close`/`Poke` for that node with that ctx fails
  the test (a step must never await its own NodeLink).
- Faults: `failNext()`, `reset()` (sockets vanish without close events, waiters dropped, storage kept), `loseState()` (the
  harsher case, see the 5b notes).
- A step log `[]{gen, kind}` for assertions.

Scenarios (real agent, fake engine, short stats interval):
1. Enrol → Hello → desired → stats → ask: `node_live.session = 1`, the inbound applied, the sample in `node_live` and
   traffic rows; `f.RunDoctor` returns the agent's report through `Replies`. (5b: mTLS enrolment with the link preset;
   5c: link enrolment, `enroll --link-url`; every scenario below runs with a link-only agent.)
2. Reconnect with a delta base: `reset()`, the alarm drops the session, the agent reconnects as gen 2, its Hello matches
   the digest, no DesiredState; a new credential + poke gives a delta with `BaseRevision`; a desired step that fails after
   its writes committed (`failNext`) closes 1011 and sends nothing, the agent reconnects, its Hello does not match the
   digest, exactly one full DesiredState follows and the agent converges (hash matches); `loseState()` after a desired
   step also ends in a full resend.
3. Liveness: `advance(liveness + 1 s)` + `runAlarm` closes with 1008; the `node_live` row is gone; the agent reconnects as
   gen + 1.
4. Retire: `AgentNotified`, the agent wipes its state and `Run` returns `ErrRetired`; `Forget` leaves no storage and no
   alarm; also the 5 s alarm path with a raw client that never closes.
5. Superseded socket: a raw client signs LinkAuth with the agent's key; the agent's socket gets 4000 and its later frames
   are not stepped (and some were queued: the test asserts they were dropped); the old generation's closed step runs inside the
   takeover, before the new Hello, so the session guard of `NodeDisconnected` is covered by the 5a driver tests, not here; the
   agent comes back as gen 3.
6. Failed step: `failNext()` during stats → 1011, queued frames dropped, reconnect, every batch counted exactly once.
7. (5c) Renew over the link: a new `cert_serial`; after the grace one frame's recheck closes the old session and the agent
   reconnects with the new certificate. Scenario 7b: the answer is lost (the step commits, the socket closes 1011), the
   agent reconnects on the old certificate, renews again inside the grace and is still accepted after it. The agent enrols with the emulated clock 25 days back, so its certificate is five
   days from the end and the renewal loop (20 ms here) asks at once; its first tries come before the session exists and
   find none.

Implementation notes (5b), decisions the design left open and where the emulator differs from `nodelink.ts`:
- **Port, not model.** Every method of the emulator's `linkObject` is a port of the method of the same name in
  `nodelink.ts` (fetch, webSocketMessage, webSocketClose, alarm, poke, ask, close, authenticate, run, dropLive, lost, arm,
  rejectWaiters). The Worker is `Worker(prefix)`: the panel's marker handler picks the node, the object takes the upgrade.
- **Differences from `nodelink.ts` (step 6 closes them in TypeScript).** The emulator sends `Generation` on the open event
  and honours `LinkOut.Forget` (`deleteAll` and `deleteAlarm` after the block); `nodelink.ts` does neither yet (§6.6).
  `webSocketError` is not emulated: a failed read is a close event with code 1006.
- **What the platform decides, here chosen.** A socket stops being open when the object closes it, or when its close event
  is delivered (frames that arrived before a peer's close are still stepped; after the object's own close they are dropped).
  `ws.close()` throws, and the socket stays open, for a reserved code (1005, 1006, 1015) and for a reason over 123 bytes,
  as in workerd. `reset()` fails the RPCs of waiting `ask` calls with "link lost" (an object reset breaks its callers'
  RPCs). The alarm is a real timer in emulated time; `advance` does not re-arm it. `runAlarm` is the platform calling
  `alarm()`: it fails the test unless an alarm is armed and due (so a missing `arm()` shows), and `advanceToAlarm` moves
  the clock to the armed time first. Only the budget and the step limit can be shortened (`setLimits`), for the moment a
  test makes a step run out of time.
- **Faults are per kind.** `failNext(node, kind)` runs the step, lets its database writes commit, then throws the result
  away: the object closes 1011, sends no frame and writes nothing, which is what an error in the step does on workerd.
  `loseState(node, kind)` applies frames, replies and alarm and does not write the returned state; this cannot happen on
  workerd (the output gate holds the frames until the state write is durable: no state write, no frames), so it is kept
  only as the harsher case that shows the core recovers from a state that is behind the database. Both consume themselves
  on the next step of that kind. A hook (`setBefore`) runs inside a step's (and the accept's) Go call, so a test can make a
  step outlive the budget or hold a block while frames queue behind it.
- **Forget** deletes the stored keys and then re-arms from what is left: the handshake deadline of a socket that has not
  authenticated yet survives it (a test pins this). The own-node guard has its own test (a step calling `Ask`, `Close` or
  `poke` for its own node, also with `context.WithoutCancel` of the step context, is reported and refused).
- **The step log** is `[]{Gen, Kind, Msg, Err}` (`Msg` is the oneof field of the frame or request); the emulator also keeps
  the frames it sent per generation, the closes it asked for, the close events it heard, the count of dropped messages, and
  a snapshot of the object's storage.
- **Agent set-up (5b; 5c replaced it with link enrolment and a link-only agent).** The agent is enrolled over mTLS (the Enroll route of a TLS server with the agent SNI), its
  `AgentService` stream is not served (the edge has none), and it starts with `linkAdvertised` set and a 100 ms hold: the
  HelloAck that advertises the link comes from an mTLS session on the VPS. Keys of the raw clients are the agent's own.
- **Beyond the list above**, the file also covers: a close from the panel (`Remote.Close`, the re-enrolment path), sockets
  that never authenticate (silent, wrong signature, text frame: nothing stored, no Go step), a session that never says Hello
  (a poke keeps its deadline; the alarm closes it), and a step that outlives the block budget (1011, waiters rejected, the
  next connection works).

### 6.4 Rounds

| Round | Changes | Proof | Risks |
|---|---|---|---|
| 5a | `link_driver.go` (types, `Link`, close mapping); `LinkHandler` answers with the marker when `Remote != nil` | driver tests on SQLite: open keeps only Poison; Hello and desired in one call, HelloAck first; desired before Hello does nothing; refusal and Retire replied in the same call; CommandResult reply bytes; expiry gives a nil reply; close codes; a 300-byte reason is clipped; closed with RetireAt gives Forget; closing an old generation keeps the new row; `Preparing` is never stored; a bad frame closes 1008; a bad state is an error; JSON round trip | the Hello-then-desired order lives in two adapters; effect handling can drift between them |
| 5b (implemented) | emulator and scenarios 1-6 with today's agent | scenarios pass under `-race`; existing VPS link tests unchanged | the emulator encodes our reading of Durable Object semantics; step 6 checks it against workerd |
| 5c (implemented) | proto renew fields; core renew case; enrol route; `Config.LinkURL` and the link install command; agent `enroll --link-url`, `Meta.Link`, link-only session, backoff, renew, capabilities; scenarios 1 and 7 on link enrolment | the enrol path reaches the handler, everything else under the prefix gets the decoy; flags cannot be combined; a pin mismatch is refused; a link-only agent never dials mTLS and backs off on a dead panel; renew with a good CSR, a bad CSR, a store error; VPS install command unchanged; mTLS enrol without SNI gets 404 | a public enrol route on the VPS (§6.5 Q1); renew against an old panel only times out |

Every round passes the §3 gate. The only store statements on a new path are RenewCert's guarded batch; bridge query
budgets are untouched.

### 6.5 Decisions on the open questions

1. The VPS also serves the link enrol route (same code and protection: secret prefix, one-time token, limiter); an edge
   fleet moving to a VPS needs it. The VPS UI keeps the mTLS command. — owner: yes (2026-10-09)
2. An offline retire leaves under 300 B in NodeLink storage: accepted (lead).
3. `Link` returns an error; the §3 row is amended (lead).
4. Edge close codes 4000/1011/1008 (lead).
5. Renew keeps the old serial until the grace runs out (one reconnect about 3 hours after each renewal; the grace was 10
   minutes until the review of 5c, which showed a lost answer would lock the agent out) (lead).

### 6.6 Carried to step 6

- TypeScript: the open event gains `generation` (`nodelink.ts` sends none today; the driver refuses 0); `LinkStepOut`
  gains `forget`; the `"step"` operation in `link_js.go` still returns `errLinkStep` and must convert `LinkIn`/`LinkOut`.
  Its ctx is not a fixed 15 s: the emulator (like `call()` in `nodelink.ts`) gives a Go call `min(20 s, block deadline -
  now)`, and a fixed 15 s is right only for the first call of a block (a slow accept leaves the closed step less than that
  while Go would still have 15 s, so a D1 write could land after the object gave up with 1011). The `"step"` operation
  must pass the block deadline (`until`, as an absolute time) and the adapter uses `ctx = min(15 s, deadline - margin)`.
- `forget`: after `deleteAll()` the object must `arm()` again, not `deleteAlarm()`: a socket that has not authenticated
  keeps its handshake deadline. A fan-out through `waitUntil` must use a fresh context: `context.WithoutCancel` keeps the
  node tag of the step context and trips the own-node guard (or, on workerd, deadlocks).
- `LinkOut.AlarmAt` is the zero time when no alarm is wanted: the `"step"` conversion must turn it into `undefined`
  (`alarmAt` absent), not `UnixMilli()` of the zero time, or the object would arm an alarm in the year 1.
- Edge Remote adapter: `Ask` → `NodeLink.ask(proto bytes, deadline ms)`, "timeout" → `context.DeadlineExceeded`;
  `Close` → `close(4000, reason)`.
- StateChanged reaches no node on the edge: `recomputeAll` walks in-memory sessions only. Step 6 fans out
  `NodeLink.poke` to connected nodes through `waitUntil`, never awaited inside a step; the same for
  `dispatchWarpAttention` and OnUsage → Recompute → StateChanged.
- Abandoned steps can still write: after the object gives up at 20 s the Go call may still land D1 writes (a late
  NodeHello could restore a superseded `node_live` row until the next Hello). The 15 s ctx means this needs a single D1
  call over 15 s; measure it.
- **Verify on workerd** (the emulator chose these; step 6 checks each against the real runtime and fixes the emulator or
  `nodelink.ts`):
  1. `readyState` right after our own `close()`: closing at once, so `openSockets()` and `webSocketMessage` already treat
     the socket as not open (the emulator does).
  2. Frames that arrived before a peer's close frame: the emulator still steps them. If workerd marks the socket closed
     before the queued message events are delivered, `nodelink.ts:155` drops them, and an earlier CommandResult or
     DoctorReport (an admin reply) becomes a timeout, and the last stats batch is lost (resent after the reconnect).
  3. Whether `webSocketClose` is delivered after our own `close()` (the emulator delivers it when the peer answers) and
     with which code.
  4. How an `ask` RPC fails when the object is reset (the emulator rejects with "link lost"; the adapter must map it to
     the same error, not to a timeout).
  5. Whether `close()` throws for 1004 and the other reserved codes (the emulator throws for 1005, 1006, 1015 and for a
     reason over 123 bytes, and leaves the socket open); `nodelink.ts` maps only 1005, 1006, 1015 to 1000.
  6. That an alarm is only called when armed and due (`runAlarm` enforces it here), and that an alarm set while its own
     handler runs is kept.
- ADR 0007: the driver is a second place that creates EventDesiredChanged and runs every preparation inline.
- SSH provisioning builds the mTLS enrol command; the edge needs the link form (phase 3).

## 7. Step 6 design: wiring the edge edition

Status (2026-10-09): designed against main 34f7e7f. Rounds 6a-6f are local only; 6g needs the owner's explicit OK and a
throwaway Cloudflare account. Every round leaves the VPS edition unchanged and passes the §3 gate. Lead decisions on the
open questions are in §7.10.

### 7.0 What the code said before round 6a (gaps and contradictions)

1. The `"step"` op lives in `cmd/mistgate-edge/link_js.go` (not fleet) and still returns `errLinkStep`;
   `cmd/mistgate-edge/testdata/bridge.cjs` pins that.
2. The edge has no link prefix: `newEdgeInstance` / `ensureEdgeSecrets` (`instance_js.go`) fill only `agent_sni` and
   `sub_prefix`, so `app.Build` mounts no `LinkHandler`; the marker is reachable only through the test hook
   (`hooks_test_js.go`); `initPanel` (`main_js.go`) passes no `Remote`.
3. `StateChanged` does nothing on the edge: `Fleet.Run` never starts, the one-slot `kick` channel fills and nobody reads
   it. Callers: the subscription path on a first fetch that creates credentials (`access/sub.go`); quota inside a stats
   step (`session_core.go` → `agent.go` → `access.go`); the AWG kernel switch inside an event step (`awgprep.go`).
4. `dispatchWarpAttention` (`l3.go`) starts a goroutine with a 3-minute Background ctx; nothing keeps the invocation
   alive after the step returns (longer than the 30 s `waitUntil` grace). On js the WARP transport refuses anyway
   (`warp/tls_js.go`), but it can still start a D1 read after its invocation ended.
5. A stats step makes 3 D1 calls, not 2: `EffectUsage` → `OnUsage` → `acc.Recompute` reads `UserStates` inline for every
   batch with traffic (plus 1 write per changed status), on top of the 30 s certificate recheck and the L3 health writes.
6. `PanelLink.call` initialises the panel with the origin `"https://panel.invalid"` (`panellink.ts`): harmless for links,
   but a cron on a fresh database would store it as `public_url`.
7. `ResetUserPeriod` is unguarded (`store/access_user.go`: `… used_bytes = 0 … WHERE id = ?`): a sweep that read the old
   period can zero usage a stats batch committed after another reset. The race exists on the VPS too; the edge makes it
   likely.
8. `nodelink.ts` is behind the driver: open carries no `generation`; `forget` is ignored; `webSocketClose` echoes the
   close although both compatibility dates are after 2026-04-07 (`web_socket_auto_reply_to_close` default: the runtime
   replies itself and `readyState` is already CLOSED when the close event fires — §6.6 item 2 is a real risk for the
   `readyState` gate).
9. Docs: ADR 0003 gives the object "its own SQLite for seq and desired state" (they live in D1: `last_seq`, `node_sent`);
   ADR 0007 has the wasm "in the DO's isolate" and the DO "clearing `Preparing` on rehydration" (the driver never stores
   it); `design/adr/README.md` does not list 0007.
10. Other per-isolate in-memory paths never drained on the edge: the `health.evaluateSoon` kick; Telegram `enqueue`
    (bounded, never sent); the evaluator grace (`startedAt` = the isolate's start). `Fleet.Run`, `closeAll`,
    `recomputeAll` and `snapshotSessions` stay VPS-only.

### 7.1 The `"step"` op (TypeScript ↔ Go)

```ts
// panellink.ts: additions only
| { kind: "open"; at: number; generation: number; certSerial: string; certNotAfterUnix: number }
interface LinkStepIn  { nodeId: string; state: string | null; until: number /* block's Go budget end, ms */; event: LinkEvent }
interface LinkStepOut { …; alarmAt?: number; replies?: …; forget?: true }
```

Input (`link_js.go` `stepFromJS`): `nodeId` non-empty → `NodeID`; `state` string or null → `""`; `at`, `until`,
`deadlineAt` finite positive integer ms → `time.UnixMilli`; `generation` (open) integer 1..2^53−1; `certNotAfterUnix` →
`time.Unix`; `frame` (frame, request) required Uint8Array; `requestId` (request) non-empty; `owned` (closed) ignored.
Anything else rejects with `errLinkArgs` (the object closes 1011 and writes nothing).

The ctx: deadline = `min(now + 15 s, until − 2 s)`; if that is already ≤ now the call rejects "link step budget spent"
before any D1 work. `until` is the block's end, so a closed step at the tail of a slow accept gets what is left. The 2 s
margin covers the RPC return and the object's state write. A D1 batch already sent when the ctx expires can still
commit; the object writes nothing and the next session ends in a full resend (§2 "Delta base"); 6g measures how often.
The ctx carries the node id (`linkStepNode{}`) for the tripwire in §7.2. `accept` uses the same helper, with `until`
added to its args, instead of the fixed `linkAcceptTimeout`.

Output: always `state` and `frames` (`Uint8Array[]`); `close` only when set; `alarmAt` only when `!AlarmAt.IsZero()`
(never the zero time); `replies` only when non-empty (`frame: null` for nil); `forget: true` only when set; plus
`waitUntil: afterResponse.WaitUntil()`.

`PanelLink.call`: hands `waitUntil` to `this.ctx.waitUntil` and removes it from the result (a promise cannot cross the
RPC) — a pure `detachWaitUntil(out, ctx)` in `shell.ts`, used by every op. Its init origin becomes `""` (item 6): a fresh
database then fails init instead of storing a placeholder.

`nodelink.ts`: `authenticate` sends `generation` (the value it just stored in `gen`); `run` sends `until: this.until` and
returns the out (undefined on failure); `dropLive`: after the closed step, `if (out?.forget) await
this.ctx.storage.deleteAll()` — every caller of `dropLive` already calls `arm()` afterwards, which re-arms from what is
left (handshake deadlines live in socket attachments, which `deleteAll` does not touch). `gen` restarts at 1 for a retired
node (accepted: its `node_live` row is gone and Hello replaces it).

### 7.2 The edge Remote adapter

Go reaches NodeLink like it reaches Limiter: closures over `env` passed at init (the `limit` → `env.LIMITER` precedent),
not `ctx.exports` (Go never sees a ctx), not a service binding. A stub is created per call inside whatever invocation is
running (fetch, PanelLink, scheduled).

```ts
// panel.ts init
nodeLink: {
  ask:   (nodeId, requestId, frame, deadlineAt) => askNode(env.NODELINK, nodeId, requestId, frame, deadlineAt),
  close: (nodeId, reason) => closeNode(env.NODELINK, nodeId, reason), // NodeLink.close(4000, reason)
  poke:  (nodeIds) => pokeNodes(env.NODELINK, nodeIds),                // Promise.allSettled → failure count
},
```

`askNode` (`shell.ts`) never rejects: it resolves `{reply: Uint8Array | null}` or `{error: "timeout" | "lost"}`
(`"timeout"` is exactly NodeLink's own message; every other rejection — "link lost", an object reset, a network error —
is `"lost"`). The text is matched in this one TypeScript function next to the class that throws it; Go never matches
error text.

Go: `cmd/mistgate-edge/remote_js.go`, `edgeRemote{ask, close, poke js.Value}` implements `fleet.Remote` (with `Poke`,
§7.3) and awaits through `d1driver.Await`. `parseInitOptions` accepts `nodeLink` (an object with three functions;
anything else is an init error). Without `nodeLink`, `Remote` stays nil and `in.LinkPrefix` is cleared before
`app.Build`: the link is served only with its object behind it.

| JS outcome | `Ask` returns | `Fleet.ask` reports |
|---|---|---|
| `{reply: bytes}` | the ConnectRequest (an error if it does not decode) | the reply ("link lost" on bad bytes) |
| `{reply: null}` | `nil, nil` | no answer |
| `{error: "timeout"}` | `context.DeadlineExceeded` | no answer |
| `{error: "lost"}` | `errRemoteLost` | link lost |
| caller's ctx ends first | `ctx.Err()` | `ctx.Err()` |
| ctx tagged with the same node | `errOwnNode`, logged at Error, JS never called | link lost |

`Close` clips the reason to 123 bytes and has the same tripwire; `Poke` too (never called with a step ctx, §7.3). The
tripwire is the production twin of the emulator's own-node guard: a programming error fails at once instead of holding a
25 s block and ending in 1011.

### 7.3 Fan-out

New seams: `fleet.Remote` gains `Poke(ctx, nodeIDs []string) error`; `fleet.Config` gains `AfterResponse func(func())`
(`app.Build` passes `c.AfterResponse`; nil = a goroutine, as in access). Implementers: `edgeRemote`, `testRemote`
(`remote_test.go`), `nodeLinkEmu` (its `poke` becomes `Poke`).

`StateChanged`: with `Remote == nil` unchanged (the kick channel). With `Remote` set it calls `pokeConnected()`, which
makes no D1 call on the caller's path (subscription budgets unchanged): under `pokeMu`, if a fan-out is already running
set `pokeDirty` and return, else mark it running and hand `work` to `AfterResponse`. `work` loops while dirty: clear
dirty, fresh `context.WithTimeout(context.Background(), 20 s)`, `f.Live(ctx)` (1 D1 read), `Remote.Poke(ctx, ids of
Connected rows)` (one JS call, pokes in parallel). The ctx must be fresh, never derived from a step
(`context.WithoutCancel` keeps the node tag and trips the guard).

Which nodes: only Connected rows. The change commits before `StateChanged` and the Live read comes after it; a node
missing from that read has not committed its NodeHello batch yet and reads desired state after it in the same call, so it
sees the change. A poked object with no session, or before Hello, does nothing.

`waitUntil` comes from fetch (already), PanelLink (§7.1) and scheduled (§7.4), each within the 30 s grace. A step's own
`StateChanged` (quota, AWG switch) pokes its own node from that waitUntil; the poke RPC queues behind the object's block
and is never awaited inside the step.

Coalescing: per isolate (running + dirty: a burst costs at most two fan-outs); per object (the `poke` flag merges pokes
waiting behind a block into one desired step). Duplicates across isolates are possible; a desired step with nothing new
sends nothing.

Cost per change with N connected nodes: 1 D1 read; N poke RPCs (each 1 DO request + 2 storage writes); N alarms, each one
desired step (one PanelLink call, 3 + k D1 reads, plus one write batch when it sends). Ceilings (`ponytail:` comments):
10,000 subrequests per invocation (~9,000 nodes per fan-out); `Live` also reads the `users` column (switch to a
`ConnectedNodeIDs` query if that ever shows up).

Lost fan-out (isolate death, the 30 s cap, a D1 error): connected nodes keep the old state until the next change or
reconnect. Safety net: the cron calls `StateChanged()` every 10 minutes (§7.4), so revocation latency is at most 10 min.

WARP attention runs through `AfterResponse` with a fresh ctx: 3 min on the VPS (unchanged), 25 s on the edge. Usage-driven
recompute stays inline (parity): a stats step is 3 D1 calls (amend §6.1); the DO duration it adds is about 38 ms × 128 MB
per frame, ~1,300 GB-s per node per month against 400,000 GB-s included.

### 7.4 `scheduled()`

Wiring: `wrangler.example.toml` gets `[triggers] crons = ["* * * * *"]` (one trigger; Go decides by time which jobs are
due). `index.ts` exports `scheduled: scheduledPanel` (in `fetch.ts`): `getPanel(env, "")` (on a fresh database without
`PUBLIC_URL` init fails, one line is logged, the tick is skipped); `out = await panel.cron({at:
controller.scheduledTime})`; `ctx.waitUntil(out.waitUntil)`; errors logged, never thrown. `mgPanel.cron` (`main_js.go`)
calls `built.EdgeTick(ctx, time.UnixMilli(at))` with a 50 s ctx; `internal/panel/app/edge_tick.go` has no build tag
(tested on SQLite).

| Job | VPS cadence | Edge | D1 per run | Idempotent because |
|---|---|---|---|---|
| user status `acc.Sweep` | 1 min | every tick | 1 read + 1 write per change | status is a function of stored fields; the period reset becomes `… WHERE id = ? AND period_start = ?old` (§7.0 item 7) |
| node-down `fl.Sweep` | 30 s | every tick | 2 reads + 1 per silent active node + 1 insert per new outage | it reads the last `node_down`/`node_recovered` event |
| health retention `hl.Retention` | 1 h | minute 17 | rollup + 2 prunes | by age |
| desired-state safety net `fl.StateChanged()` | kick only | minute % 10 == 0 | §7.3 | no-op when nothing changed |

Each job gets its own 20 s ctx; one failure does not stop the others. An overlapping tick at worst duplicates a
`node_down` event (cosmetic). Budget on Paid (interval under 1 h): 30 s CPU and 15 min wall per invocation; a warm tick
should take a few ms CPU and 3 D1 round trips, a cold one ~254 ms CPU more.

Parity test (`app`): every name in `Panel.BackgroundJobs` is either run by `EdgeTick` or listed in `edgeDeferred` with
its phase, so a new VPS background job fails CI until it has an edge home. Deferred: health checker and evaluator
(NODE_DOWN alerts; phase 2, the evaluator needs a stored start time instead of the per-isolate grace); periodic UDP port
rechecks (on-demand checks already work through `Remote`); `updates` (owner decision 2), `backups` (R2), `telegram`,
`provision` (phase 3); `mcp-plan-sweep` (MCP is not served on the edge); log streaming (owner decision 2).

### 7.5 workerd verification (`edge/worker/test/do/workerd.test.ts`, fake PanelLink)

| # | Test | Emulator assumed | A different result means |
|---|---|---|---|
| 1 | own `close(4000)`, then `readyState` in the same turn | not OPEN at once | OPEN: `shut()` sets `att.shut` and `openSockets`/`webSocketMessage` check it; Go unchanged |
| 2 | client sends `a`, `b` (first step slowed 100 ms), then `close(1000)` | `frame:a`, `frame:b`, `closed` | `b` dropped: replace the `readyState` gate in `nodelink.ts` with `att.shut` (set only by our own close); if workerd never delivers `b`, document that a reply sent just before the agent's own close is lost |
| 3 | after `stub.close(4000)`, spy on `webSocketClose` | delivered when the peer answers, with its code | not delivered: no change; delivered with CLOSED: delete the echo in `webSocketClose` |
| 4 | a pending `ask`, then reset the object | rejects "link lost" | other message: `askNode` maps it to "lost"; hangs to the deadline: Go reports "no answer", document it |
| 5 | `close()` with 1000, 1008, 1011, 3000, 4000, 4999, 999, 1004, 1005, 1006, 1015, 5000 and a 124-byte reason | throws for 1005, 1006, 1015 and over 123 bytes | others throw: widen the map in `webSocketClose`; 1008/1011 throw: Go `closeWith` maps to 4008/4011 |
| 6 | (a) `alarmAt = now + 1.2 s`: no earlier alarm step; (b) an alarm step returning a new `alarmAt` keeps it | armed and due only; set in its own handler is kept | (b) lost: re-arm from the next event; the emulator changes the same way |
| 7 | closed step returns `forget` while another socket is mid-handshake | no keys left; the alarm is that handshake deadline | `deleteAll` keeps the old alarm: `arm()` overwrites it, no change |
| 8 | the fake PanelLink's step calls `this.ctx.waitUntil(stub(ownNode).poke())` | the poke runs after the block; one desired step; no 1011 | deadlock or 1011: the fan-out must leave PanelLink |
| 9 | a module-scope counter bumped by the object and read by PanelLink | — | local answer only; 6g gives the production one |

Item 10 runs with the real wasm in wrangler dev (6f): 20 parallel subscription fetches, link steps and a cron tick in
one isolate; pass = no hung invocation and no "Cannot perform I/O on behalf of a different request" (Go's scheduler is
global to the isolate). If it fails: every entry point also waits on an "isolate has no Go call in flight" promise (a
small extension of `edgeTaskRunner`).

### 7.6 Local end-to-end run with a real agent

`scripts/e2e-wsl.sh --edge`: the same scenario against both builds (ADR 0006), in the run's own network namespace.

Build: `GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" ./cmd/mistgate-edge` into `$WORK/worker/dist` plus
`wasm_exec.js` from the same GOROOT; copy `src/`, `package.json`, `pnpm-lock.yaml`, `tsconfig.json` to `$WORK/worker` and
`pnpm install --frozen-lockfile` there (never into the repo's Windows `node_modules`). `web/dist` from the repo.

Config: `wrangler.toml` from the template with the zero D1 id and `assets.directory = $REPO/web/dist`; `.dev.vars` with
`MASTER_KEY`, `PUBLIC_URL=https://de1.example.com:$PUB`, random `ADMIN_PREFIX` and `SUB_PREFIX` (all into `SECRETS` for
the log check). TLS: an openssl CA and a leaf for de1.example.com; `wrangler dev --ip 127.0.0.1 --port $PUB
--local-protocol https --https-key-path … --https-cert-path … --persist-to $WORK/wrangler-state`; `/etc/netns/$NS/hosts`
maps de1.example.com; the agent gets `SSL_CERT_FILE`, curl `--cacert`. The agent requires `challenge.audience ==
base.Host`: if wrangler rewrites the host, the run fails at "node online" — check that first.

No Cloudflare account is touched: `HOME=$WORK/home` (no stored login), `CLOUDFLARE_API_TOKEN`/`CLOUDFLARE_ACCOUNT_ID`
unset, `WRANGLER_SEND_METRICS=false`, never `--remote`, D1 id all zeros; outbound traffic only the npm install and
miniflare's public `cf.json`.

Steps (VPS numbering, differences only): (1) setup link from the Worker log, first admin; (2) `installCommand` contains
`enroll --link-url wss://de1.example.com:$PUB/<prefix>/`; `mistgate-node enroll --link-url …` then `run`; node online in
`node_live`, inbound running, desired state over the link; 2b doctor through `Remote.Ask`; 2c replaced: stop the agent →
offline after liveness → start → online with Hello matching the digest and no full DesiredState; touch `src/index.ts` →
wrangler reloads → the agent reconnects as gen + 1; 2d skipped (decision 2, no `update/1`); (3-8) used bytes grow; a
quota overrun on alice → fan-out → her credential removed within one stats interval; disabling a user → fan-out → the
client refused; an expiry 70 s ahead then `curl …/cdn-cgi/handler/scheduled?cron=*+*+*+*+*` → refused (the cron path);
(9) measurement only (§7.7); (10) retire: `AgentNotified`, the agent wipes and exits, the object's SQLite under
`--persist-to` has no `_cf_KV` rows, the `node_live` row is gone; (11) the secrets check also covers `wrangler.log`. The
M3 tunnel steps are not run with `--edge` in step 6.

Scripts: `test-edge-store.ps1` adds `./cmd/mistgate-edge/` (Go js tests of the `step`, `cron` and remote ops on the fake
D1); `test-edge-entry.ps1` (`bridge.cjs`) keeps the budgets, adds fan-out and cron checks, and awaits
`response.waitUntil` after each count so background queries cannot leak into the next; `pnpm test` gains
`workerd.test.ts`.

### 7.7 Measurements

Local (6f): D1 calls per step kind asserted with the fake D1 counters (open 0, Hello 4 + k on a first connect and up to 6 + k on a reconnect after a gap, measured in 6a, stats 2 (+1 with usage),
alarm 0, request 0, closed 1, +1 CertStatus when due; cron tick 3); CPU per step kind p50/p95 of 50; wasm cold start
(instantiate and init CPU, init D1 query count; more than 10 sequential init reads → batch the settings reads); batch
shapes (statement count and bound-value bytes for the largest IngestStats with 65,536 users and 2,000 sessions,
NodeHello, NodeApplied, RenewCert: counts unchanged 8 / 1 / 3 / 4, largest value under 2 MB); wasm memory after the e2e
mix and the 65,536-user batch, against 128 MB.

REAL CLOUDFLARE (6g) — a separate round, only with the owner's explicit OK: a throwaway account, or at least a throwaway
Worker and D1 named `mistgate-measure-<date>` on workers.dev only (no zone, no custom domain); never the production
account's Workers, D1, zones or secrets; a token scoped to that account, revoked afterwards, everything deleted; the
results go into this document. Measures: D1 batch latency p50/p95 and how often one call exceeds 2 s or 15 s (the
abandoned-write window); D1 limits for the largest IngestStats batch (65,536 users, the 1.5 MB valve, rows read and
written); DO billing per stats frame with 1-3 agents for an hour; whether `ctx.exports.PanelLink` runs in the object's
isolate and is billed as a request, and a cold step inside a block; isolate memory under a request mix; cron `cpuTime`
warm and cold; that a fan-out started through PanelLink's `waitUntil` completes; sockets across a redeploy.

### 7.8 Rounds

| Round | Changes | Proof | Risks |
|---|---|---|---|
| 6a (local) | the `step` op and the `accept` ctx helper; `remote_js.go` (Ask, Close, tripwire); the `nodeLink` init option; `link_prefix` in `newEdgeInstance`/`ensureEdgeSecrets`; the link mounted only with `Remote`; the marker test hook deleted | Go js tests: conversion both ways; generation 0 and above 2^53 refused; zero alarm leaves `alarmAt` out; `forget`; a past `until` refused before any D1 call; the §7.2 table with `js.FuncOf` fakes; the tripwire; D1 counts per kind. `bridge.cjs`: the marker under `<link_prefix>link/<id>`, the enrol POST reaches its handler, budgets unchanged | js-only files; the VPS is untouched |
| 6b (local) | `Remote.Poke`, `Config.AfterResponse`, `pokeConnected`, WARP through the runner; `Poke` in the emulator and `testRemote` | fleet: `StateChanged` never blocks, two calls during a run cause exactly one more, only connected ids are poked, the ctx is fresh; `edge_link_test` scenario 8 (a stats batch crosses alice's quota → node-a poked after its own step → credential gone, guard silent); scenario 2's manual pokes become admin changes; `bridge.cjs`: a credential-creating fetch has 0 extra queries before the response, then 1 query and one poke in its waitUntil | duplicate pokes across isolates (cost only) |
| 6c (local) | `nodelink.ts` (generation, `until`, forget = deleteAll + arm); `panellink.ts` types and `detachWaitUntil`; `panel.ts` `nodeLink`; `shell.ts` `askNode`, `closeNode`, `pokeNodes`; PanelLink origin `""` | vitest: open carries the stored gen; `until` is the block's end; forget leaves no keys and only the handshake alarm; an absent `alarmAt` leaves no alarm; `askNode` maps reply, null, timeout, link lost and reset; `pokeNodes` counts failures | DO tests use the fake PanelLink only |
| 6d (local) | `workerd.test.ts` (§7.5), the fixes it calls for, ports to `nodelink_emu_test.go` | the tests; the emulator and `nodelink.ts` agree item by item | a result that changes the Go driver (row 5) or §7.3 (row 8) |
| 6e (local) | `EdgeTick`, `edgeDeferred` and the parity test; exported `Fleet.Sweep` and `Health.Retention`; the guarded `ResetUserPeriod`; `mgPanel.cron`; `scheduledPanel`; `[triggers]` | SQLite: an expired alice → status change and one fan-out; a silent node-a → one `node_down`, none on the next tick; minute 17 prunes; minute 10 fans out; a stale reset writes nothing (also on the fake D1); a `scheduledPanel` test; `bridge.cjs` counts the cron op | the reset guard changes VPS behaviour, only in the race |
| 6f (local) | `e2e-wsl.sh --edge`; the measurement tests; results and the §6.1 counts into this document | §7.6 passes end to end; checklist item 10; the §7.7 local table | wrangler rewriting the host; the Linux workerd install |
| 6g (REAL CLOUDFLARE, owner OK) | nothing merged except the results | the §7.7 real table | cost (expected under $1); production resources excluded by the setup |

Every round passes the §3 gate and the race tests (fleet, agent, store); bridge budgets stay at Happ 5, Mihomo 7, page
5/3/4, AWG configs 7; D1 batch shapes unchanged; each round gets a manager review.

### 7.9 ADR amendments

- ADR 0007 (amended for step 6): the session state is one string in DO storage (attachments hold only handshake data and
  the generation); admin changes reach nodes as a `NodeLink.poke` fan-out to the nodes `node_live` shows connected,
  through the `waitUntil` of the invocation that made the change, never awaited inside a step, coalesced per isolate and
  per object, backed by a 10-minute cron safety net; the driver is a second place that creates EventDesiredChanged and runs
  every preparation inline, `Preparing` is never stored (replaces "clears Preparing on rehydration"); each Go call gets
  `ctx = min(15 s, until − 2 s)`; a step never awaits its own NodeLink and a production tripwire refuses it; usage runs
  inline (stats = 3 D1 calls); WARP attention through the runner with a 25 s bound; Retire `Forget` = deleteAll, then
  arm; the object never runs Go; the cost of a cold PanelLink step inside the block gets the 6f/6g numbers.
- ADR 0003: the DO holds the socket and an opaque state; seq and the sent digest live in D1 (`last_seq`, `node_sent`).
- New ADR 0008 "Edge background work: one Cron Trigger, idempotent jobs, waitUntil fan-out": the one-minute trigger,
  jobs chosen by `scheduledTime`, the parity test with `edgeDeferred`, the 30 s CPU / 15 min wall budget; phase-2 jobs
  plug into it.
- ADR 0006: `e2e-wsl.sh --edge` is the shared scenario; the workerd semantics tests are part of the edge CI job.
- `design/adr/README.md`: list 0007 and 0008; rule: dated amendment sections for refinements, a new record for a reversal.

### 7.10 Decisions on the open questions

1. The 10-minute safety-net fan-out: yes (lead).
2. NODE_DOWN alerts (the evaluator) on the edge come in phase 2 with the probes; step 6 keeps `node_down` events only. —
   owner: PENDING
3. Usage recompute inline, for parity (lead).
4. The `ResetUserPeriod` guard in both editions: yes (lead).
5. 6g timing and access (who creates the throwaway account, when; capped at one evening). — owner: PENDING
6. `--edge` inside `e2e-wsl.sh` rather than a new script: yes (lead).
7. WARP attention bound of 25 s on the edge, VPS kept at 3 min: yes (lead).

### 7.11 Round 6a implementation notes (2026-10-09)

- `step` now applies the §7.1 input and output contract, and `accept` also takes `until` for the shared 15 s / block-budget
  context. The TypeScript side passes the end of the call (`min(block end, call start + 20 s)`) as `until` to both `accept`
  and `step`; it refuses locally when 2 s or less remain.
- 6a wires `Remote.Ask` and `Remote.Close` only. `nodeLink.poke` is accepted but ignored; adding `Remote.Poke` remains 6b,
  as listed in §7.8.
- The Await helper is in `edge/d1driver/d1.go` (there is no separate `await.go` file).
- The bridge's existing `assertQueryBudget` allows 6 sequential queries for most labels and 7 for Mihomo/AWG. This differs
  from the 6a proof figures (Happ 5, Mihomo 7, pages 5/3/4, AWG configs 7); the thresholds were left unchanged pending
  lead clarification.
- **Known pre-existing race:** two isolates on an existing database can generate different `link_prefix` values;
  `SetSettings` is last-writer-wins, the same pattern as `sub_prefix` and `agent_sni`. This is harmless while no production
  edge database exists. Before edge goes live, use `INSERT ... ON CONFLICT DO NOTHING` and re-read the stored value.
