<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./.github/assets/banner-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./.github/assets/banner-light.svg">
    <img alt="Mistgate: a lightweight, self-hosted VPN panel" src="./.github/assets/banner-dark.svg" width="100%">
  </picture>

  <p><b>English</b> · <a href="README.ru.md">Русский</a> · <a href="https://mistgate.app/">Documentation</a></p>
</div>

**Mistgate** is a lightweight, self-hosted panel for your own VPN fleet: one Go binary for the panel, one for the node agent, no Docker. Hysteria2 and AmneziaWG live side by side, in one subscription, in one admin UI. The panel keeps users, profiles and nodes; the agent on each node runs the protocols and keeps the host in the state the panel asks for.

> **Install with an AI agent.** Claude Code, Codex CLI or any agent that can run `ssh` on your computer can install the panel for you: copy the install prompt from the [AI agent guide](docs/en/getting-started/ai-agents.md#install-mistgate-with-an-ai-agent). It asks before every change and hands you the setup link; you create the owner account yourself.

## Why another panel

- **Light.** SQLite, systemd, two static binaries. No Docker, no database server. The panel's memory target at idle is 80 MB (a target for now, not a benchmark).
- **AmneziaWG users are not a blind spot.** Every AmneziaWG device is a peer the panel knows about: who is online, traffic per user and per device.
- **A doctor that knows hosters.** Disk and journals filling up, a resolver that cannot resolve, clock drift, port conflicts, network settings that drifted from the baseline (fq, BBR). Found, explained in plain words, fixed with one confirmed click where that is safe.
- **Looked at from the client's side.** The panel connects to every profile on every node the way a real client does (Hysteria2 and AmneziaWG), so a node is green only if traffic really flows.
- **Hidden by default.** The public listener serves a decoy site (built-in or your own directory). The admin lives under a secret path prefix, a secret host or a separate listener, and unknown subscription tokens get the same decoy.
- **Made for AI agents too.** API tokens and an MCP server, with plan / apply and owner approvals for anything risky; ready prompts for installing and running the fleet.

## What is inside

| The fleet | Access and tooling |
|:--|:--|
| <img src="./.github/assets/icons/zap.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Hysteria2](docs/en/guide/hysteria2.md) and [AmneziaWG](docs/en/guide/amneziawg.md)**<br>Hysteria2 on the official core with Salamander; AmneziaWG 2.0 / 3.1, userspace by default or the kernel module per node. Several profiles per node. Protocols are plugins with a schema-driven editor. | <img src="./.github/assets/icons/qr.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Subscriptions](docs/en/guide/subscriptions.md)**<br>One link per person. Each app gets the format it reads, chosen by User-Agent rules: a URI list for apps such as Happ, a Mihomo YAML profile for [kl!ck](docs/en/guide/client-apps.md#klck), the desktop app Mistgate recommends, Clash Verge or FlClash. A browser gets the person's page; AmneziaVPN gets a `vpn://` key per device. See [Client apps](docs/en/guide/client-apps.md). |
| <img src="./.github/assets/icons/globe.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[WARP egress](docs/en/guide/warp.md)**<br>Send a profile's traffic out through Cloudflare WARP. If the WARP link drops, the profile fails closed instead of leaking direct. | <img src="./.github/assets/icons/users.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Users and devices](docs/en/guide/users-and-groups.md)**<br>Groups, one peer per device, traffic limits, [DNS presets](docs/en/guide/dns.md) per user or group. A [user page](docs/en/guide/user-page.md) with instructions per platform, a QR code, traffic and term, behind its own password; people can add their own AmneziaWG devices there if you allow it. |
| <img src="./.github/assets/icons/pulse.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Fleet doctor](docs/en/operations/health.md)**<br>Node self-checks with safe fixes, client-eye checks, alerts with a full lifecycle, and per-node [torrent protection](docs/en/guide/torrent-protection.md) that blocks recognized BitTorrent in the kernel without logging addresses. | <img src="./.github/assets/icons/window.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Admin UI](docs/en/operations/security.md)**<br>Russian and English, dark and light. Sign-in with a passkey or a password plus an authenticator code, optional Cloudflare Turnstile, an audit log. |
| <img src="./.github/assets/icons/terminal.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Install over SSH](docs/en/getting-started/ssh-install.md)**<br>Confirm the host key, type the password, watch the node connect. The panel checks the server first, prepares an active host firewall and keeps the SSH access encrypted, with a verified password change. | <img src="./.github/assets/icons/archive.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Encrypted backups](docs/en/operations/backups.md)**<br>Scheduled backups to your Cloudflare R2 bucket, encrypted to an offline recovery key before they leave the panel, with retention and an offline restore. |
| <img src="./.github/assets/icons/shield.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Signed updates](docs/en/operations/updates.md)**<br>Node agents and the panel install only releases signed with the release key. Update a node now or on a schedule, with a health gate and automatic rollback. | <img src="./.github/assets/icons/spark.svg" width="32" height="32" align="absmiddle" alt="">&nbsp; **[Agent access](docs/en/getting-started/ai-agents.md)**<br>Scripts use the same [Connect API](docs/en/reference/api.md) as the admin with readonly, operator and admin tokens. AI agents use the built-in [MCP server](docs/en/reference/mcp.md); changes go through plan / apply, the risky ones wait for the owner. |

## Status

Mistgate is in early releases; the current one is [`v0.1.15`](https://github.com/Mistgate/mistgate/releases/latest). It runs in production for its author, but the API, stored settings and node protocol may still change before 1.0. Linux binaries for amd64 and arm64 are on [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest); build from source for other targets.

| Stage | What |
|:--|:--|
| **Done** | Panel and node agent over mTLS · Hysteria2 · AmneziaWG 2.0 / 3.1 · WARP egress · subscriptions and the user page · DNS presets · health doctor, client-eye checks, alerts · per-node torrent protection · SSH installation from the admin and through MCP with owner approval · saved SSH access with verified password change · encrypted Cloudflare R2 backups and restore · signed node updates, now or scheduled, with health gate and rollback · panel self-update from signed releases · reproducible, offline-signed releases · API tokens and MCP server · admin UI in ru / en |
| **Now** | Field testing of `v0.1.15` in production: torrent protection, SSH installation and recovery, signed panel and node releases |
| **Next** | Telegram bot for the whole fleet · more subscription formats (Xray JSON, sing-box) and subscription mirrors · a one-line installer |
| **Later** | VLESS REALITY as the first external protocol plugin |

Details and the known limits: [Status and roadmap](docs/en/roadmap/status.md). Some defaults lean towards users in Russia (Yandex DNS for nodes in Russia, the control domains of the node doctor, split-DNS presets); all of them are settings.

## Quick start

You need a Linux server with systemd and a domain pointing at it (here `panel.example.com`), with TCP 80 and 443 free. On the server, as root:

```sh
ARCH=amd64    # arm64 on an ARM server
for f in "mistgate-linux-$ARCH" SHA256SUMS; do
  curl -fsSLO "https://github.com/Mistgate/mistgate/releases/latest/download/$f"
done
sha256sum --check --ignore-missing SHA256SUMS
install -m 0755 "mistgate-linux-$ARCH" /usr/local/bin/mistgate
mistgate setup --public-url https://panel.example.com
#   prints the admin URL (https://panel.example.com/<secret prefix>/) and a one-time setup link
mistgate serve --listen :443 --acme-domain panel.example.com    # a first run in the foreground
```

Then run `serve` under systemd with the unit from [Install the panel](docs/en/getting-started/install-panel.md), open the setup link and create the owner (a passkey, or a password with an authenticator code). The install page also covers what the checksum does and does not prove, the secret admin host or separate listener (`setup --admin-host`, `--admin-listen`), your own certificate (`--tls-cert`, `--tls-key`) and your own decoy site (`--decoy-dir`). `mistgate serve -h` lists every flag, each with a `MISTGATE_*` environment fallback.

Add nodes in **Nodes → Add node**:

- **Install automatically over SSH**: enter the server's address, confirm its host key, type the password; the panel checks the server, installs the agent and waits for it. See [Install a node over SSH](docs/en/getting-started/ssh-install.md).
- **Get a manual install command**: copy `mistgate-node` to the server and run the one-time command as root. See [Add a node](docs/en/getting-started/add-node.md).

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

Then create a profile, put it on the node, give it to a group and create users; the first-run checklist on the Overview walks through it ([First users](docs/en/getting-started/first-users.md)).

> **Note:** in the API the subscription way is the enum value `HAPP` (and `access.happ`, `users.apps.happ`). It means "by the subscription link", whatever app opens it; `AMNEZIA` means "by an AmneziaWG key".

## Documentation

The full documentation is at [mistgate.app](https://mistgate.app/) (Russian: [mistgate.app/ru](https://mistgate.app/ru/)); its source is [`docs/`](docs/README.md), which reads the same on GitHub. Good places to start:

- [Overview](docs/en/getting-started/overview.md) and [Requirements](docs/en/getting-started/requirements.md): the concepts and what you need.
- [Install the panel](docs/en/getting-started/install-panel.md), [install a node over SSH](docs/en/getting-started/ssh-install.md), [first users](docs/en/getting-started/first-users.md).
- [AI agent guide](docs/en/getting-started/ai-agents.md): prompts to install the panel, run the fleet through MCP and contribute.
- [Health](docs/en/operations/health.md), [updates](docs/en/operations/updates.md), [encrypted backups](docs/en/operations/backups.md), [security](docs/en/operations/security.md), [troubleshooting](docs/en/operations/troubleshooting.md).
- [CLI](docs/en/reference/cli.md), [configuration](docs/en/reference/configuration.md), [API](docs/en/reference/api.md), [MCP](docs/en/reference/mcp.md), [architecture](docs/en/reference/architecture.md).

Coding agents should start with [`AGENTS.md`](AGENTS.md); the published site also has [`llms.txt`](https://mistgate.app/llms.txt).

## Requirements

- **Panel:** Linux (amd64 or arm64) with systemd, a domain name, ports 443 (and 80 for Let's Encrypt). Data lives in SQLite under `/var/lib/mistgate`.
- **Nodes:** Linux with systemd (Ubuntu 22.04+ or Debian 12+), root or passwordless `sudo`, a public address, the UDP ports of your profiles open. The agent dials the panel, so a node needs no inbound management port.
- **Building:** Go 1.27 (the `toolchain` line in go.mod; `GOTOOLCHAIN=auto` fetches it), Node.js 22+, pnpm 10, make and a POSIX shell (Git Bash or WSL on Windows).

## Development

```
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

Linux-only code (nftables, netlink, AmneziaWG, systemd) sits behind `//go:build linux` with stubs, so `go build ./...`, `go vet ./...` and `go test ./...` work on Windows and macOS too.

```sh
make build                            # bin/mistgate-linux-{amd64,arm64}, bin/mistgate-node-linux-{amd64,arm64}
make dev                              # the panel in dev mode: decoy :8080, admin :8081, agent endpoint :8082, data in ./.data
cd web && pnpm install && pnpm dev    # Vite on http://localhost:5173 with hot reload, proxied to the admin on :8081
make test                             # go vet, go test, then pnpm typecheck, lint and vitest
make gen                              # buf lint + buf generate after editing proto/ (remote plugins: needs internet)
```

- On first start the dev panel prints a one-time setup link (`http://localhost:8081/setup#...`). Reset everything by stopping it and deleting `./.data`.
- If 8081 is taken, run the panel with `--admin-listen 127.0.0.1:<port>` and Vite with `MISTGATE_PANEL=http://127.0.0.1:<port> pnpm dev`.
- To try a node against the dev panel, run both in WSL (or a Linux VM): `mistgate serve --dev`, add the node in the admin, then `mistgate-node enroll ... --state-dir /tmp/node` and `mistgate-node run --state-dir /tmp/node` as root. The agent never dials private addresses, so test traffic against a public site.
- A binary built without `RELEASE_KEY` cannot update itself or its nodes; see [Releases and signing](docs/en/operations/releases.md).

End-to-end tests:

- `scripts/e2e-wsl.sh [--keep] [--m3 | --awg | --warp | --mihomo | --m3-only | --old-node <path>]`: the panel, a node, a real Hysteria2 client and self-update, optionally AmneziaWG clients, a fake WARP peer and a real mihomo. Runs as root in WSL in its own network namespace (`wsl -d Ubuntu -u root -- bash scripts/e2e-wsl.sh` from the repo root); needs go, curl, jq, python3 (with yaml for the tunnel steps), openssl, nft, ip, and internet access. 3 to 10 minutes.
- `scripts/e2e-mcp.sh [-mode listener|prefix|both] [-keep]`: API tokens and MCP against a real panel on loopback, any OS, no internet, about a minute.

> **Warning:** tests that need root (`MG_ROOT_TESTS=1`) and the e2e scripts change network state. Run them in WSL or on a disposable VM, never on a production node or panel.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the conventions, and the [contribute prompt](docs/en/getting-started/ai-agents.md#contribute-with-a-coding-agent) if you work with a coding agent.

## Releases

Every stable tag builds a draft release in GitHub Actions. A maintainer rebuilds the binaries from the tag on a machine that holds the offline release key, and `mistgate release sign` signs only binaries that rebuild byte for byte; the signed manifests are uploaded and the draft is published. Panels then download the signed node bundle by themselves and offer the panel update on the Updates page. The whole procedure, building with a key of your own and rotating the key: [Releases and signing](docs/en/operations/releases.md).

## Security notes

- Back up the panel's data directory (`/var/lib/mistgate`): it holds the database, the master key that encrypts the stored secrets, and the panel CA the nodes trust. Losing it means re-enrolling every node. [Encrypted R2 backups](docs/en/operations/backups.md) do this on a schedule.
- Keep the admin behind the secret prefix, a secret host or a separate listener; do not publish its address.
- API tokens and the MCP endpoint never receive subscription links, device keys, page passwords or server passwords; still, give agents the narrowest profile that works.
- Report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## License

Mistgate is free software under the [GNU Affero General Public License v3.0 only](LICENSE). If you run a modified panel for others, its admin shows a "Source code" link (`serve --source-url`) that should point to your source.
