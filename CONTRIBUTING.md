# Contributing

Thank you for helping. Small, focused pull requests are easiest to review; for a larger change, open an issue first.

## Development

You need Go 1.27 (the `toolchain` line in go.mod; `GOTOOLCHAIN=auto` fetches it), Node.js 22+, pnpm 10, make and a
POSIX shell (Git Bash or WSL on Windows).

```text
cmd/mistgate/            panel: serve, setup, backup, auth, mcp (stdio proxy), release, version
cmd/mistgate-node/       node agent: enroll, install, run, cleanup-net, awg prepare-kernel, version
proto/mistgate/          admin API and panel <-> agent API (Connect-RPC)
gen/, web/src/gen/       generated from proto/ (never edit by hand)
internal/panel/          panel modules: store, vault, auth, fleet, access, subs, protocols, health, update, provision, backup, warp, mcp, httpserver ...
internal/node/           agent modules: agent, engine, hysteria2, awg, warp, hostctl, doctor, torrentguard, update ...
web/                     admin SPA (Vite, React, TypeScript, TanStack Router/Query, Tailwind) and the user page
docs/, site/             the documentation (en, ru) and the static site built from it
scripts/                 end-to-end tests
```

Linux-only code (nftables, netlink, AmneziaWG, systemd) sits behind `//go:build linux` with stubs, so `go build ./...`,
`go vet ./...` and `go test ./...` work on Windows and macOS too.

```sh
make build                            # bin/mistgate-linux-{amd64,arm64}, bin/mistgate-node-linux-{amd64,arm64}
make dev                              # the panel in dev mode: decoy :8080, admin :8081, agent endpoint :8082, data in ./.data
cd web && pnpm install && pnpm dev    # Vite on http://localhost:5173 with hot reload, proxied to the admin on :8081
make test                             # go vet, go test, then pnpm typecheck, lint and vitest
make gen                              # buf lint + buf generate after editing proto/ (remote plugins: needs internet)
```

- On first start the dev panel prints a one-time setup link (`http://localhost:8081/setup#...`). Reset everything by
  stopping it and deleting `./.data`.
- If 8081 is taken, run the panel with `--admin-listen 127.0.0.1:<port>` and Vite with
  `MISTGATE_PANEL=http://127.0.0.1:<port> pnpm dev`.
- To try a node against the dev panel, run both in WSL (or a Linux VM): `mistgate serve --dev`, add the node in the
  admin, then `mistgate-node enroll ... --state-dir /tmp/node` and `mistgate-node run --state-dir /tmp/node` as root.
  The agent never dials private addresses, so test traffic against a public site.
- A binary built without `RELEASE_KEY` cannot update itself or its nodes; see
  [Releases and signing](docs/en/operations/releases.md).

End-to-end tests:

- `scripts/e2e-wsl.sh [--keep] [--m3 | --awg | --warp | --mihomo | --m3-only | --old-node <path>]`: the panel, a node,
  a real Hysteria2 client and self-update, optionally AmneziaWG clients, a fake WARP peer and a real mihomo. Runs as
  root in WSL in its own network namespace (`wsl -d Ubuntu -u root -- bash scripts/e2e-wsl.sh` from the repo root);
  needs go, curl, jq, python3 (with yaml for the tunnel steps), openssl, nft, ip, and internet access. 3 to 10 minutes.
- `scripts/e2e-mcp.sh [-mode listener|prefix|both] [-keep]`: API tokens and MCP against a real panel on loopback, any
  OS, no internet, about a minute.

Every stable tag builds a draft release in GitHub Actions; a maintainer rebuilds it from the tag on a machine with the
offline release key and signs only binaries that rebuild byte for byte. The whole procedure:
[Releases and signing](docs/en/operations/releases.md).

## Checks

Run what CI runs before you open a pull request (the toolchain is listed under [Development](#development)):

```sh
go vet ./... && go test ./...          # also passes on Windows and macOS: Linux-only code has stubs
gofmt -l .                             # must print nothing
cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm lint && pnpm test && pnpm build
```

The store has two `database/sql` backends: SQLite for the VPS edition and D1 for `js/wasm`. New migrations
must avoid explicit `BEGIN`/`COMMIT`/`SAVEPOINT`, `-- +goose NO TRANSACTION`, and `PRAGMA foreign_keys`;
see [ADR 0002](design/adr/0002-database-sql-seam.md). Run the edge storage tests with
`pwsh -File scripts/test-edge-store.ps1`.

For a change to `docs/`, also build the documentation site; it fails on a broken internal link (see
[docs/README.md](docs/README.md) for the page conventions):

```sh
pnpm --dir site install --frozen-lockfile && pnpm --dir site build && pnpm --dir site test
```

`make test` runs the same Go and web checks. Tests that need root (`MG_ROOT_TESTS=1`) and the scripts in `scripts/`
change network state: run them in WSL or on a disposable VM, never on a production host.

## Protocol buffers

Edit `proto/**/*.proto` first, then run `make gen` (`go tool buf lint` and `go tool buf generate`; the plugins are
remote, so it needs internet) and commit the regenerated `gen/` and `web/src/gen/` with your change. Never edit
generated files by hand. Changes to the agent protocol are additive: old agents must keep working.

## Conventions

- Code comments and identifiers are in English.
- UI strings live only in `web/src/i18n`, in English and Russian; English is the source of truth and `ru` is
  type-checked against it, with the same `{placeholders}` in both.
- Stdlib first; add a dependency only when it replaces real work. SQLite through `modernc.org/sqlite` (no cgo),
  migrations are goose SQL files in `internal/panel/store/migrations` (never edit an applied migration; add a new one).
- Secrets never reach logs; errors to clients are Connect codes with short messages.
- Each package with real logic has its tests next to it; small fakes instead of mock frameworks.
- **The code never names a real deployment.** Examples and fixtures use `example.com` hosts, node names like `de1`,
  addresses from `203.0.113.0/24` (and the other documentation ranges) and `2001:db8::/32`; no real domains,
  hosters, node addresses or people.

## License

By contributing you agree that your contribution is licensed under the [GNU AGPL v3.0 only](LICENSE), like the rest
of the project.
