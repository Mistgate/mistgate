# Mistgate: AI agent guide

Mistgate is a Go VPN fleet panel and node agent. The panel and agent are static Go binaries; the admin UI is embedded from `web/`. Read [`CONTRIBUTING.md`](CONTRIBUTING.md) before changing code and use [`docs/en/index.md`](docs/en/index.md) or [`docs/ru/index.md`](docs/ru/index.md) as the documentation map.

## Install and operate

- Panel setup and manual node enrollment: [`docs/en/getting-started/install-panel.md`](docs/en/getting-started/install-panel.md) and [`docs/en/getting-started/add-node.md`](docs/en/getting-started/add-node.md).
- AI-assisted setup: [`docs/en/getting-started/ai-agents.md`](docs/en/getting-started/ai-agents.md) (Russian: [`docs/ru/getting-started/ai-agents.md`](docs/ru/getting-started/ai-agents.md)). For an MCP client, connect with an admin-profile token to `<admin URL>mcp`; use `node_install_plan`, show the exact SSH fingerprint, wait for the user's confirmation and the owner's panel approval, then call `node_install_apply` with the password and the exact confirmed fingerprint. The MCP plan does not contain the password.
- The owner starts SSH installation from **Nodes → Add node**. The four-step wizard stays in the panel's modal and supports root or an SSH login with non-interactive `sudo -n` on public Ubuntu 22.04+ and Debian 12+ hosts. It confirms the pinned host key, runs preflight, installs the currently trusted signed agent bundle, and waits for the agent. The Go page at `<admin URL>nodes/install` remains the advanced job and server-access manager for cancellation, recovery, and password rotation.
- Password changes use `node_server_password_rotate_plan` / `_apply` in MCP or the **Доступ к серверам / Server access** section of the Go installer page. Through MCP the apply takes only the confirm token: the panel generates the new password itself and never returns it (the owner can reveal it in node Settings after step-up); on the Go page the owner types it. The panel verifies the new SSH login before committing it. A retired node keeps its saved access until the owner forgets it, and the panel no longer rotates it.
- Check [`docs/en/reference/mcp.md`](docs/en/reference/mcp.md) for token profiles, approvals and the full tool contract. Never put passwords, tokens or private keys in plan arguments, logs, audit fields, issue text or repository files.

## Repository map

- `cmd/mistgate/`: panel CLI and wiring; `cmd/mistgate-node/`: node agent CLI.
- `internal/panel/`: storage, auth, fleet, SSH provisioning, MCP, updates and WARP.
- `internal/node/`: node runtime, protocol engines, doctor and self-update.
- `proto/mistgate/`: Connect API contracts. Edit these sources, then regenerate `gen/` and `web/src/gen/` with `go tool buf lint && go tool buf generate`.
- `web/`: React/TypeScript admin and user interfaces; `docs/{en,ru}/`: documentation source; `site/`: static documentation renderer for Cloudflare Pages.
- `internal/panel/store/migrations/`: forward-only SQLite migrations; never edit a migration that has shipped.

## Build and verify

```sh
go test ./...
go vet ./...
cd web && pnpm typecheck && pnpm lint && pnpm test
cd ../site && pnpm test
```

For changes to a bounded package, start with its package tests and then run the full checks above. `make build` builds the static Linux panel and node binaries. `make test` runs the Go and web checks. On Windows, use PowerShell for local Go tests; Go tests do not require WSL unless a test explicitly asks for root or Linux networking.

## Safety and quality rules

- Keep remote installation in Go. Do not add shell installers or Docker as runtime dependencies.
- Confirm the SSH host key before authentication. Reject private, loopback, link-local and other non-public SSH targets. Never disable the pin or log remote command output.
- Seal passwords with the panel vault and record-specific associated data. Only return public connection metadata. MCP plans, approval text, audit events and results must never contain a password.
- A risky MCP action must use plan/apply, have a procedure allow-list entry, and be tied to the exact approved procedure in `internal/panel/auth/policy_tokens.go`.
- Any claim that setup or recovery works should be backed by tests for cancellation/errors, redaction, and recovery after interruption where relevant.
