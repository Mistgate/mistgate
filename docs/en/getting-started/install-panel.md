---
title: Install the panel
description: Download or build Mistgate, prepare the panel with mistgate setup, run it under systemd and sign in for the first time.
---

This page takes a fresh Linux server to a running panel with an owner account. The examples use `panel.example.com`; check [Requirements](requirements.md) first. Commands on the panel server run as root. An AI agent with SSH access to the server can do the same steps for you: see the install prompt in the [AI agent guide](ai-agents.md).

## 1. Get the binary

The panel is one static Linux binary for amd64 or arm64, with the admin web app built in. Use the official release unless you have a reason to build your own.

### From GitHub Releases (recommended)

Every release on [GitHub Releases](https://github.com/Mistgate/mistgate/releases/latest) carries the panel (`mistgate-linux-amd64`, `mistgate-linux-arm64`), the node agent (`mistgate-node-linux-amd64`, `mistgate-node-linux-arm64`), `SHA256SUMS`, `BUILDINFO` and two signed manifests: `manifest.json` with `manifest.sig` for the node agents and `panel-manifest.json` with `panel-manifest.sig` for the panel. The official binaries carry the project's release key, so a panel installed from them downloads the signed node agents by itself and can update itself from later releases (see [Updates](../operations/updates.md)).

On the panel server, as root (`uname -m` prints `x86_64` for amd64 and `aarch64` for arm64):

```sh
ARCH=amd64    # arm64 on an ARM server
cd /root
for f in "mistgate-linux-$ARCH" SHA256SUMS panel-manifest.json; do
  curl -fsSLO "https://github.com/Mistgate/mistgate/releases/latest/download/$f"
done
sha256sum --check --ignore-missing SHA256SUMS
grep -q "$(sha256sum "mistgate-linux-$ARCH" | cut -d' ' -f1)" panel-manifest.json && echo "listed in panel-manifest.json"
install -m 0755 "mistgate-linux-$ARCH" /usr/local/bin/mistgate
mistgate version
```

What these checks prove, and what they do not:

- `sha256sum --check` proves the file arrived intact and matches the checksums published with the release. `SHA256SUMS` is written by the release workflow and is not signed, so it does not prove who built the binary.
- The same SHA-256 is listed in `panel-manifest.json`, which the maintainer signs offline with the release key after rebuilding every binary from the tag. Mistgate has no command that checks this signature for you, and common tools cannot check its Ed25519ctx signature, so the `grep` is a consistency check only.
- From then on the panel checks signatures itself: it installs a panel release, and hands a node bundle to the agents, only when the signature verifies with the release key compiled into it. `mistgate version` prints the version and that key's fingerprint.

### From source

On a build machine with the tools listed in [Requirements](requirements.md):

```sh
git clone https://github.com/Mistgate/mistgate.git
cd mistgate
make build
scp bin/mistgate-linux-amd64 root@panel.example.com:/root/
ssh root@panel.example.com 'install -m 0755 /root/mistgate-linux-amd64 /usr/local/bin/mistgate'
```

`bin/` also holds the node agents (`mistgate-node-linux-amd64`, `mistgate-node-linux-arm64`); keep them for the [manual node install](add-node.md).

> **Warning:** a build without `RELEASE_KEY` has no release key: its node agents never update themselves, the panel cannot install panel releases, and the SSH installation has no trusted agent bundle to install. A build with your own key trusts only releases you sign. Make the key before the first build: see [Releases and signing](../operations/releases.md).

## 2. Choose how the admin is reached

The public address shows a decoy site to everyone. The admin is reached in one of three ways, chosen once, when you run `mistgate setup`:

| Mode | `setup` flags | Admin address | When to pick it |
|:--|:--|:--|:--|
| Secret path prefix (default) | `--public-url https://panel.example.com` | `https://panel.example.com/<secret>/`, where the secret is 24 random characters | The simplest: one host name, one certificate. |
| Secret host | `--public-url https://panel.example.com --admin-host <secret host>` | `https://<secret host>/` | You want the admin on a host name nobody can guess. It needs a DNS record and a certificate that covers it. |
| Separate listener | `--public-url https://panel.example.com --admin-listen 127.0.0.1:8081` | `http://localhost:8081/`, through an SSH tunnel | The admin never appears on the public port at all. |

- Requests that do not match the admin (a wrong prefix, an unknown host, an unknown path) all get the decoy site, so a prober cannot tell a near miss from a random guess.
- Always pass `--public-url`. It is the base of every subscription link and the address that node install commands carry. Without it the panel hands out no subscription links, and adding a node fails unless `serve` gets `--agent-addr`.
- `--admin-host` and `--admin-listen` cannot be combined.
- The separate admin listener speaks plain HTTP. Bind it to loopback and reach it through SSH:

  ```sh
  ssh -N -L 8081:127.0.0.1:8081 root@panel.example.com
  ```

  Then open `http://localhost:8081/` in your browser. Use the same port on your side: passkeys are bound to the address `http://localhost:<port>` that setup stored.
- `--rp-id` and `--rp-origins` override the WebAuthn settings that setup derives from the admin address. You rarely need them.

> **Warning:** setup stores these addresses once. Running it again keeps them, and Mistgate has no command to change them later. Choose before you go on; starting over means a new data directory and enrolling every node again.

## 3. Run setup

```sh
mistgate setup --public-url https://panel.example.com
```

It prints:

```text
Data dir:   /var/lib/mistgate
Admin URL:  https://panel.example.com/<secret>/
Setup link: https://panel.example.com/<secret>/setup#<token>
The link works once and expires in 30 minutes. Open it in a browser and create your admin (a passkey, or a password with an authenticator code).
```

What setup does:

1. Creates the data directory `/var/lib/mistgate` with mode 0700 (`--data-dir` changes the path).
2. Creates `master.key`, 32 random bytes with mode 0600. It encrypts every secret the panel stores.
3. Creates the database `mistgate.db` and applies the schema.
4. Stores the addresses of the chosen mode and generates two more secrets: the TLS name the node agents use to reach the panel, and the secret path prefix of the subscription links.
5. Prints a one-time setup link, valid for 30 minutes.

Keep the admin URL to yourself. Setup is safe to run again: it keeps the existing configuration and, as long as no admin exists, prints a fresh setup link.

## 4. Choose the certificate

| Option | Flags of `serve` | What happens |
|:--|:--|:--|
| Let's Encrypt | `--acme-domain panel.example.com` | The panel gets and renews the certificate itself (TLS-ALPN-01 on the public listener, so it must be reachable on port 443). A second listener on `:80` answers HTTP-01 and redirects plain HTTP to HTTPS (`--acme-http`; an empty value turns it off). Certificates are cached in `<data-dir>/acme`. `--acme-email` adds a contact address. Using it accepts the Let's Encrypt terms of service. |
| Your own certificate | `--tls-cert /path/fullchain.pem --tls-key /path/privkey.pem` | The panel serves your PEM files and reads them again when the certificate file changes (checked at most every 30 seconds), so a renewal by another tool needs no restart. |
| Both | both sets of flags | Names listed in `--acme-domain` get Let's Encrypt certificates; every other name gets your certificate. Useful for a secret admin host covered by a wildcard certificate of your own. |

`--acme-domain` takes plain host names only, no wildcards. It can be repeated for several names.

> **Note:** a Let's Encrypt certificate for a secret admin host puts that name into public Certificate Transparency logs. To keep it secret, cover it with a wildcard certificate of your own.

## 5. The decoy site

Without anything else the panel shows a built-in "Coming soon" page. Every installation ships the same page, so it is recognisable; your own site is better. Put static files in a directory and pass `--decoy-dir`:

- `/` and every directory serve their `index.html`.
- `404.html` and `429.html`, when present, replace the built-in "not found" and "too many requests" pages. Every unknown path, wrong admin prefix and unknown subscription link gets the same 404.
- `robots.txt`, when absent, is a built-in one that turns away known AI crawlers.
- Only GET and HEAD are served. Files and directories whose name starts with a dot are never served, and responses carry no Last-Modified or ETag headers.

The panel only reads this directory. With the systemd unit below, keep it outside `/home` and `/root` (for example in `/srv/mistgate-decoy`). [Your own decoy site](../guide/decoy-site.md) says what a good one looks like and has a prompt that has an AI agent invent a unique site.

## 6. Try it in the foreground

```sh
mistgate serve --listen :443 --acme-domain panel.example.com
```

`--listen` defaults to `127.0.0.1:8080`, so pass `--listen :443` on a real server. The log goes to stderr; look for `listening` lines for the public listener (and `acme-http` for port 80). Stop it with Ctrl+C once it works.

## 7. Run it under systemd

Save this as `/etc/systemd/system/mistgate.service` and change `ExecStart` to the flags you chose:

```ini
[Unit]
Description=Mistgate panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mistgate serve --listen :443 --acme-domain panel.example.com
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

# The panel writes only its data directory and only needs to bind ports 443 and 80.
NoNewPrivileges=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
ProtectSystem=strict
ReadWritePaths=/var/lib/mistgate
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectClock=yes
ProtectControlGroups=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectHostname=yes
LockPersonality=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

[Install]
WantedBy=multi-user.target
```

```sh
systemctl daemon-reload
systemctl enable --now mistgate
journalctl -u mistgate -f
```

Why it looks like this:

- `mistgate setup` ran as root, so the data directory belongs to root, and the service runs as root too. Every capability except binding low ports is dropped, and only `/var/lib/mistgate` is writable. If you use another `--data-dir`, change `ReadWritePaths`.
- The panel stops gracefully on SIGTERM. With `Restart=on-failure` systemd restarts it when a listener fails, not when you stop it.
- Every flag has a `MISTGATE_*` environment variable, so you can move the flags to `Environment=` lines or an `EnvironmentFile=`. See [Configuration](../reference/configuration.md).
- With `--decoy-dir`, `--tls-cert` or `--tls-key`, the files only need to be readable.

### Behind a reverse proxy

If another web server must own port 443 on this host, Mistgate can run behind it:

- Run the public listener without TLS on loopback (`--listen 127.0.0.1:8080`, no `--acme-domain`, no `--tls-cert`) and let the proxy terminate TLS for `panel.example.com`, passing the Host header and the path through unchanged.
- Add `--trusted-proxy 127.0.0.1` so that rate limits, sessions and the audit log see the real client address from `X-Forwarded-For` or `Forwarded`.
- Node agents need TLS from the panel itself. Give them a listener of their own and put its address into the install commands: `--agent-listen :8443 --agent-addr panel.example.com:8443`. The proxy must not touch that port.

The agent port is then a port of its own that answers only the secret TLS name; on the shared port 443 it hides behind the decoy site. Prefer the plain setup above when you can.

## 8. Sign in for the first time

Open the setup link from step 3 in a browser, the whole link including the part after `#`.

1. **Language.** Pick English or Russian. You can change it later.
2. **Create the admin.** The LOGIN field (it starts as `admin`) is your login and the name the panel shows. Then:
   - **Create passkey**: the browser asks for a fingerprint, a face, a PIN or a security key. The key stays on your device. This is the recommended way.
   - **password + authenticator code**: a password of at least 12 characters, then scan the QR code with an authenticator app (Google Authenticator, 1Password, Aegis and the like), type the 6-digit code and press **Create admin**.
3. **Done.** Choose **Add your first node** or **Later, let me look around first**.

The first admin is the owner. The setup wizard also creates an empty group "Everyone" for your first users.

- A login is 3 to 64 characters: a–z, 0–9 and `. _ @ -`.
- The link is spent only when the admin is created. If it expired or something went wrong, run `mistgate setup` again for a new link.
- A passkey works only on the admin address that setup stored. "The browser refused the passkey for this address" means the page was opened under another address.

Later you sign in at the admin URL with **Sign in with passkey**, or with **another way**: login, password and code. Five wrong passwords within an hour lock that login for 15 minutes. Lost your phone or your passkey: [Security](../operations/security.md) explains `mistgate auth reset-login`.

## 9. Back up right away

Everything the panel knows lives in the data directory:

| File | What it is |
|:--|:--|
| `mistgate.db`, `mistgate.db-wal`, `mistgate.db-shm` | The database: admins, nodes, profiles, users, devices, traffic, events, the audit log. Secrets inside are encrypted with the master key. |
| `master.key` | The key that encrypts the stored secrets, including the key of the panel CA that every node trusts. |
| `acme/` | Let's Encrypt account and certificates (with `--acme-domain` only). |

Make a copy now, with the panel stopped so the database is consistent:

```sh
systemctl stop mistgate
tar czf mistgate-backup-$(date +%F).tgz -C /var/lib mistgate
systemctl start mistgate
```

> **Warning:** the copy holds the master key: whoever has it can read every secret of the panel. Keep it encrypted and off the server. Without the data directory every node has to be enrolled again and every user, link and key is gone.

For regular copies, turn on [encrypted backups](../operations/backups.md) in **Settings → Backups**: the panel encrypts a consistent snapshot to your offline recovery key and uploads it to your Cloudflare R2 bucket on a schedule. If you built the panel with your own release key, keep that key file offline and backed up as well.

## Next

[Add a node](add-node.md): automatically over SSH or with a command you run yourself.
