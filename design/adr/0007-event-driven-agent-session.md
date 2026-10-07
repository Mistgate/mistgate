# 0007. The agent session is an event-driven core; on the edge a hibernating Durable Object drives it

Status: accepted, 2026-10-06.

## Context

ADR 0003 puts each node's link on the edge into a Durable Object (DO) with a hibernating WebSocket. Agents send a
`StatsBatch` every stats interval (10 s by default; it doubles as the heartbeat), so a node's DO is never idle for long.

Cloudflare bills DO duration at 128 MB per instance for as long as the object is in memory, but not while it is idle
and eligible for hibernation: "Durable Objects that are idle and eligible for hibernation are not billed for duration,
even before the runtime has hibernated them" (Durable Objects pricing, checked 2026-10-06). An object stays eligible
only if it uses the WebSocket hibernation API and has nothing pending between events: no timers, no unresolved
promises. In-memory state is lost on hibernation; per-socket attachments (up to 16 KiB) and DO storage survive.

The panel's session (`internal/panel/fleet`, `runSession`) is a goroutine per stream that blocks in `Receive` and holds
state in local variables. Acknowledgements need a timer only while one is owed; certificate checks run on ordinary
inbound events. In Go compiled to wasm, a blocked goroutine or a timer is a pending promise or timer in the isolate, so
a DO hosting that loop would never be eligible for hibernation. With four nodes that is about 1.3 million GB-s a month,
against 0.4 million included in Workers Paid.

## Decision

- The session logic becomes an event-driven core shared by both editions: given the session state, sidecar and one event
  (a frame from the agent, a "desired state changed" notice, or an alarm), it mutates the supplied state and returns
  frames, adapter effects, a close decision and the next alarm time. The core owns protocol state; effects never write
  back into it.
- Store calls run inline in the core, including on the edge where D1 is available. Effects are reserved for work that
  differs by edition: delivering results to waiters, Cloudflare calls, and the cross-module usage callback. Publishing
  the live view for admin reads is not an effect: the adapter does it (`publishView`) after every step. Desired-state
  reads use a single in-flight preparation with a dirty bit: the effect starts its own session preparation, while
  `stepDesired` (the one caller that creates the desired-state-changed event) runs the first reconcile round's read inline
  instead of through the effect.
  Each prepared result is stepped through the core, which applies it and records withheld inbounds inline; a dirty
  follow-up starts through the effect. Certificate checks and pure database writes run inline.
- VPS: the existing `Connect` and WebSocket handlers drive the core from a goroutine loop as now, using one timer reset
  to the transition's `NextAlarm` after every step. The core sets hello, liveness, first-bandwidth, outstanding-ack,
  preparation-retry and request-expiry deadlines. Certificate checks run on ordinary inbound events after their check
  interval; the acknowledgement cadence stays the same.
- Edge: a `NodeLink` DO per node (`idFromName(node_id)`) accepts the WebSocket with the hibernation API, does the
  signed handshake through the same Go code, keeps the small session state in the socket attachment or DO storage,
  calls the core per event, and turns "next alarm" into a DO alarm. Admin changes reach it as a call from the Worker.
- Session data that must outlive a connection stays where it is today (D1: `last_seq`, applied revision, certificates, and the `node_sent` digest).
  The DO-side SQLite stats buffer from the original plan is deferred: one D1 write set per batch is well within the
  included D1 writes for a small fleet; revisit if D1 latency or cost says so (phase 4).
- Session state carries the Hello-applied hash, preparation-in-flight and dirty bits, retry time, and pending full-resend
  bit. A delta base is valid only when the digest's revision and hash both match what the session last sent. A step whose
  writes committed but whose state was lost ends in a full resend. The adapter checks ownership before applying prepared
  data; a base mismatch or first drift also forces a full state before a later delta. On rehydration, the Durable Object
  adapter clears `Preparing` and requests preparation again, since eviction may interrupt an in-flight read. Poisoned
  stats-batch identity is session state too; the VPS adapter persists it per node across reconnects, and the DO will
  persist it in DO storage.

## Consequences

- The DO is billed for the milliseconds it spends handling events, not for the hours a node is connected.
- One core means the edge cannot drift from the VPS session rules; both editions run the same tests against it.
- The refactor touches the most delicate code in the panel (seq/Ack/dedupe, one owner per node, desired-state
  revisions). It lands in two steps: first the core with the VPS loop around it and no behaviour change, then the DO.
- Cold wakes re-instantiate the panel wasm in the DO's isolate when the isolate was evicted (~90 ms CPU measured in the
  spike); to be measured with a real agent before the edge edition is called ready.
