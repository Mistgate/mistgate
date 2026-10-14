# 0009. Edge: six scripts behind a JS front

Status: accepted, 2026-10-14. Supplements [0008](0008-ssh-installs-in-a-second-worker.md): the second script stays, the
split goes on from it.

## Context

The edge edition was one Worker (`panel.wasm`, 40.6 MB) and the Provisioner script of 0008 with the same wasm. Every
isolate of the first one carried the whole panel: the admin and its SPA cache, the subscription hot path, the agent-link
steps and the cron. A burst of subscription requests shared 128 MB with an `IngestStats` step (a 40 MB peak) and the admin;
anything heavy that was added to the panel grew the one wasm toward Cloudflare's 64 MiB per script. The measured floor of
a Go Worker that touches the domain packages is about 21 MB (the admin RPC layer and go-webauthn link into every one of
them), so the split cannot go below that without a refactor that is not part of this decision.

## Decision

One host, six scripts behind it. `<name>` is the `--name` of the installer (default `mistgate-edge`).

| Script | Owns | wasm |
|---|---|---|
| `<name>` (**FRONT**) | the path router; the Durable Objects `NodeLink` and `Limiter`; the custom domain | none |
| `<name>-public` | subscriptions in every format, the user page and its password, devices, logo, decoy | 24.7 MiB |
| `<name>-link` | the Go side of the agent link (`PanelLink`: challenge, accept, step); link-only enrolment | 26.4 MiB |
| `<name>-admin` | the SPA, the Connect API, sign-in, setup (the **only creator of the instance**), page preview, the SSH marker | 37.6 MiB |
| `<name>-jobs` | the cron `* * * * *`: `EdgeTick`, later Telegram delivery, backups, self-update | 25.1 MiB |
| `<name>-provision` | SSH installs, as in 0008, with its own build | 29.4 MiB |

- **FRONT keeps the old name, so nothing moves.** The Durable Object classes, their data and the custom domain stay on
  `<name>`; the agents' address does not change. The classes cannot be moved to another script without a lifecycle
  transfer that is in practice irreversible, and re-pointing a custom domain from one script to another has not been
  tried. The Go side of the link (`PanelLink`, a `WorkerEntrypoint`) is the one thing that leaves: FRONT binds it as a
  service with `entrypoint = "PanelLink"` and `NodeLink` calls `env.PANEL_LINK`.
- **FRONT is JavaScript only.** Every request goes through it, so it starts in milliseconds, and the Durable Object
  isolates carry no Go. It reads three settings from D1 (`link_prefix`, `admin_prefix`, `admin_host`), caches them per
  isolate for five minutes and applies the order of `httpserver.Public`: link prefix, then the admin host, then the admin
  prefix, then everything else to public. With no instance yet, everything goes to admin, which creates it. Each Go
  script mounts only its own surface, plus the decoy, and checks the path again in Go (`canonicalPath`, `ctEqual`), so a
  routing mistake answers the decoy and not another script's surface.
- **One D1 database, bound by all six** (`database_id` from the installer's key file). The scripts share the state that was
  already shared: `Limiter` (the security limits), `NodeLink` (live nodes, through `fleet` as a facade over D1 and the
  object), `Provisioner`. Caches keep their TTLs: subscription settings up to 60 s between admin and public.
- **Secrets per script.** `MASTER_KEY` on the five Go scripts; `ADMIN_PREFIX`, `SUB_PREFIX`, `SETUP_TOKEN_SHA256` and the
  var `PUBLIC_URL` on admin only. Honest limit: this does not reduce privileges. D1 has no per-table rights and all five
  Go scripts hold the master key; what is gained is that less code is reachable from the surface that clients see.
- **The installer deploys the order link, provision, public, admin, jobs (with its cron), FRONT last.** What a script
  calls is uploaded before it. FRONT is the one entry from outside, so the new routing switches on with one upload. The
  very first time `<name>` does not exist and link binds its Durable Objects, so FRONT goes up first without its service
  bindings and again at the end, the same way the main script and the Provisioner were handled in 0008.
- **Release and bundle.** One `mistgate-edge.tar.gz` and one signed manifest as before; `edge.json` is schema 2 (scripts
  with a role, modules, own and external objects, service bindings, assets, secrets, vars, cron, routes). Schema 1 is not
  read: no install of it exists, so there is no code or test for moving from the single Worker.
- **Skipping.** The installer keeps a fingerprint per script (modules, bindings with the secret values, migrations, assets)
  and does not upload a script that would send the same thing. A release without a FRONT change does not restart the
  `NodeLink` objects, so agents do not reconnect.
- **Rollback.** Before the first upload of a new bundle the installer records each script's active version and cron schedule
  (`GET .../deployments`). `--rollback` posts them back (`POST .../deployments?force=true`, force because a secret may have
  changed), FRONT first and in reverse order of the upload, and restores the schedules. D1 is not rolled back.

### Skew while a release is uploaded

The window is the time to upload six scripts (25 to 40 MB each, about a minute, an estimate). Rules that make it safe:

1. **D1 migrations inside a release only widen**: the first new Go isolate migrates D1 while the scripts not uploaded yet
   still run on the new schema, and `--rollback` leaves the schema. A narrowing change (drop, rename, rebuild of a table)
   goes in a later release. This is a rule for authors and reviewers; a CI check that refuses a migration with `DROP` or
   `RENAME` unless the file says why it is safe is planned and not built yet.
2. **The RPC of the Durable Objects and of `PanelLink` is compatible across one version**: `NodeLink.ask/close/poke`,
   `Limiter.limit`, `PanelLink.challenge/accept/step`. The callers are uploaded before FRONT, the owner of the objects.
   The meaning of an existing method is never changed: new behaviour gets a new method name and the old one goes a
   release later.
3. `<name>-provision` keeps its build check (`X-Mistgate-Build`, 503 `panel_updating`) from 0008.
4. FRONT and the Go scripts talk plain HTTP, a stable contract. A script that is new in a release is reachable only once
   FRONT is uploaded.

## Consequences

- The subscription hot path is a Go isolate of about 25 MiB instead of 40; a burst of subscriptions no longer shares memory
  with agent-link steps or the admin. admin is the same size as before, within 2.5 MB of its CI limit; heavier features
  (MCP, backups, integrations) are to go into scripts of their own, not into admin.
- Every script has a size limit in CI (`scripts/check-edge-sizes.sh`), raised only by a commit that names the feature and
  says why it cannot live in another script.
- The installer, the docs and the e2e script handle six scripts; the dev wrangler configs are generated from `edge.json`.
- Calls between scripts through service bindings are not billed as requests and run on the same thread, but CPU is billed
  for both. FRONT costs about 0.5 to 1 ms per request (an estimate).
- Not verifiable without a real account, checked in the first real deploy: an upload of a script with a binding to a
  script that does not exist (the order avoids it, the platform's answer is unknown), a WebSocket through a service binding
  to a Durable Object on production (works on local workerd), whether a Durable Object calling a service binding counts as
  a request, the shape of `GET .../deployments` and `POST .../deployments?force=true` with a script that has Durable
  Objects, clearing a cron with an empty `PUT .../schedules`, the cold start, CPU and memory of each role, the `scheduled`
  endpoint with several configs in `wrangler dev`.

## Alternatives rejected

- **Routes by path instead of a FRONT script**: the secret prefixes would sit in the zone's route list, visible to anyone
  who can read the zone; routes need a proxied DNS record per hostname, so the token would also need DNS edit; a switch is
  several edits, not one upload.
- **`<name>` becomes link and a new script is the router**: the custom domain would have to move to another script, with a
  pause and a new certificate; the platform's behaviour for a host that another script holds is unknown.
- **Moving the Durable Object classes to a new script**: a lifecycle transfer with a tombstone on the source, in practice
  irreversible.
- **A separate script for the static files**: the SPA lives under a secret prefix and Go sets its CSP and `<base>`; a
  request with `run_worker_first` is billed as a Worker request anyway, and the admin's traffic is small.
- **Fewer scripts (jobs inside admin)**: saves one wasm but mixes the cron's CPU and the future memory-heavy jobs with the
  admin.
- **Fewer parts of the panel in the domain packages (cutting the 21 MB floor)**: a wide refactor of the RPC layer and of
  go-webauthn; only worth it if admin reaches its limit.

ADRs 0003 and 0007 are unchanged: the agent link and the session core are the same, only the Go side of a step runs in
`<name>-link`.
