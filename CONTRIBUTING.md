# Contributing

Thank you for helping. Small, focused pull requests are easiest to review; for a larger change, open an issue first.

## Checks

Run what CI runs before you open a pull request (see [README.md](README.md#development) for the toolchain):

```sh
go vet ./... && go test ./...          # also passes on Windows and macOS: Linux-only code has stubs
gofmt -l .                             # must print nothing
cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm lint && pnpm test && pnpm build
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
