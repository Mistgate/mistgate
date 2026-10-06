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

`pnpm test` runs two vitest projects: the pure code under Node (`test/*.test.ts`) and the `Limiter` class in workerd with
real Durable Object storage (`test/do`, `@cloudflare/vitest-pool-workers`; its own `wrangler.test.toml`).

`dist/` is generated: `wasm_exec.js` must come from the Go toolchain that built `panel.wasm`, so it is copied by the
build script rather than committed. `wrangler.example.toml` is the deployment template (no account id, no secret).