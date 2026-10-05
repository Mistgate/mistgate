---
title: Security
description: Who can do what in the admin, how sign-in, step-up and sessions work, how the panel hides behind a decoy site, what the data directory holds and how to get back in.
---

This page covers the protection of the admin: roles, sign-in, step-up re-authentication, sessions, the captcha, the audit log, the decoy site in front of everything, saved SSH access, the data directory, and how to recover access from the panel server. To report a vulnerability, see the end of this page.

## Admin roles

The admin knows three roles. Every request is checked against the role per procedure; a procedure with no rule needs the owner.

| Role | May |
|---|---|
| Owner | Everything: nodes (add, install over SSH, retire, logs, saved SSH access), profiles and where they run, DNS presets and which of them a node offers to people, doctor fixes, node and panel updates, WARP, backups, API tokens and approvals, the audit log, security and brand settings. |
| Helper | Day-to-day work: users (create, change, enable and disable, extend, reset traffic, delete, devices, the subscription link), groups, subscription settings, resetting a person's DNS choices by server, node settings and restarting profiles on a node, muting alerts, Check now, running the doctor, accepting doctor warnings. |
| Read-only | Reads: the overview, nodes, profiles (secrets masked), groups, DNS presets, subscription settings, users, events, alerts, checks, doctor reports, the Updates page. And their own account: passkeys, password, sessions. |

> **Note:** Today the panel has one admin, the owner created at setup: adding a helper or a read-only admin is not possible yet. The roles already matter for [API tokens](../reference/api.md), whose profiles act as these roles.

## Signing in

The owner is created with the one-time link that `mistgate setup` prints (valid 30 minutes; running `setup` again while no admin exists issues a new link and invalidates the old one). The owner chooses one of two ways to sign in, and can add the other later in **Settings → Security**.

**Passkey.** Sign in with a fingerprint, a face or a security key. No login field: the browser offers the passkeys it holds for this panel. User verification (PIN or biometrics) is required every time. A passkey is bound to the admin's address (the WebAuthn relying party set at setup), so it works only there.

**Password and authenticator code.** A login (3 to 64 characters of `a-z 0-9 . _ @ -`), a password (at least 12 characters, at most 256 bytes) and a 6-digit code from an authenticator app (time-based, 30-second steps). Passwords are stored as argon2id hashes; the authenticator secret is encrypted with the master key, so a panel without a master key cannot offer this way. A code works once.

Limits on password sign-in:

- 5 failed attempts for a login within an hour lock that login for 15 minutes, even for the right credentials.
- 10 failed attempts from one address (an IPv4 address, or an IPv6 /64) within an hour, for any logins, lock that address for 15 minutes.
- The answer is the same for a login that does not exist, so failures do not reveal which logins exist.
- Sign-in, setup and step-up calls are also rate limited per address (a burst of 10, then one request every 3 seconds).
- A lockout is written to the audit log with the address and the time. The panel sends no notification about it.

In **Settings → Security** an admin manages their own sign-in:

- Passkeys: add one (needs step-up) or remove one (needs step-up; the last way to sign in cannot be removed).
- **Password and code**: add it as a spare way in next to the passkeys, **Change password** (needs step-up and the current password; ends the other sessions), **Re-bind the app** for a new phone (needs step-up; ends the other sessions).

## Step-up re-authentication

A stolen session cookie must not be enough to plant a passkey, remove the owner's, or change what runs on the nodes. These actions need a sign-in proof from the last 5 minutes:

- adding or removing a passkey;
- ending another session, or all other sessions;
- changing the password, binding an authenticator app;
- changing the captcha settings;
- starting, pausing, resuming or cancelling a rollout, rolling a node back, reading the release folder again, installing a panel update, scheduling a node update or cancelling the schedule, changing the update time zone;
- checking an SSH login and starting or retrying an installation over SSH;
- changing, revealing or forgetting a node's saved SSH password;
- registering, importing or deleting a WARP account;
- saving the backup settings, testing the backup storage, creating a backup now;
- creating or revoking an API token, approving an agent's change.

Signing in counts as a proof, so "sign in, then add a passkey" asks nothing more. Otherwise the admin shows **Confirm it is you**: use the passkey or type an authenticator code. A code for step-up must be newer than the one used to sign in (wait for the next 30-second step). Five wrong proofs lock step-up for 15 minutes. An API token never passes step-up; see [API](../reference/api.md).

## Sessions

- The session cookie is `__Host-sid`: HTTPS only, not readable by scripts, sent only to the panel itself (SameSite=Strict).
- A session ends after 12 hours without activity, and after 30 days at most.
- **Settings → Sessions** lists the admin's own sessions with the address and browser. **End** ends one (ending another than the current one needs step-up); **End all others** ends the rest (needs step-up).
- Changing the password, re-binding the app and `mistgate auth reset-login` end other sessions too.

## Cloudflare Turnstile

The owner can put a Cloudflare Turnstile check in front of the sign-in and setup pages: **Settings → Security → Cloudflare check at sign-in**.

1. Create a Turnstile widget in the Cloudflare dashboard for the panel's domain.
2. Enter the **Site key** and the **Secret key**. The secret key is write-only: the panel stores it encrypted with the master key and never shows it again.
3. Click **Check and switch on** and pass the check that appears. The panel asks Cloudflare whether the secret key fits the token the widget produced; nothing is saved before that.

When it is on, every start of a sign-in (passkey or password) and of the setup needs a fresh token from the widget; the second step of a passkey sign-in does not. If Cloudflare cannot be reached, sign-in is refused. The admin pages load Cloudflare's script only while the check is on.

If the check keeps everyone out (Cloudflare unreachable, wrong keys), switch it off on the panel server; the keys stay stored:

```sh
mistgate auth turnstile off
```

## The audit log

**Settings → Audit** (owner only) lists who did what and from where, newest first.

- **Audit source**: All sources, Panel, Bot, MCP, API.
- **What to show**: Everything, Sign-ins (setup, sign-in and out, lockouts, step-up, captcha, passkeys, passwords, sessions), Changes (users, groups, profiles, devices, DNS presets, nodes with their SSH installations and saved SSH access, WARP, updates, doctor fixes, tokens, approvals, settings), Failures (failed, refused or locked attempts of any kind). Backup actions are listed under Everything (and, when they fail, under Failures).
- Each row has the actor, the action, its parameters, the result and the client address. Parameters never contain secrets.
- A token is shown as "API token <name>" or "MCP token <name>". Successful reads by a token are written at most once a minute per procedure; changes and refusals every time.
- Actions from the command line (`mistgate auth ...`) appear as "command line", work of the panel itself (a sign-in lockout, an automatic WARP re-registration) as "the panel itself", and a person working on their own user page as "a user on their page". Among the DNS rows: `node_dns_options` (the owner changes the presets a node offers), `page_dns_choice` (a person picks a DNS for a server on their page: the person's name, the node's name and the preset, no link or address) and `user_dns_choices_reset` (an admin resets a person's choices). See [DNS](../guide/dns.md).

The panel does not prune the audit log.

## The decoy site and the hidden admin

Everything on the public listener that is not the admin, a subscription or the agent endpoint answers with the decoy site.

- **The decoy.** By default a built-in "Coming soon" page on `/`, a `robots.txt` that shuts out known AI crawlers, and the same 404 page for every other path and every method other than GET and HEAD. Every installation ships the same built-in page, so it is recognisable: serve your own site with `serve --decoy-dir <dir>`. Files are served without `Last-Modified` or `ETag`; names starting with a dot are not served; a `404.html` and a `429.html` in the directory replace the built-in error pages.
- **The admin** is reachable in one of three ways, chosen at `mistgate setup`:
  - a secret path prefix (the default): `https://panel.example.com/<24 random characters>/`;
  - a secret host name: `setup --admin-host <name>`;
  - a separate listener: `setup --admin-listen 127.0.0.1:8081`. It serves plain HTTP, so bind it to loopback and reach it through an SSH tunnel.

  A wrong prefix or another host name gets the decoy site, an unusual path form its 404. The admin never answers with a redirect that could reveal the prefix.
- **Subscriptions** live under another secret prefix. An unknown subscription token gets the same 404 as an unknown path, and every 404 takes at least 8 ms, so a guessed token cannot be told from a wrong path by content, headers or timing.
- **The agent endpoint** is selected by a secret TLS server name: a random label under the panel's domain that needs no DNS record. The panel answers it with a certificate from its own CA, which never reaches public certificate logs and does not name the product. A client without the name sees the decoy.
- **Rate limits** per client network (an IPv4 address or an IPv6 /64): 10 requests per second (burst 60) on the public site and subscriptions, 30 per second (burst 200) on the admin, 5 per second (burst 30) on the agent endpoint. Over the limit the answer is a 429 page in the decoy's style with `Retry-After`.
- **Admin pages** are sent with a strict Content-Security-Policy, no framing, no referrer, `noindex` and `no-store`, and HSTS over HTTPS.

**Settings → Domains** shows the owner the admin address and the base of the subscription links. Both carry a secret part: do not publish them. Behind a reverse proxy, list it with `serve --trusted-proxy` so the panel sees the real client addresses (rate limits, sessions and the audit log depend on them). See [Configuration](../reference/configuration.md) for every flag.

## Saved SSH access

A node installed over SSH keeps its SSH login in the panel: the address, the port, the login, the password and the pinned host key. The password is encrypted with the master key. Only the owner sees it, under **SSH access** in the node's **Settings**. How to use it: "Saved SSH access" in [Install a node over SSH](../getting-started/ssh-install.md).

- The panel sends the password only after the server shows the host key the owner confirmed. A changed key never gets it.
- **Reveal password** needs step-up and is written to the audit log. No API read, MCP tool or log returns the password.
- An AI agent never sees the password. For an installation through MCP the owner types it on the approval screen. An agent can ask to change it, as a plan the owner approves; the panel then generates the new password, and only the panel knows it until the owner reveals it.
- A password change (by the owner in the install manager, or the generated one) is checked with a fresh SSH login before the panel stores the new password.
- Retiring a node keeps its saved access, because the password may be one only the panel knows. **Forget saved access** then deletes it for good; it needs step-up.

## The data directory

Everything the panel knows is in its data directory, `/var/lib/mistgate` by default (mode 0700):

| Path | What it is |
|---|---|
| `master.key` | 32 random bytes, mode 0600. It encrypts every stored secret (XChaCha20-Poly1305). The panel can also read it from a systemd credential (`$CREDENTIALS_DIRECTORY/master.key`). |
| `mistgate.db` (with `-wal` and `-shm`) | The SQLite database, mode 0600: users, nodes, profiles, sessions, the audit log, and the panel's CA, whose private key is encrypted with the master key. |
| `acme/` | Let's Encrypt certificates, with `serve --acme-domain`. |
| `dist/` | The node-agent release bundle (see [Updates](updates.md)). |
| `release.pub` | The installation's release public key: the first build with a release key stores it, and node bundles are checked against it. A panel binary with another compiled-in key trusts nothing until `mistgate release trust-key`. |

The stored secrets include profile secrets and device keys, the authenticator secrets, the Turnstile secret key, WARP keys, saved SSH passwords and the R2 secret access key of the backups. The users' page passwords are not stored: they are derived from the master key. A new master key therefore changes every page password and makes every stored secret unreadable. Losing the data directory means enrolling every node again (the nodes trust the panel's CA) and giving users new links.

> **Warning:** A lost `master.key` cannot be replaced by a new one: put it back from a backup. `mistgate serve` and `mistgate setup` refuse to make a new key next to an existing database.

### Backups

Owner-only encrypted backups to Cloudflare R2 are available under **Settings → Backups**. They are off until configured. The panel encrypts every backup to your age recovery recipient before the upload, so the R2 bucket never holds a readable copy; the private recovery identity stays offline with you. Read [Encrypted panel backups](backups.md) before enabling them; it covers token permissions, the schedule, retention and restore into a fresh data directory.

A copy by hand needs no R2. Make it with the panel stopped:

```sh
systemctl stop mistgate     # the name of your unit
tar czf mistgate-backup-$(date +%F).tgz -C /var/lib mistgate
systemctl start mistgate
```

> **Warning:** The copy holds `master.key`: whoever has it can read every secret of the panel. Keep it encrypted and off the panel server. If systemd gives the panel its key as a credential, the key is not in the directory: keep that credential with the copy, or the copy cannot be read.

To restore, stop the panel, put the directory back in place (owned by the user that runs the panel, mode 0700) and start it.

## Recovering access

All of these run on the panel server, as the user that can read the data directory (normally root). They work with the panel running or stopped. Add `--data-dir <dir>` when the data is not in `/var/lib/mistgate`. `reset-login` needs the master key: if it is not in the data directory but given to the panel as a systemd credential, set `CREDENTIALS_DIRECTORY` to the directory that holds `master.key`.

**A lost phone (password sign-in).**

```sh
mistgate auth reset-login <login>
```

It prints a new password and a new authenticator key: as a QR code drawn in the terminal (for a dark background; add `--qr-invert` for a light one), as text in groups of four, and as an `otpauth://` link. It lifts the lockouts of the login and of step-up and ends every session of that admin. Passkeys stay. Sign in with the login, the password and a code, then set your own password in **Settings → Security**. The password is shown only once and is not stored anywhere in plain text.

**No passkey left (an admin with passkeys only).** Without arguments the command lists the admins; give the admin a password login by id:

```sh
mistgate auth reset-login
mistgate auth reset-login <new login> --admin <admin id>
```

Then sign in with the password and remove the lost passkey in **Settings → Security**.

**The captcha keeps you out:** `mistgate auth turnstile off`.

**The admin address is lost:** run `mistgate setup` again. It keeps the existing settings and prints the admin URL.

## What API tokens and MCP can never see

API tokens and the MCP server reach the panel through the same procedures as the admin, but a token is allowed only a fixed list of them, and none of those returns a credential:

- subscription links, user page passwords, device keys and device configurations;
- WARP accounts and keys, profile secrets (they come back masked);
- saved SSH passwords (a token sees only the connection details of a node's saved access: address, port, login, host key);
- backups and their settings;
- tokens, approvals, sessions, passkeys and security settings.

A token never passes step-up, so procedures that need it are closed to tokens, except through an MCP plan the owner approved. Every token call is in the audit log. Give each script or agent the narrowest profile that works. Details: [API](../reference/api.md), [MCP](../reference/mcp.md).

## Reporting a vulnerability

Report it privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Do not open a public issue for it. `SECURITY.md` in the repository lists what counts and what is out of scope.
