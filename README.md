<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./.github/assets/banner-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./.github/assets/banner-light.svg">
    <img alt="Mistgate: a lightweight, self-hosted VPN panel" src="./.github/assets/banner-dark.svg" width="100%">
  </picture>

  <p><b>English</b> · <a href="README.ru.md">Русский</a></p>
</div>

**Mistgate** is a lightweight, self-hosted panel for your own VPN fleet: one Go binary for the panel, one for the node agent, no Docker. Hysteria2 and AmneziaWG live side by side, in one subscription, in one admin UI. The panel keeps users, profiles and nodes; the agent on each node runs the protocols and keeps the host in the state the panel asks for.

## Why another panel

- **Light.** SQLite, systemd, two static binaries. No Docker, no database server. The panel's memory target at idle is 80 MB (a target for now, not a benchmark).
- **AmneziaWG users are not a blind spot.** Every AmneziaWG device is a peer the panel knows about: who is online, traffic per user and per device.
- **A doctor that knows hosters.** Disk and journals filling up, a resolver that cannot resolve, clock drift, port conflicts, network settings that drifted from the baseline (fq, BBR). Found, explained in plain words, fixed with one confirmed click where that is safe.
- **Looked at from the client's side.** The panel connects to every profile on every node the way a real client does (Hysteria2 and AmneziaWG), so a node is green only if traffic really flows.
- **Hidden by default.** The public listener serves a decoy site (built-in or your own directory). The admin lives under a secret path prefix, a secret host or a separate listener, and unknown subscription tokens get the same decoy.
- **Made for AI agents too.** API tokens and an MCP server, with plan / apply and owner approvals for anything risky.

## What is inside

| The fleet | Access and tooling |
|:--|:--|
| <img src="./.github/assets/icons/zap.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Hysteria2 and AmneziaWG**<br>Hysteria2 on the official core with Salamander; AmneziaWG 2.0 / 3.1, userspace by default or the kernel module per node. Several profiles per node. Protocols are plugins with a schema-driven editor. | <img src="./.github/assets/icons/qr.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Subscriptions**<br>One link per person. Each app gets the format it reads, chosen by User-Agent rules: a URI list for apps such as Happ, a Mihomo YAML profile for Clash Verge or FlClash. A browser gets the person's page; AmneziaVPN gets a `vpn://` key per device. |
| <img src="./.github/assets/icons/globe.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **WARP egress**<br>Send a profile's traffic out through Cloudflare WARP. If the WARP link drops, the profile fails closed instead of leaking direct. | <img src="./.github/assets/icons/users.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Users and devices**<br>Groups, one peer per device, traffic limits, DNS presets per user or group. A user page with instructions per platform, a QR code, traffic and term, behind its own password; people can add their own AmneziaWG devices there if you allow it. |
| <img src="./.github/assets/icons/pulse.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Fleet doctor**<br>Node self-checks with safe fixes, client-eye checks, alerts with a full lifecycle. | <img src="./.github/assets/icons/window.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Admin UI**<br>Russian and English, dark and light. Sign-in with a passkey or a password plus an authenticator code, optional Cloudflare Turnstile, an audit log. |
| <img src="./.github/assets/icons/shield.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Signed updates**<br>Node agents update only from bundles signed with your ed25519 release key. Canary rollout in batches, health gate, automatic rollback. | <img src="./.github/assets/icons/spark.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **Agent access**<br>Scripts use the same Connect API as the admin with readonly, operator and admin tokens. AI agents use the built-in MCP server; changes go through plan / apply, the risky ones wait for the owner. |

## Status

Mistgate is pre-release. It runs in production for its author, but the API, the stored settings and the node protocol may still change before 1.0, and there are no binary releases yet: build from source.

| Stage | What |
|:--|:--|
| **Done** | Panel and node agent over mTLS · Hysteria2 · AmneziaWG 2.0 / 3.1 · WARP egress · subscriptions and the public user page · DNS presets · health doctor, client-eye checks, alerts · signed node-agent updates with canary and rollback · GitHub panel self-update with checksum verification and rollback · API tokens and MCP server · admin UI in ru / en |
| **M2 — planned; not started** | Node provisioning over SSH from the UI, with preflight checks · encrypted vault for server passwords · encrypted panel backups to R2 · [M2 plan](docs/en/roadmap/m2-ssh-provisioning.md) |
| **Next** | Telegram bot for the whole fleet · more subscription formats (Xray JSON, sing-box) and subscription mirrors · a one-line installer |
| **Later** | VLESS REALITY as the first external protocol plugin |

Some defaults lean towards users in Russia (Yandex DNS for nodes in Russia, the control domains of the node doctor, split-DNS presets); all of them are settings.

## Documentation

Read the full documentation at [mistgate.app](https://mistgate.app/) (Russian: [mistgate.app/ru](https://mistgate.app/ru/)). The source lives in [`docs/en`](docs/en/index.md) and [`docs/ru`](docs/ru/index.md). Good places to start:

- [Overview](docs/en/getting-started/overview.md): the concepts (panel, node, profile, user, group, device, subscription).
- [Install the panel](docs/en/getting-started/install-panel.md), [add a node](docs/en/getting-started/add-node.md), [first users](docs/en/getting-started/first-users.md).
- [Health](docs/en/operations/health.md), [updates](docs/en/operations/updates.md), [security](docs/en/operations/security.md), [troubleshooting](docs/en/operations/troubleshooting.md).
- [CLI](docs/en/reference/cli.md), [configuration](docs/en/reference/configuration.md), [API](docs/en/reference/api.md), [MCP](docs/en/reference/mcp.md).

## Requirements

- **Panel:** Linux (amd64 or arm64) with systemd, a domain name, ports 443 (and 80 for Let's Encrypt). Data lives in SQLite under `/var/lib/mistgate`.
- **Nodes:** Linux with systemd (Ubuntu 22.04+ or Debian 12+), root, a public address, the UDP ports of your profiles open. The agent dials the panel, so a node needs no inbound management port.
- **Building:** Go 1.27 (the `toolchain` line in go.mod; `GOTOOLCHAIN=auto` fetches it), Node.js 22+, pnpm 10, make and a POSIX shell (Git Bash or WSL on Windows).

## Quick start

Build the static Linux binaries (the admin SPA is embedded into the panel):

```sh
make build            # bin/mistgate-linux-{amd64,arm64}, bin/mistgate-node-linux-{amd64,arm64}
```

On the panel server (here `panel.example.com`), as root:

```sh
install -m 0755 mistgate-linux-amd64 /usr/local/bin/mistgate
mistgate setup --public-url https://panel.example.com
#   prints the admin URL (https://panel.example.com/<secret prefix>/) and a one-time setup link
mistgate serve --listen :443 --acme-domain panel.example.com
```

Open the setup link and create the owner (a passkey, or a password with an authenticator code). Run `serve` under systemd or another supervisor; `mistgate serve -h` lists every flag, each with a `MISTGATE_*` environment fallback.

- `--tls-cert` / `--tls-key` use your own certificate instead of Let's Encrypt.
- `setup --admin-host` or `--admin-listen` put the admin on a secret host or a separate listener instead of the path prefix.
- Everything outside the admin answers with the decoy site; `--decoy-dir` serves your own.

To add a node, open **Nodes → Add node** in the admin. It shows a one-time command. Put the agent binary on the server as `/root/mistgate-node` (the panel prints the `scp` line once it holds a signed bundle) and run the command as root:

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

`enroll` exchanges the token for a node certificate, `install` writes a hardened systemd unit and starts the agent. The node turns online in the admin by itself. Then create a profile, put it on the node, give it to a group and create users; the first-run checklist on the Overview walks through it.

> **Note:** in the API the subscription way is the enum value `HAPP` (and `access.happ`, `users.apps.happ`). It means "by the subscription link", whatever app opens it; `AMNEZIA` means "by an AmneziaWG key".

## Development

```
cmd/mistgate/            panel: serve, setup, auth, mcp (stdio proxy), release, version
cmd/mistgate-node/       node agent: enroll, install, run, cleanup-net, awg prepare-kernel, version
proto/mistgate/          admin API and panel <-> agent API (Connect-RPC)
gen/, web/src/gen/       generated from proto/ (never edit by hand)
internal/panel/          panel modules: store, vault, auth, fleet, access, subs, protocols, health, update, warp, mcp, httpserver ...
internal/node/           agent modules: agent, engine, hysteria2, awg, warp, hostctl, doctor, update ...
web/                     admin SPA (Vite, React, TypeScript, TanStack Router/Query, Tailwind) and the user page
scripts/                 end-to-end tests
```

Linux-only code (nftables, netlink, AmneziaWG, systemd) sits behind `//go:build linux` with stubs, so `go build ./...`, `go vet ./...` and `go test ./...` work on Windows and macOS too.

```sh
make dev                              # the panel in dev mode: decoy :8080, admin :8081, agent endpoint :8082, data in ./.data
cd web && pnpm install && pnpm dev    # Vite on http://localhost:5173 with hot reload, proxied to the admin on :8081
make test                             # go vet, go test, then pnpm typecheck, lint and vitest
make gen                              # buf lint + buf generate after editing proto/ (remote plugins: needs internet)
```

- On first start the dev panel prints a one-time setup link (`http://localhost:8081/setup#...`). Reset everything by stopping it and deleting `./.data`.
- If 8081 is taken, run the panel with `--admin-listen 127.0.0.1:<port>` and Vite with `MISTGATE_PANEL=http://127.0.0.1:<port> pnpm dev`.
- To try a node against the dev panel, run both in WSL (or a Linux VM): `mistgate serve --dev`, add the node in the admin, then `mistgate-node enroll ... --state-dir /tmp/node` and `mistgate-node run --state-dir /tmp/node` as root. The agent never dials private addresses, so test traffic against a public site.

End-to-end tests:

- `scripts/e2e-wsl.sh [--keep] [--m3 | --awg | --warp | --mihomo | --m3-only | --old-node <path>]`: the panel, a node, a real Hysteria2 client and self-update, optionally AmneziaWG clients, a fake WARP peer and a real mihomo. Runs as root in WSL in its own network namespace (`wsl -d Ubuntu -u root -- bash scripts/e2e-wsl.sh` from the repo root); needs go, curl, jq, python3 (with yaml for the tunnel steps), openssl, nft, ip, and internet access. 3 to 10 minutes.
- `scripts/e2e-mcp.sh [-mode listener|prefix|both] [-keep]`: API tokens and MCP against a real panel on loopback, any OS, no internet, about a minute.

> **Warning:** tests that need root (`MG_ROOT_TESTS=1`) and the e2e scripts change network state. Run them in WSL or on a disposable VM, never on a production node or panel.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the conventions.

## Releasing node updates

```sh
mistgate release keygen --out ~/mistgate-release.key      # once; prints the public key, keep the file offline
RELEASE_KEY=<public key> make build                       # stamps the key and the build time into both binaries
mistgate release sign --key ~/mistgate-release.key --version "$(git describe --tags --always)" \
  --built "$(git log -1 --format=%ct)" --expires 30d \
  bin/mistgate-node-linux-amd64 bin/mistgate-node-linux-arm64 --out dist/
scp dist/* panel.example.com:/var/lib/mistgate/dist/      # the panel picks it up within a minute
```

Then start a rollout on the **Updates** page (owner only, with a step-up).

- Node agents built without `RELEASE_KEY` cannot update themselves. The panel keeps this installation's bundle-verification key in `release.pub` while its own binary updates from GitHub.
- The panel checks official GitHub Releases and can install a verified Linux release on root systemd installations. It keeps the previous binary and a stopped-service data backup for rollback.
- Nodes that predate self-update are updated once by hand (`mistgate-node install` with the new binary).

## Security notes

- Back up the panel's data directory (`/var/lib/mistgate`): it holds the database, the master key that encrypts the stored secrets, and the panel CA the nodes trust. Losing it means re-enrolling every node.
- Keep the admin behind the secret prefix, a secret host or a separate listener; do not publish its address.
- API tokens and the MCP endpoint never receive subscription links, device keys or page passwords; still, give agents the narrowest profile that works.
- Report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## License

Mistgate is free software under the [GNU Affero General Public License v3.0 only](LICENSE). If you run a modified panel for others, its admin shows a "Source code" link (`serve --source-url`) that should point to your source.
