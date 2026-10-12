# Mistgate edge Worker

The thin TypeScript shell that runs the Mistgate panel on Cloudflare Workers: the Go panel compiled to `js/wasm`
(`cmd/mistgate-edge`) does everything, this Worker only starts it once per isolate and converts `Request` and `Response`
to the panel's fetch contract. Design: `design/cloudflare-edition/README.md`. To deploy without Wrangler, use `mistgate edge deploy` (`docs/en/getting-started/install-edge.md`); the Wrangler templates below are for development and hand-made deployments.

- `src/index.ts`: every request goes to the panel (`mgPanel.fetch`); the Worker does not know the secret admin path.
  If the panel cannot start, the answer is 503 and the next request tries again.
- `src/panel.ts`: per isolate, instantiates `dist/panel.wasm` once and calls `mgPanel.init` once (concurrent first
  requests share it).
- `src/shell.ts`: the pure parts (request/response conversion, static-asset reader, init de-duplication, routing of limit calls); tested by `pnpm test`.
- `src/limiter.ts`, `src/limitmath.ts`: the `Limiter` Durable Object (see below) and its pure math.
- `src/nodelink.ts`, `src/panellink.ts`: the agent link shell (`NodeLink` Durable Object) and the entrypoint through which it reaches Go (see below).
- `src/provisioner.ts`, `src/provision-index.ts`, `wrangler.provision.example.toml`: the second script, `mistgate-edge-provision`, with the `Provisioner` Durable Object that installs nodes over SSH (see below).
- `build-bundle.mjs`, `test/bundle/`: the release build of both scripts (`mistgate release build mistgate-edge.tar.gz`, see Releases and signing in the docs): esbuild, pinned in `package.json`, bundles `src/index.ts` and `src/provision-index.ts` into `worker.js` and `provision.js`, with `panel.wasm` left as a separate CompiledWasm module. `scripts/test-edge-bundle.ps1` builds the tarball twice, loads it in workerd (Miniflare) and prints each script's `wrangler deploy --dry-run` upload size against Cloudflare's 64 MiB limit.

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

## SSH installs (`Provisioner` Durable Object, second script)

Installing a node over SSH needs a TCP socket that lives as long as the install, and a socket belongs to the I/O context it was
opened in, while the Go panel resumes on any JavaScript event of its isolate. So the installs run in a **second Worker script**,
`mistgate-edge-provision`, whose isolate serves one object: the same `panel.wasm`, in its `serve` role. Deploy it **before** the
main Worker (which binds to it), with the same `MASTER_KEY` secret and the same D1 database. It has no route and no workers.dev
name; its own `fetch` answers 404 without starting Go.

- Main Worker: behind the sign-in check the panel answers the ProvisioningService and the install page with 204 and
  `X-Mistgate-Forward: provision;v=<build>` (`provision.ForwardMarker`); `forwardProvision` (`shell.ts`) sends the request, with the body the panel
  already read and the build in `X-Mistgate-Build`, to the one `Provisioner` (`idFromName("provisioner")`). No binding, or an object that cannot be
  reached: 503 `provisioner_unavailable`. The cron wakes the object (`kick`) while an install is queued, running or being cancelled.
- `Provisioner`: `fetch` runs the same panel handler (sign-in and step-up are checked again; a request of another build gets 503 `panel_updating`),
  `kick()` sets the alarm to now unless one is set earlier, `alarm()` calls `mgPanel.provision()` (one install per pass, 14 minutes at most) and
  sets the alarm again: in a second when another install waits, in 30 s when a pass was already running, in 60 s after an error. The object stores
  nothing but its alarm; the install is in D1, and an evicted pass is requeued (three interruptions fail the install).
- `openSocket` (`shell.ts`) is the `sshConnect` option of the serve role: `connect()` with an opening limit, `read`, `write`, `close`; the
  platform's refusal text is `refused`.
- Local development runs both scripts: `pnpm dev` (`wrangler dev -c wrangler.dev.toml -c wrangler.provision.dev.toml`, written by `scripts/build-edge.ps1`).

## Init contract (`mgPanel.init`)

| option | value |
|---|---|
| `d1` | the `DB` D1 binding |
| `masterKey` | the `MASTER_KEY` secret, 64 hex characters |
| `assets` | `async (path) => Uint8Array \| null`: reads one file of `web/dist` through the `ASSETS` binding (exact 200 hit, else null); the panel caches what it reads |
| `publicURL` | `PUBLIC_URL`, or the origin of the first request (https). Used only while the database is empty |
| `limit` | `async (request) => {ok, retryAfterMs, remaining, first}`: one call to the `Limiter` object of the request's (name, key); a rejection makes the panel refuse the request |
| `provision` | `{role: "forward" \| "serve", kick?, sshConnect?}`: the main Worker is a `forward` role (with `kick` when `PROVISIONER` is bound); the Provisioner script is `serve` and needs both functions |
| `adminHost`, `adminPrefix`, `subPrefix` | optional `ADMIN_HOST`, `ADMIN_PREFIX`, `SUB_PREFIX`, first run only |
| `setupTokenSHA256` | optional `SETUP_TOKEN_SHA256` secret: 64 lowercase hex characters, the SHA-256 of a setup token the deployer chose (see below); anything else makes `init` fail before it writes |

On a fresh database the panel prints the one-time setup link (it holds the secret admin path) to the Worker log once;
a later isolate only says that a link was already issued.

The first-run settings (prefixes, agent SNI, RP ID and origins) are stored with "insert if absent" and read back, so isolates
that start on an empty database at the same time all build from the values of the first write.

## Unattended first deploy (`SETUP_TOKEN_SHA256`)

The random setup link above ends up in Cloudflare's log and a deploy tool cannot read it. With the secret set, the deployer
picks the token instead and the panel only ever sees its hash:

```sh
TOKEN=$(openssl rand -hex 32)        # keep it; it is the credential
printf %s "$TOKEN" | sha256sum | cut -d' ' -f1 | wrangler secret put SETUP_TOKEN_SHA256
```

- Also set `ADMIN_HOST` or `ADMIN_PREFIX` yourself: with this secret the log shows no address, so a random admin path would be unknown to you.
- While no admin exists, the first isolate that starts offers the hash as the setup token and the log says only
  `No admin yet. The setup token of SETUP_TOKEN_SHA256 is accepted until <time>`. The token is valid for 30 minutes from that first offer;
  later isolates keep the first expiry. An expired or used token is never revived: set a new `SETUP_TOKEN_SHA256` and deploy again.
- The setup link is the admin address plus `setup#` and the token: `https://<ADMIN_HOST>/setup#<TOKEN>` with an admin host, or
  `<PUBLIC_URL><ADMIN_PREFIX>setup#<TOKEN>` with a secret path (for example `https://panel.example.com/some-secret/setup#<TOKEN>`).
  The `#` part never reaches a server log. A deploy tool can also call `AuthService/BeginSetup` with the token as `setupToken`.
- After the first admin exists the secret does nothing; remove it.
- If `SETUP_TOKEN_SHA256` is not set nothing changes: a random token and a printed link.

## Scheduled background work

The Wrangler template schedules the Worker once a minute. Each tick sweeps user status and silent nodes; at UTC minute 17
it also runs health retention, and at minutes divisible by 10 it runs the desired-state safety net.

With `PUBLIC_URL` set, the first cron tick after deployment (within about 60 seconds) may initialize a fresh database and
print the one-time setup link in that cron invocation's log. Run `wrangler tail` before the first deploy to capture it;
the link is only re-issued after it expires. With `SETUP_TOKEN_SHA256` set (above) nothing is printed but the expiry.

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
