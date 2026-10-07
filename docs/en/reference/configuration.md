---
title: Configuration
description: Every flag of mistgate serve and mistgate setup with its environment variable and default, the other configurable commands, the node agent's flags, and the data directory layout.
---

The panel has no configuration file. `mistgate setup` stores the addresses of an installation in the database once; everything else is a flag of `mistgate serve`, and every flag has a `MISTGATE_*` environment variable. This page lists them all, together with the flags of the node agent and what lives in the data directories. For the commands themselves see [CLI](cli.md).

## How flags and environment variables combine

- A flag on the command line wins over its environment variable.
- An environment variable set to an empty string counts as not set, so the default applies. The exceptions are `MISTGATE_SOURCE_URL` (set to empty, it hides the source link) and `MISTGATE_ACME_HTTP` (set to empty, it turns the port 80 listener off).
- Boolean variables (`MISTGATE_DEV`) accept `1`, `true`, `yes` or `on`, in any case.
- The repeatable flags `--acme-domain` and `--trusted-proxy` also take comma-separated values. Their environment variables are comma-separated lists and are read only when the flag is not given at all.

## `mistgate serve`

| Flag | Environment | Default | Meaning |
|:--|:--|:--|:--|
| `--listen` | `MISTGATE_LISTEN` | `127.0.0.1:8080` | Address of the public listener: the decoy site, subscriptions and user pages, the admin in the prefix and host modes, and (with TLS) the node agent endpoint. On a real server `:443`. |
| `--tls-cert` | `MISTGATE_TLS_CERT` | none | PEM certificate (with its chain) for the public listener. Read again when the file changes, checked at most every 30 seconds; a pair that fails to load is logged and the old one stays. Needs `--tls-key`. |
| `--tls-key` | `MISTGATE_TLS_KEY` | none | The private key of `--tls-cert`. The two go together. |
| `--acme-domain` | `MISTGATE_ACME_DOMAIN` | none | Get a Let's Encrypt certificate for this host name. Repeatable. Plain host names only, no wildcards. Uses TLS-ALPN-01 on the public listener, which must be reachable on port 443. Using it accepts the CA's terms of service. With `--tls-cert` as well, these names use Let's Encrypt and every other name the static certificate. |
| `--acme-email` | `MISTGATE_ACME_EMAIL` | none | Contact address for Let's Encrypt. Optional. |
| `--acme-http` | `MISTGATE_ACME_HTTP` | `:80` | Listener for ACME HTTP-01 and the redirect from HTTP to HTTPS, used only with `--acme-domain`. Other host names and unknown paths get the decoy's 404. Pass `--acme-http=` (empty) or set `MISTGATE_ACME_HTTP=` (empty) to turn it off. |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | the address setup stored | Separate plain-HTTP listener for the admin. Only for an installation set up with `setup --admin-listen`; with a secret prefix or host it is an error. It overrides the stored address, but passkeys stay bound to the port setup stored. |
| `--agent-listen` | `MISTGATE_AGENT_LISTEN` | none (`127.0.0.1:8082` with `--dev`) | Separate TLS listener for the node agent endpoint. Without it agents use the public listener with the secret TLS name, which needs TLS on the public listener. |
| `--agent-addr` | `MISTGATE_AGENT_ADDR` | derived | The `host:port` agents dial, written into new install commands. Without it: `--agent-listen` when it names a concrete host (not empty, `0.0.0.0` or `[::]`), else the host and port of the public URL (443 when the URL has no port). Enrolled nodes keep the address they enrolled with. |
| `--trusted-proxy` | `MISTGATE_TRUSTED_PROXY` | none | CIDR or IP of a reverse proxy whose `X-Forwarded-For` and `Forwarded` headers are believed. Repeatable. Without it the TCP peer is the client, whatever the headers say. |
| `--decoy-dir` | `MISTGATE_DECOY_DIR` | the built-in page | Directory with your own static decoy site (`index.html`, optional `404.html`, `429.html`, `robots.txt`). |
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | The data directory. It must exist (run `mistgate setup` first); `serve` sets its mode to 0700 at every start. |
| `--update-service` | `MISTGATE_UPDATE_SERVICE` | `mistgate.service` (none with `--dev`) | The panel's own systemd unit, which the panel updater stops and starts again around the binary swap. It matters when the panel runs as root: the updater then starts its helper as a transient unit with this name. A panel running as another user starts the fixed root helper unit `mistgate-panel-update.service` instead, whose command line names the unit and the data directory itself (edit that unit if yours differ); see [Updates](../operations/updates.md). With `--dev` the panel does not update itself. |
| `--source-url` | `MISTGATE_SOURCE_URL` | `https://github.com/Mistgate/mistgate` | Where the source code of this build is published, linked as "Source code" next to the version in the admin (AGPL-3.0, section 13). A fork points it at its own repository; empty hides the link. |
| `--dev` | `MISTGATE_DEV` | off | Development mode: data in `./.data` (created with a master key), plain HTTP with the decoy on `--listen` (`127.0.0.1:8080`) and the admin on `127.0.0.1:8081`, the agent endpoint on `127.0.0.1:8082`, WebAuthn on `localhost`, and a setup link printed at start while no admin exists. Nothing about the addresses is stored. Never on a public server. |

Without `--tls-cert` and `--acme-domain` the public listener speaks plain HTTP. That only makes sense behind a reverse proxy that terminates TLS, together with `--trusted-proxy` and `--agent-listen`; see "Behind a reverse proxy" in [Install the panel](../getting-started/install-panel.md).

Built-in limits that are not flags: each client (an IPv4 address or an IPv6 /64) may make 10 requests a second with a burst of 60 on the public side, 30 a second with a burst of 200 on the admin, and 5 a second with a burst of 30 on the agent endpoint. A request over the limit gets a 429 page in the style of the decoy site.

## `mistgate setup`

Setup runs once per installation. The first run stores the addresses below; later runs keep them, print the admin URL and, while no admin exists, a new one-time setup link.

| Flag | Environment | Default | Meaning |
|:--|:--|:--|:--|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` | Where to create the data directory, the master key and the database. |
| `--public-url` | `MISTGATE_PUBLIC_URL` | none | URL of the public (decoy) site, `http(s)://host[:port]`, for example `https://panel.example.com`. The base of subscription links and of the agents' address. Required unless `--admin-host` or `--admin-listen` is given, and recommended always. |
| `--admin-host` | `MISTGATE_ADMIN_HOST` | none | Serve the admin on this secret host name instead of a secret path prefix. |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | none | Serve the admin on a separate listener, for example `127.0.0.1:8081`. Cannot be combined with `--admin-host`. |
| `--rp-id` | `MISTGATE_RP_ID` | derived | WebAuthn relying party ID. |
| `--rp-origins` | `MISTGATE_RP_ORIGINS` | derived | Comma-separated browser origins allowed for WebAuthn and for state-changing admin requests. |

What setup derives in each mode:

| Mode | Admin URL | WebAuthn RP ID | Allowed origin |
|:--|:--|:--|:--|
| Secret prefix (neither `--admin-host` nor `--admin-listen`) | `<public-url>/<24 random characters>/` | the host of the public URL | the scheme, host and port of the public URL |
| `--admin-host` | `https://<admin host>/` (scheme and port taken from the public URL when given) | the admin host | the admin URL's origin |
| `--admin-listen` | `http://localhost:<port>/` | `localhost` | `http://localhost:<port>` |

Setup also generates two secrets in every mode: the secret TLS name of the agent endpoint (16 random characters as a label under the public URL's host, else under the admin host, else under `com` when the panel has no domain name; it never needs a DNS record) and the secret path prefix of subscription links (24 random characters). All of it is stored in the database. There is no command to change these values afterwards.

> **Warning:** `MISTGATE_ADMIN_LISTEN` is read by both `setup` and `serve`. Set it only for an installation that uses the separate admin listener.

## Other panel commands

**`mistgate auth turnstile off`** and **`mistgate auth reset-login`** work on the panel's own server, with the panel running or stopped:

| Flag | Environment | Default | Meaning |
|:--|:--|:--|:--|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | The panel's data directory. The command refuses a directory without a database. |
| `--dev` | `MISTGATE_DEV` | off | Use the development data directory `./.data`. |
| `--admin` | none | none | `reset-login` only: which admin gets the new password login, when that admin has passkeys only and the panel has several. |
| `--qr-invert` | none | off | `reset-login` only: draw the QR code for a light terminal background. |

**`mistgate mcp`**, the stdio proxy for MCP clients:

| Flag | Environment | Default | Meaning |
|:--|:--|:--|:--|
| `--url` | `MISTGATE_URL` | none | The admin URL that setup printed, for example `https://panel.example.com/<secret>/`. Plain `http` is refused unless the host is localhost. |
| `--token-file` | `MISTGATE_TOKEN_FILE` | none | A file whose first line is an API token. The token is never taken from the command line or the environment. |

**`mistgate backup keygen`** and **`mistgate backup restore`**, the offline side of [encrypted backups](../operations/backups.md), take only flags, all required, no environment variables:

| Flag | Meaning |
|:--|:--|
| `keygen --identity-file` | Where to create the private age recovery identity (mode 0600, never overwritten). |
| `restore --identity-file` | That identity. |
| `restore --file` | The downloaded encrypted backup (`.tar.gz.age`). |
| `restore --data-dir` | A new directory that does not exist yet. `MISTGATE_DATA_DIR` is not read. |

**`mistgate release keygen`**, **`mistgate release build`** and **`mistgate release sign`** take only flags, no environment variables (`release trust-key` also reads `MISTGATE_DATA_DIR` and `MISTGATE_DEV`, and takes `--data-dir` and `--dev`): see [CLI](cli.md) and [Releases and signing](../operations/releases.md).

The hidden **`mistgate panel-update-helper`** takes `--fetch-latest` (required), `--data-dir` and `--service`. Only the panel updater starts it, as root; the fixed unit `mistgate-panel-update.service` runs it with `--data-dir /var/lib/mistgate --service mistgate.service`.

## Other environment variables

| Variable | Read by | Meaning |
|:--|:--|:--|
| `CREDENTIALS_DIRECTORY` | the panel | Set by systemd's `LoadCredential=`. When `master.key` is there, it is used instead of `<data-dir>/master.key`. Note that `mistgate auth reset-login` run from a shell does not see it. |
| `MISTGATE_HEALTH_BLIP_WINDOW` | `mistgate serve` | A Go duration that shortens the health module's blip window. A hook for the end-to-end tests; leave it unset. |
| `MISTGATE_UNIT_GEN` | `mistgate-node run` | The generation of the systemd unit, written into the unit by `mistgate-node install`. Do not set it yourself. |
| `GOMEMLIMIT` | `mistgate-node run` | The Go memory limit, written into the unit by `install` (about 60% of RAM). |
| `MISTGATE_PANEL` | the Vite dev server | Development only: where `pnpm dev` proxies the admin API (default `http://127.0.0.1:8081`). Not the same as the agent's `MISTGATE_PANEL` below. |

## Node agent: `mistgate-node`

| Command and flag | Environment | Default | Meaning |
|:--|:--|:--|:--|
| `enroll --panel` | `MISTGATE_PANEL` | none | Panel address, `host:port`. |
| `enroll --sni` | `MISTGATE_AGENT_SNI` | none | The panel's secret TLS name for agents. |
| `enroll --ca-sha256` | `MISTGATE_CA_SHA256` | none | SHA-256 fingerprint of the panel CA certificate, 64 hex digits (colons and case are ignored). |
| `enroll --token` | `MISTGATE_ENROLL_TOKEN` | none | The one-time enrollment token. The variable keeps it out of the process list. |
| `enroll --token-stdin` | none | off | Read the token from stdin (one line, at most 256 bytes). Not together with `--token` or `MISTGATE_ENROLL_TOKEN`. |
| `enroll --resume-key` | none | off | Keep the new key in `enroll-pending.pem` until enrollment succeeds and reuse it on a retry. Used by the SSH install. |
| `enroll --force` | none | off | Replace an identity that is already there. |
| `enroll`, `install`, `run --state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | The agent's state directory. Use the same one for all three. |
| `run --link-url` | `MISTGATE_LINK_URL` | empty | Optional `wss://host/<secret-prefix>` base for the signed WebSocket link. The agent uses it when the panel advertises support; otherwise it uses mTLS. Every panel serves and advertises the link; only agents with this value use it. If the link cannot be established the agent falls back to mTLS and tries the link again after 10 minutes. Existing nodes stay on mTLS unless you configure this value. The install command does not set it. |
| `install --bin` | none | `/usr/local/bin/mistgate-node` | Where the binary lives; the running executable is copied there when it is elsewhere. Its directory becomes writable for the agent (self-update). |
| `install --no-start` | none | off | Write and enable the unit without starting it. |
| `run --log-level` | `MISTGATE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `run --log-format` | `MISTGATE_LOG_FORMAT` | `text` | `text` or `json`. |
| `awg prepare-kernel --yes` | none | off | Run the plan; without it the plan is only printed. |
| `awg prepare-kernel --verify-only` | none | off | Only check a module that is already installed. |
| `awg prepare-kernel --status-file` | none | none | Where to report progress; used by the agent. |
| `awg prepare-kernel --timeout` | none | `45m` | Hard limit of the run. |

`cleanup-net` and `version` take no flags. Everything else about a node (its address, country, DNS, timeouts, AmneziaWG backend, WARP) is set in the admin and reaches the agent over its connection.

## The panel's data directory

`/var/lib/mistgate` by default. The directory is 0700, and the panel creates every file in it readable by its owner only.

| Path | What it is | Secret |
|:--|:--|:--|
| `mistgate.db` | The SQLite database: settings and addresses, admins and sessions, nodes and their certificates, saved SSH access of nodes, profiles, groups, users, devices, credentials, traffic, events, alerts, rollouts and update schedules, backup settings, API tokens and the audit log. Secrets inside (the panel CA key, authenticator secrets, subscription tokens, device keys, WARP keys, SSH passwords, the backup bucket's secret key and the like) are encrypted with the master key; passwords are stored as argon2id hashes, API and enrollment tokens as SHA-256 hashes. | yes |
| `mistgate.db-wal`, `mistgate.db-shm` | SQLite's write-ahead log and its index. Part of the database: copy them together with it, or stop the panel first. | yes |
| `master.key` | 32 random bytes, mode 0600. Encrypts every stored secret (XChaCha20-Poly1305) and derives the user page passwords, which are never stored. The panel refuses to start when the file is readable by group or others. | the most sensitive file |
| `release.pub` | The installation's release public key (ed25519); it verifies the node bundles in `dist/`. The first panel build with a key writes it; a later build with the same key uses it, a build without a key keeps using it, and a build with another compiled-in key trusts no bundle until `mistgate release trust-key` replaces the file. Panel releases are verified only with the key compiled into the binary, never with this file. | no |
| `dist/` | The signed bundle for node updates: `manifest.json`, `manifest.sig` and `mistgate-node-linux-amd64` / `-arm64`. You copy it here, or the panel puts it here itself: every 10 minutes it looks at the latest stable GitHub release and installs a newer bundle that `release.pub` verifies (not while a rollout runs). The panel rescans the directory every minute. | no |
| `panel-update.request` | The panel release the owner confirmed (version and SHA-256), left for the update helper, which removes it. | no |
| `panel-update.new` | The new panel binary, downloaded by the update helper and checked against the signed panel manifest; removed after a successful update. | no |
| `.mistgate-backup-*/` | The work directory of a running backup: a snapshot of the database and the encrypted archive. Removed when the backup ends (one left by a crash goes at the next backup), and never part of a backup. | yes |
| `acme/` | Let's Encrypt account key and certificates, only with `--acme-domain`. Rebuilt by itself when lost. | yes |

The panel updater also writes next to the data directory and the binary: `<data-dir>.panel-update-backup.tar.gz` (for example `/var/lib/mistgate.panel-update-backup.tar.gz`, mode 0600) is the whole data directory, archived with the panel stopped just before the binary was replaced; a failed update puts it back, and only the latest one is kept. It holds `master.key` and the database, so protect it like the directory. The previous binary stays as `<binary>.prev` (for example `/usr/local/bin/mistgate.prev`), and a binary that was rolled back as `<binary>.failed`.

The database is migrated forward automatically whenever `serve`, `setup`, an `auth` command or `backup restore` opens it.

> **Warning:** losing `master.key` makes every encrypted secret unreadable, the panel CA's key and the saved SSH passwords of nodes included; losing the whole directory means enrolling every node again. Back it up as described in [Install the panel](../getting-started/install-panel.md), step 9, and set up [encrypted backups](../operations/backups.md).

## The node's state directory

`/var/lib/mistgate-node` by default, mode 0700, files 0600.

| Path | What it is |
|:--|:--|
| `identity.pem` | The node's private key and its certificate from the panel CA, in one file. Its presence means "enrolled". |
| `enroll-pending.pem` | The private key of an enrollment started with `--resume-key` (the SSH install), kept only until it succeeds. |
| `ca.pem` | The panel CA: the only certificate authority the agent trusts. |
| `agent.json` | The panel address, the secret TLS name and the node id. |
| `state.json` | The last applied state, so servers come back after a restart without the panel. It holds profile secrets (obfuscation passwords, server keys, the WARP key) and what the node needs to verify users (hashes of Hysteria2 tokens, AmneziaWG public keys and preshared keys), never a client's private key or raw token. |
| `certs/` | The certificates of the servers on the node: Let's Encrypt state and self-signed certificates. |
| `last_version` | The version that started last. |
| `update.*`, `awg-prepare.json` | Markers of a self-update in progress and the status of a kernel-module build, present only while they matter. |
