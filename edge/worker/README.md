# Mistgate edge Worker

The thin TypeScript shell that runs the Mistgate panel on Cloudflare Workers: the Go panel compiled to `js/wasm`
(`cmd/mistgate-edge`) does everything, this Worker only starts it once per isolate and converts `Request` and `Response`
to the panel's fetch contract. Design: `design/cloudflare-edition/README.md`.

- `src/index.ts`: every request goes to the panel (`mgPanel.fetch`); the Worker does not know the secret admin path.
  If the panel cannot start, the answer is 503 and the next request tries again.
- `src/panel.ts`: per isolate, instantiates `dist/panel.wasm` once and calls `mgPanel.init` once (concurrent first
  requests share it).
- `src/shell.ts`: the pure parts (request/response conversion, static-asset reader, init de-duplication, routing of limit calls); tested by `pnpm test`.
- `src/limiter.ts`, `src/limitmath.ts`: the `Limiter` Durable Object (see below) and its pure math.
- `src/nodelink.ts`, `src/panellink.ts`: the agent link shell (`NodeLink` Durable Object) and the entrypoint through which it reaches Go (see below).

## Security limits (`Limiter` Durable Object)

Worker isolates share no memory, so the panel's security limits (the login burst per client address, page-password tries,
enrollment failures, API-token rates) are counted in a Durable Object. There is one SQLite-backed object per
`(name, key)` pair, addressed with `idFromName(name + "\0" + key)`, and it implements the five operations of the `limit`
init option (`take`, `peek`, `record`, `fail`, `reset`) with the semantics of `securitylimit.Memory` for one key.

- The object's own `Date.now()` is the clock; the Go side's `now` is not sent. `maxKeys` is ignored: there is no map of
  keys to bound, one object holds one key.
- State is a bucket (`b`) and/or a window (`w`) in the object's storage. An alarm is set for the moment the bucket is full
  again / the window has ended (the state is then the same as none) and deletes it, so an idle object stores nothing.
- A malformed request throws; a rejected call, or a missing `limit` option, makes the panel refuse the guarded request
  (fail closed). The object is created by the `[[migrations]]` entry (`new_sqlite_classes`) of `wrangler.example.toml`.

## Agent link (`NodeLink` Durable Object, `PanelLink` entrypoint)

One `NodeLink` object per node (`idFromName(node id)`, SQLite-backed) holds the agent's WebSocket and the session state as an
opaque string; it never reads a frame. The Go panel runs every handshake step and every session step in the Worker's wasm:
the object calls it through `this.ctx.exports.PanelLink` (a loopback entrypoint, nothing to bind), which calls
`mgPanel.link(op, args)` (`cmd/mistgate-edge/link_js.go`: `challenge`, `accept`; `step` is not implemented yet).

- Routing: the panel answers an upgrade on the link path with 204 and `X-Mistgate-Link: <node id>` (`fleet.LinkMarker`); `index.ts`
  forwards the original request to that node's object (`forwardLink` in `shell.ts`); the object takes the node id from its own name (`ctx.id.name`), and the marker never reaches the client.
  The edge has no link prefix yet, so the marker is mounted only by the bridge test hooks.
- Ordering: every entry point (upgrade, message, close, alarm, RPC) runs inside `blockConcurrencyWhile`, one event at a time.
  A Go call gets 20 s and one block 25 s in all (the platform resets the object at 30 s); an error, a timeout or an empty budget closes the socket with 1011 and the agent reconnects.
- Ownership: each authenticated socket carries a generation. A new accept closes every other authenticated socket with 4000 (handshakes in progress stay), ends the old
  session (a `closed` step, `owned: false`) and starts the new one; frames of an older generation are dropped. A socket that
  has not sent a valid LinkAuth within 10 s is closed with 1008.
- Alarm: one alarm, the earliest of the session's own `alarmAt`, the handshake deadlines and "now" when a `poke()` is pending.
  The session's alarm is never set closer than 1 s from now (a Go side that always answers "now" must not loop; alarms
  are billed). Nothing pending means no alarm and no timer, so the object hibernates.
- RPC: `poke()` (desired state changed), `ask(requestId, frame, deadlineAt)` (a request frame and the reply a later step resolves;
  `deadlineAt` is the caller's own deadline in ms since the epoch, and a request that reaches the object after it is never
  sent; rejects with `link lost` when there is no session or it ends, and `timeout` at the deadline), `close(code, reason)`.
- Step contract (`LinkEvent`, `LinkStepIn`, `LinkStepOut` in `panellink.ts`): the state goes in and out as one string; the
  object writes it first, then sends the frames, delivers replies, applies the close and re-arms the alarm.

## Init contract (`mgPanel.init`)

| option | value |
|---|---|
| `d1` | the `DB` D1 binding |
| `masterKey` | the `MASTER_KEY` secret, 64 hex characters |
| `assets` | `async (path) => Uint8Array \| null`: reads one file of `web/dist` through the `ASSETS` binding (exact 200 hit, else null); the panel caches what it reads |
| `publicURL` | `PUBLIC_URL`, or the origin of the first request (https). Used only while the database is empty |
| `limit` | `async (request) => {ok, retryAfterMs, remaining, first}`: one call to the `Limiter` object of the request's (name, key); a rejection makes the panel refuse the request |
| `adminHost`, `adminPrefix`, `subPrefix` | optional `ADMIN_HOST`, `ADMIN_PREFIX`, `SUB_PREFIX`, first run only |

On a fresh database the panel prints the one-time setup link (it holds the secret admin path) to the Worker log once;
a later isolate only says that a link was already issued.

## Build and run locally

```powershell
scripts/build-edge.ps1          # web/dist, dist/panel.wasm, dist/wasm_exec.js, wrangler.dev.toml (all git-ignored)
cd edge/worker
pnpm install
# .dev.vars (git-ignored): MASTER_KEY=<64 hex characters>
pnpm dev                        # wrangler dev --local over https (the panel only serves https origins; the certificate is self-signed)
```

`pnpm test` runs two vitest projects: the pure code under Node (`test/*.test.ts`) and the `Limiter` and `NodeLink` classes in
workerd with real Durable Object storage (`test/do`, `@cloudflare/vitest-pool-workers`; its own `wrangler.test.toml`). There
`NodeLink` reaches a scripted fake `PanelLink` (`test/do/worker.ts`) through `ctx.exports`, like the real one.

`dist/` is generated: `wasm_exec.js` must come from the Go toolchain that built `panel.wasm`, so it is copied by the
build script rather than committed. `wrangler.example.toml` is the deployment template (no account id, no secret).