---
title: Command line
description: Every command of mistgate and mistgate-node, with its flags, environment variables, defaults and examples.
---

Mistgate has two binaries: `mistgate` is the panel, `mistgate-node` is the node agent. Both take a command and then flags. Flags can be written with one dash or two (`-data-dir` or `--data-dir`); this page uses two. `<command> -h` prints the flags of a command.

Many flags fall back to an environment variable. A flag given on the command line wins over the variable. Every flag and variable of `serve` and `setup` is also described, with the layout of the data directory, in [Configuration](configuration.md).

## mistgate

| Command | What it does |
|---|---|
| `serve` | Runs the panel. |
| `setup` | Creates the data directory, the master key and the database; prints the admin URL and a one-time setup link. |
| `auth turnstile off` | Switches the Cloudflare captcha off on the sign-in and setup pages. |
| `auth reset-login` | Gives an admin a new password and authenticator app, or adds a password login. |
| `mcp` | A local stdio proxy to the panel's MCP endpoint, for agent clients that cannot speak HTTP. |
| `release keygen` | Makes the owner's release key. |
| `release sign` | Signs node binaries into a release bundle. |
| `version` | Prints the version, the build time and the release key fingerprint. |

`mistgate` exits with 0 on success, 1 on an error (printed as `mistgate: <error>`) and 2 for an unknown command or a missing one.

### mistgate serve

Runs the panel in the foreground. Run it under systemd or another supervisor. Outside development mode it needs an installation made by `mistgate setup`.

```sh
mistgate serve --listen :443 --acme-domain panel.example.com
```

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--listen` | `MISTGATE_LISTEN` | `127.0.0.1:8080` | Address of the public listener: the decoy site, subscriptions, the admin (when it lives under a secret prefix or host) and the agent endpoint. |
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | Data directory. |
| `--tls-cert`, `--tls-key` | `MISTGATE_TLS_CERT`, `MISTGATE_TLS_KEY` | none | Your own certificate and key for the public listener. Given together. The certificate is reloaded when the file changes. |
| `--acme-domain` | `MISTGATE_ACME_DOMAIN` (comma-separated) | none | Get a Let's Encrypt certificate for this host name. Repeatable. The public listener must be reachable on port 443. Using it accepts the CA's terms of service. The certificates are kept in `<data-dir>/acme`. |
| `--acme-email` | `MISTGATE_ACME_EMAIL` | none | Contact address for Let's Encrypt. |
| `--acme-http` | `MISTGATE_ACME_HTTP` | `:80` | Listener for the ACME HTTP-01 challenge and the http → https redirect, used with `--acme-domain`. Empty switches it off. |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | the stored setting | Separate admin listener (plain HTTP). Only for an installation set up with `setup --admin-listen`; anything else is refused at start. |
| `--agent-listen` | `MISTGATE_AGENT_LISTEN` | none (`127.0.0.1:8082` with `--dev`) | A separate TLS listener for the node agents. Without it agents use the public listener with the secret TLS name, which needs TLS on it (`--tls-cert` or `--acme-domain`). |
| `--agent-addr` | `MISTGATE_AGENT_ADDR` | `--agent-listen` when it names a host, else the public URL's host and port (443 when none) | The `host:port` agents dial; it goes into the install command. |
| `--decoy-dir` | `MISTGATE_DECOY_DIR` | the built-in page | A directory with your own decoy site. `404.html` and `429.html` in it replace the built-in error pages. |
| `--trusted-proxy` | `MISTGATE_TRUSTED_PROXY` (comma-separated) | none | CIDR or IP of a reverse proxy whose `X-Forwarded-For` and `Forwarded` headers are believed. Repeatable. Without it the TCP peer is the client. |
| `--source-url` | `MISTGATE_SOURCE_URL` | `https://github.com/Mistgate/mistgate` | Where the source code of this build is published, linked next to the version in the admin (AGPL-3.0 section 13). A fork points it at its own repository; an empty value hides the link. |
| `--dev` | `MISTGATE_DEV` (`1`, `true`, `yes`, `on`) | off | Development mode, see below. |

**Development mode.** `--dev` uses `./.data`, serves plain HTTP with the decoy on `127.0.0.1:8080` and the admin on `127.0.0.1:8081`, puts the agent endpoint on `127.0.0.1:8082`, uses `localhost` as the WebAuthn relying party, needs no `setup`, and prints a one-time setup link (`http://localhost:8081/setup#…`, valid 30 minutes) while no admin exists. Never use it on a server.

### mistgate setup

Prepares a new installation: creates the data directory (mode 0700), the master key and the database, stores where the admin is reached, and prints how to get there. It is safe to run again: an existing configuration is kept, and a new setup link is issued only while no admin exists (earlier unused links stop working).

```sh
mistgate setup --public-url https://panel.example.com
```

```text
Data dir:   /var/lib/mistgate
Admin URL:  https://panel.example.com/<secret prefix>/
Setup link: https://panel.example.com/<secret prefix>/setup#<token>
The link works once and expires in 30 minutes. Open it in a browser and create your admin (a passkey, or a password with an authenticator code).
```

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` | Data directory (database, master key). |
| `--public-url` | `MISTGATE_PUBLIC_URL` | none | URL of the public (decoy) site, for example `https://panel.example.com`. Also the base of the subscription links. |
| `--admin-host` | `MISTGATE_ADMIN_HOST` | none | Serve the admin on this secret host name instead of a secret path prefix. |
| `--admin-listen` | `MISTGATE_ADMIN_LISTEN` | none | Serve the admin on a separate listener, for example `127.0.0.1:8081`. Cannot be combined with `--admin-host`. |
| `--rp-id` | `MISTGATE_RP_ID` | derived | WebAuthn relying party id. |
| `--rp-origins` | `MISTGATE_RP_ORIGINS` | derived | Comma-separated allowed WebAuthn origins. |

One of `--public-url`, `--admin-host` or `--admin-listen` is required. Where the admin ends up:

| Given | Admin URL | WebAuthn relying party |
|---|---|---|
| `--public-url` only | `<public url>/<24 random characters>/` | the public host |
| `--admin-host admin-k7q2.example.com` | `https://admin-k7q2.example.com/` (scheme and port of `--public-url` when given) | the admin host |
| `--admin-listen 127.0.0.1:8081` | `http://localhost:8081/` | `localhost` |

Setup also generates the secret TLS name of the agent endpoint and the secret prefix of the subscription links. These settings cannot be changed by running `setup` again.

### mistgate auth

Commands for the operator on the panel server. Both work with the panel stopped or running: it reads these settings on every request.

```text
mistgate auth turnstile off [--data-dir DIR]
mistgate auth reset-login [<login>] [--admin ID] [--qr-invert] [--data-dir DIR]
```

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--data-dir` | `MISTGATE_DATA_DIR` | `/var/lib/mistgate` (`./.data` with `--dev`) | Data directory. The command refuses a directory without a panel database. |
| `--dev` | `MISTGATE_DEV` | off | A development installation: data in `./.data`. |
| `--admin` | none | none | `reset-login` only: the admin id whose new password login this is. |
| `--qr-invert` | none | off | `reset-login` only: draw the QR code for a light terminal background. |

**`auth turnstile off`** switches the Cloudflare check off on the sign-in and setup pages; the keys stay stored. It is the way back in when Cloudflare is unreachable or the keys are wrong. Switch it on again in **Settings → Security**.

**`auth reset-login`** is the way back in after a lost phone:

- `mistgate auth reset-login` lists the admins of the panel with their id, name, role, login and number of passkeys.
- `mistgate auth reset-login <login>` gives that login a new password and a new authenticator app, lifts its lockouts and ends every session of that admin. Passkeys stay.
- `mistgate auth reset-login <new login> --admin <id>` gives an admin who has passkeys only a password login (`--admin` picks which admin when there are several).

It prints the login, the new password, and the authenticator key as a QR code drawn in the terminal, as text in groups of four, and as an `otpauth://` link. The password is shown only once. It needs the master key, so run it as the user that can read the data directory. Both commands are written to the audit log as "command line".

```sh
mistgate auth reset-login alice
```

### mistgate mcp

A local MCP server on stdin and stdout for agent clients that can only start a command. It forwards every message to the panel's MCP endpoint (the admin URL plus `mcp`) and decides nothing itself: the panel answers, with the token's profile.

```sh
mistgate mcp --url https://panel.example.com/<prefix>/ --token-file ~/.config/mistgate/token
```

| Flag | Environment | Meaning |
|---|---|---|
| `--url` | `MISTGATE_URL` | The admin URL that `mistgate setup` printed. Plain `http` is refused unless the host is `localhost` or a loopback address. The URL must not carry credentials, a query or a fragment. |
| `--token-file` | `MISTGATE_TOKEN_FILE` | A file whose first line is an API token (`tk1_…`). The token is read only from the file, never from the command line or the environment, and never printed. A warning is printed when others can read the file. |

The proxy stops when the panel refuses the token (expired, revoked or the wrong profile). stdout carries only protocol messages; everything else goes to stderr. See [MCP](mcp.md).

### mistgate release keygen

```sh
mistgate release keygen --out ~/mistgate-release.key
```

| Flag | Meaning |
|---|---|
| `--out` | File for the private key. It must not exist: an existing key is never overwritten. The file gets mode 0600. |

Prints the public key and its fingerprint. Put the public key into the build: `RELEASE_KEY=<public key> make build`. Keep the key file offline and back it up. See [Updates](../operations/updates.md).

### mistgate release sign

```text
mistgate release sign --key FILE --version V --built UNIX [--expires 30d] BINARY... --out DIR
```

| Flag | Default | Meaning |
|---|---|---|
| `--key` | none | The release private key file made by `release keygen`. |
| `--version` | none | Release version, for example the output of `git describe`. |
| `--built` | none | Unix time of the source commit (`git log -1 --format=%ct`). It orders releases and must match the build time stamped into the binaries. |
| `--expires` | `30d` | How long the manifest stays installable: days (`30d`) or a Go duration (`720h`). |
| `--out` | none | Directory for `manifest.json`, `manifest.sig` and the copies of the binaries. |

The binaries are named `<name>-<os>-<arch>`, for example `mistgate-node-linux-amd64`. Flags may come after the binaries. The command verifies what it wrote and prints every file with its size and the key fingerprint. Copy the directory to `<data-dir>/dist` on the panel.

```sh
mistgate release sign --key ~/mistgate-release.key \
  --version "$(git describe --tags --always)" \
  --built "$(git log -1 --format=%ct)" --expires 30d \
  bin/mistgate-node-linux-amd64 bin/mistgate-node-linux-arm64 --out dist/
```

### mistgate version

Prints `mistgate <version>`, then `built <time>` when the binary was built with a build time, then `release key <fingerprint>`, or `release key: none (unsigned build, nodes are updated by hand)`.

## mistgate-node

| Command | What it does |
|---|---|
| `enroll` | Exchanges a one-time token for a node certificate. |
| `install` | Writes the hardened systemd unit, enables and starts it. |
| `run` | Runs the agent in the foreground. |
| `cleanup-net` | Removes the agent's tunnel interfaces, WARP routes and nftables tables. |
| `awg prepare-kernel` | Installs the AmneziaWG kernel module. |
| `version` | Prints the version, the build time and the release key fingerprint. |

`enroll` and `run` take `--state-dir`; give them the same directory. Exit codes: 0 success, 1 an error, 2 bad usage, 78 from `run` when the node is not enrolled (systemd does not restart the agent then).

The panel shows the whole install command when you add a node:

```sh
chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni <secret name> \
  --ca-sha256 <fingerprint> --token <one-time token> && /root/mistgate-node install
```

### mistgate-node enroll

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--panel` | `MISTGATE_PANEL` | none | Panel address, `host:port`. |
| `--sni` | `MISTGATE_AGENT_SNI` | none | The secret TLS name of the panel's agent endpoint. |
| `--ca-sha256` | `MISTGATE_CA_SHA256` | none | SHA-256 fingerprint of the panel's CA certificate (64 hex digits). The agent trusts the panel only by it. |
| `--token` | `MISTGATE_ENROLL_TOKEN` | none | The one-time enrollment token. The variable keeps it out of the process list. |
| `--state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | State directory. |
| `--force` | none | off | Replace an existing identity. |

The first four are required. A token works once, within the time the panel showed (an hour by default). On success it prints the node id and the next step:

```text
enrolled as nod_… with panel.example.com:443; state in /var/lib/mistgate-node
next: mistgate-node install   (or, in the foreground: mistgate-node run)
```

### mistgate-node install

Linux only, as root, after `enroll`.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | State directory; must be the one `enroll` used. |
| `--bin` | none | `/usr/local/bin/mistgate-node` | Where the binary lives. The running binary is copied there if it is elsewhere. |
| `--no-start` | none | off | Write and enable the unit, but do not start it. |

It writes `/etc/systemd/system/mistgate-node.service`, then runs `systemctl daemon-reload`, `enable` and `restart`. The unit:

- runs the agent as root with only two capabilities (network administration and binding low ports) and a read-only system, except the state directory, the binary's directory (for self-update), `/etc/sysctl.d` and `/etc/systemd/journald.conf.d`;
- sizes its memory limits from the server's RAM;
- restarts the agent 5 seconds after a failure, but not after exit code 78;
- before every start, puts the previous binary back after a crash loop of a fresh update; after every stop, runs `cleanup-net`.

Running `install` again with a new binary is how a node is updated by hand (see [Updates](../operations/updates.md)). Paths may contain only letters, digits and `_ . / -`.

### mistgate-node run

Runs the agent in the foreground: what the unit does, and a way to try the agent without systemd.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--state-dir` | `MISTGATE_NODE_STATE_DIR` | `/var/lib/mistgate-node` | State directory. |
| `--log-level` | `MISTGATE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `--log-format` | `MISTGATE_LOG_FORMAT` | `text` | `text` or `json`. Logs go to stderr. |

It exits with 78 when the state directory holds no identity, and with 0 when the panel retired the node.

### mistgate-node cleanup-net

Takes no arguments. Removes the tunnel interfaces of the agent, its tunnel nftables table and, when present, the WARP device, routes, rules and table. It needs no state and no panel. The unit runs it after every stop; the agent creates everything again when it starts.

### mistgate-node awg prepare-kernel

Puts the AmneziaWG kernel module on the node. Linux only, as root. The default userspace backend needs no module.

```sh
mistgate-node awg prepare-kernel          # prints the plan, runs nothing
mistgate-node awg prepare-kernel --yes    # runs it
```

| Flag | Default | Meaning |
|---|---|---|
| `--yes` | off | Run the commands. Without it the plan is only printed. |
| `--verify-only` | off | Only check a module that is already installed (loaded, the right interface version, loaded at boot). |
| `--status-file` | none | Write progress and the result to this file (used by the agent when the panel asks it to prepare the module). |
| `--timeout` | `15m` | Hard limit of the run. |

It installs packages with apt (non-interactive, waiting for another package manager's lock). Supported: Debian and Ubuntu, not a container, without Secure Boot, with systemd. See [AmneziaWG](../guide/amneziawg.md).

### mistgate-node version

Prints `mistgate-node <version>`, `built <unix time>`, and `release key <fingerprint>` or `release key none (unsigned build: update by hand)`.
