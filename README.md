<p align="center"><img src="web/src/assets/mistgate-badge.svg" alt="Mistgate" width="96"></p>

<p align="center"><b>English</b> · <a href="README.ru.md">Русский</a></p>

# Mistgate

Mistgate is a self-hosted panel for a small VPN fleet: one Go binary for the panel, one for the node agent, no Docker.
The panel keeps users, profiles and nodes; the agent on each node runs the protocols and keeps the host in the state
the panel asks for.

- **Protocols.** Hysteria2 (the official core, built in), AmneziaWG 2.0/3.1 (userspace by default, the kernel module
  per node on request), Cloudflare WARP as an egress for both. Protocols are plugins with a schema-driven editor.
- **One subscription for many apps.** A person gets one link. An app that opens it gets the format it reads (a URI list,
  a Mihomo YAML profile), chosen by User-Agent rules; a browser gets the person's page. AmneziaVPN users get a key per device.
- **Hidden by default.** The public listener serves a decoy site (built-in or your own directory); the admin lives
  under a secret path prefix, a secret host or a separate listener, and unknown subscription tokens get the same decoy.
- **User pages.** Each person has a page with instructions per platform, a QR code, traffic and term, and (if allowed)
  self-service AmneziaWG devices. The page is protected by its own password.
- **Health.** The panel dials every inbound as a real client (synthetic checks), each node runs a doctor of host checks
  with a few safe fixes, and alerts say what is wrong in plain words.
- **Signed node updates.** Node agents update themselves only from bundles signed with your release key, in a staged
  rollout (canary, batches, a health gate, automatic rollback).
- **API tokens and MCP.** Scripts use the same Connect API as the admin with scoped tokens (readonly, operator, admin).
  AI agents use the built-in MCP server; dangerous changes are planned by the agent and wait for the owner's approval.
- **Admin sign-in** with passkeys or a password plus an authenticator code, optional Cloudflare Turnstile, an audit log.
  The UI speaks English and Russian.

## Status

Early. Mistgate runs in production for its author, but the API, the stored settings and the node protocol may still
change before 1.0, and there are no binary releases yet: build from source. Some defaults lean towards users in
Russia (Yandex DNS for nodes in Russia, the control domains of the node doctor, split-DNS presets); all of them are
settings.

## Requirements

- **Panel:** Linux (amd64 or arm64) with systemd, a domain name, ports 443 (and 80 for Let's Encrypt). It keeps its
  data in SQLite under `/var/lib/mistgate` and targets about 80 MB of RAM when idle.
- **Nodes:** Linux with systemd (Ubuntu 22.04+ or Debian 12+), root, a public address, the UDP ports of your profiles
  open. The agent dials the panel, so a node needs no inbound management port.
- **Building:** Go 1.27 (the `toolchain` line in go.mod; `GOTOOLCHAIN=auto` fetches it), Node.js 22+, pnpm 10, make and a
  POSIX shell (Git Bash or WSL on Windows).

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

Open the setup link and create the owner (a passkey, or a password with an authenticator code). Run `serve` under
systemd or another supervisor; `mistgate serve -h` lists every flag, each with a `MISTGATE_*` environment fallback.
`--tls-cert`/`--tls-key` use your own certificate instead of Let's Encrypt; `setup --admin-host` or `--admin-listen`
put the admin on a secret host or a separate listener instead of the path prefix. Everything outside the admin
answers with the decoy site (`--decoy-dir` serves your own).

To add a node, open **Nodes → Add node** in the admin. It shows a one-time command; put the agent binary on the
server as `/root/mistgate-node` (the panel prints the `scp` line once it holds a signed bundle) and run the command as
root. It looks like this:

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

`enroll` exchanges the token for a node certificate, `install` writes a hardened systemd unit and starts the agent.
The node turns online in the admin by itself. Then create a profile, put it on the node, give it to a group and
create users; the first-run checklist on the Overview walks through it.

A note on names: in the API the subscription way is the enum value `HAPP` (and `access.happ`, `users.apps.happ`). It
means "by the subscription link", whatever app opens it; `AMNEZIA` means "by an AmneziaWG key".

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

Linux-only code (nftables, netlink, AmneziaWG, systemd) sits behind `//go:build linux` with stubs, so
`go build ./...`, `go vet ./...` and `go test ./...` work on Windows and macOS too.

```sh
make dev                              # the panel in dev mode: decoy :8080, admin :8081, agent endpoint :8082, data in ./.data
cd web && pnpm install && pnpm dev    # Vite on http://localhost:5173 with hot reload, proxied to the admin on :8081
make test                             # go vet, go test, then pnpm typecheck, lint and vitest
make gen                              # buf lint + buf generate after editing proto/ (remote plugins: needs internet)
```

On first start the dev panel prints a one-time setup link (`http://localhost:8081/setup#...`). Reset everything by
stopping it and deleting `./.data`. If 8081 is taken, run the panel with `--admin-listen 127.0.0.1:<port>` and Vite
with `MISTGATE_PANEL=http://127.0.0.1:<port> pnpm dev`. To try a node against the dev panel, run both in WSL (or a
Linux VM): `mistgate serve --dev`, add the node in the admin, then `mistgate-node enroll ... --state-dir /tmp/node`
and `mistgate-node run --state-dir /tmp/node` as root. The agent never dials private addresses, so test traffic
against a public site.

End-to-end tests:

- `scripts/e2e-wsl.sh [--keep] [--m3 | --awg | --warp | --mihomo | --m3-only | --old-node <path>]`: the panel, a node,
  a real Hysteria2 client and self-update, optionally AmneziaWG clients, a fake WARP peer and a real mihomo. Runs as root
  in WSL in its own network namespace (`wsl -d Ubuntu -u root -- bash scripts/e2e-wsl.sh` from the repo root); needs
  go, curl, jq, python3 (with yaml for the tunnel steps), openssl, nft, ip, and internet access. 3 to 10 minutes.
- `scripts/e2e-mcp.sh [-mode listener|prefix|both] [-keep]`: API tokens and MCP against a real panel on loopback,
  any OS, no internet, about a minute.

Tests that need root (`MG_ROOT_TESTS=1`) and the e2e scripts change network state. Run them in WSL or on a disposable
VM, **never on a production node or panel**.

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

Then start a rollout on the **Updates** page (owner only, with a step-up). Binaries built without `RELEASE_KEY` never
update themselves. The panel itself is updated by hand: replace the binary and restart the service. Nodes that
predate self-update are updated once by hand (`mistgate-node install` with the new binary).

## Security notes

- Back up the panel's data directory (`/var/lib/mistgate`): it holds the database, the master key that encrypts the
  stored secrets, and the panel CA the nodes trust. Losing it means re-enrolling every node.
- Keep the admin behind the secret prefix, a secret host or a separate listener; do not publish its address.
- API tokens and the MCP endpoint never receive subscription links, device keys or page passwords; still, give agents
  the narrowest profile that works.
- Report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## License

Mistgate is free software under the [GNU Affero General Public License v3.0 only](LICENSE). If you run a modified
panel for others, its admin shows a "Source code" link (`serve --source-url`) that should point to your source.
