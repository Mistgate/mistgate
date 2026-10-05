---
title: Architecture
description: The components of Mistgate, how the panel and the node agents talk, where the data lives, and what each part can and cannot do if it falls into the wrong hands.
---

Mistgate is two Go programs: the panel (`mistgate`) and the node agent (`mistgate-node`). This page describes their parts and boundaries, the protocol between them, the panel's HTTP surface and storage, the protocol plugin contract, and the security model in brief. For the concepts themselves start with the [Overview](../getting-started/overview.md).

## Components

```text
                       browsers, subscription apps, scripts, AI agents
                                         |
                                     TCP 443
                                         |
  +--------------------------------------v---------------------------------------------+
  | panel: mistgate serve                                                              |
  |                                                                                    |
  |  public listener --+-- decoy site (everything that matches nothing else)           |
  |                    +-- <secret prefix>/...  subscriptions, user pages, self-service |
  |                    +-- admin: secret prefix or secret host (or its own listener)   |
  |                    |     /api  Connect API (session cookie or API token)            |
  |                    |     /mcp  MCP server (API token)                              |
  |                    +-- secret TLS name -> agent endpoint (mutual TLS)              |
  |                                                                                    |
  |  modules: auth, fleet, access, protocols, subs, dns, health, update, warp, mcp ... |
  |  store: SQLite + migrations        vault: master key, encrypted secrets            |
  +--------------------------------------^---------------------------------------------+
                                         |  one stream per node, opened by the node
  +--------------------------------------+---------------------------------------------+
  | node: mistgate-node run                                                            |
  |  agent core: connection, worker, reliable outbox, reconcile                        |
  |  engines: hysteria2 (in-process core), awg (userspace or kernel module)            |
  |  host: nftables, sysctl, journald, tunnels; WARP manager; certificates; doctor;    |
  |        self-update                                                                 |
  +------------------------------------------------------------------------------------+
                                         |
                           VPN traffic from the apps, out to the internet
```

### Panel modules

The panel is one process. `cmd/mistgate` wires the modules together; they know each other only through small interfaces.

| Module (`internal/panel/...`) | Responsibility |
|:--|:--|
| `httpserver` | The listeners, the routing between decoy, admin, public mounts and the agent endpoint, rate limits, TLS (static files or Let's Encrypt), security headers. |
| `auth` | Admins and roles, passkeys (WebAuthn), password plus authenticator code, sessions, step-up, API tokens, the owner's approvals, the Turnstile captcha, the audit log. |
| `instance` | The brand (name, logo, accent, language) and the read-only addresses shown in **Settings → Domains**. |
| `fleet` | The panel CA, node enrollment, the agent stream, liveness, ingestion of stats and events, desired-state revisions and deltas, commands to nodes, the admin's node and overview APIs. |
| `access` | Profiles, servers on nodes, groups, users, devices and credentials; the rule of who gets what; subscription views. |
| `protocols` | The registry of protocol plugins (Hysteria2, AmneziaWG) and their schema-driven settings. |
| `subs`, `subsettings`, `dns` | The public subscription endpoint and the user page, the subscription settings and User-Agent rules, DNS presets. |
| `health` | Client-eye checks, alerts, the node doctor's reports and fixes, data retention. |
| `update` | The signed release bundle in `<data-dir>/dist` and its download from GitHub, staged rollouts and scheduled updates of node agents, the panel's own update. |
| `provision` | Installing nodes over SSH (the durable install job and its wizard) and the saved SSH access of nodes with its password rotation. |
| `backup` | Encrypted panel backups to Cloudflare R2, and the offline side behind `mistgate backup`. |
| `warp` | Cloudflare WARP accounts of the nodes. |
| `mcp` | The MCP server on top of the same admin API. |
| `store`, `vault`, `pagepass` | The database, encryption with the master key, user page passwords derived from the master key. |

### The node agent

| Part (`internal/node/...`) | Responsibility |
|:--|:--|
| `agent` | Enrollment, the one stream to the panel with reconnects, a single worker that applies every change to engines and host, the reliable outbox, commands, the in-memory log of the last 5000 records. |
| `engine`, `hysteria2`, `awg` | One engine per protocol. Hysteria2 runs the official core in-process; AmneziaWG runs in userspace (amneziawg-go) or on the kernel module. |
| `hostctl` | The only host state the agent owns: its nftables tables, the sysctl and journald baseline, the tunnel firewall, the UDP ports of its servers in an active UFW (only rules it tagged itself, never one of yours; `ufw` runs outside the agent's sandbox), host facts and metrics. |
| `torrentguard` | Torrent protection: recognises plaintext BitTorrent that a client starts by its protocol signature, never by port. Hysteria2 checks the flows it proxies; for AmneziaWG the host hands tunnel flows to the agent and a match is blocked in the kernel. See [Torrent protection](../guide/torrent-protection.md). |
| `warp`, `egress` | The node's WARP tunnel and the exits (direct or WARP) the engines use. |
| `certs` | Let's Encrypt and self-signed certificates for the servers on the node. |
| `doctor` | Host checks and the four safe fixes. |
| `update`, `awgprep` | Self-update from signed bundles; building the AmneziaWG kernel module on request. |

Engines never touch nftables, sysctl, the resolver or certificates themselves: they get what they need from the agent.

## Enrollment and the panel CA

The panel has its own certificate authority: an ECDSA P-256 CA valid for 10 years, created on first start, its key sealed in the database with the master key. It signs two kinds of certificates:

- **Node certificates**: client certificates valid for 30 days. A node is identified by a URI in its certificate, never by anything it writes in a message.
- **The agent endpoint's server certificate**: valid for 90 days, kept in memory only, issued for the secret TLS name. No public CA is involved, so the name never reaches Certificate Transparency logs.

Enrollment:

1. **Add node** creates the node and a one-time token (256 bits, valid for an hour by default, stored as a SHA-256 hash). The install command carries the panel address, the secret TLS name, the CA fingerprint and the token. An SSH install makes its own token and runs the same commands over SSH.
2. `mistgate-node enroll` makes a key and a certificate request on the node, connects with TLS 1.3 and the secret name, and accepts the server only if its chain ends in the CA with the pinned fingerprint.
3. The panel checks the token, issues the certificate and returns it with the CA. A repeat of the same call within 10 minutes (with the same key) gets the same certificate, in case the answer was lost. Ten failed attempts within a minute from one address (IPv6: one /64) are refused for a while.
4. From then on every call is mutual TLS. The panel checks the certificate's serial at every handshake (session tickets are off, so every handshake is a full one) and every 30 seconds on a running stream: a revoked certificate or a retired node is cut off.
5. The agent renews its certificate with a new key when less than 10 days are left. The old certificate stays valid for 10 minutes after a renewal, in case the answer was lost.

Agents reach the endpoint on the panel's public port: a TLS handshake with the secret name gets the agent endpoint, every other name gets the normal public site. `serve --agent-listen` gives agents a listener of their own instead.

## The agent stream

Each node keeps exactly one bidirectional Connect stream over HTTP/2, always opened by the node. A newer stream from the same node replaces the older one.

- **Hello.** The agent opens with its version, capabilities, host facts, the revision and hash of what it runs, and its reliable-message position. The panel answers with its clock, the node's settings and what it has already received.
- **Liveness.** Any message from the node counts. Stats are sent every 10 seconds even when empty, so they double as a heartbeat. The panel closes a stream after 90 seconds of silence (per node, configurable); a node without a stream for less than 10 minutes is a "Host blip", longer is "Unreachable". The agent reconnects with a backoff from 1 to 60 seconds.
- **Capabilities.** New features are additive. An agent lists what it can do (`doctor/1`, `update/1`, `awg/1`, `warp/1` and so on), and the panel sends a feature only to agents that listed it. An old agent with a new panel keeps working; the admin shows what needs an agent update.

### Desired state and the applied hash

The panel owns the truth. For every node it computes the desired state: every server on the node with its full settings and the credentials of every user who may use it, plus the node's WARP configuration.

- A full state lists everything; anything not listed is removed. A delta changes one revision into the next, so adding a user does not resend everything.
- Both sides compute the same state hash (shared code in `internal/statehash`). After applying, the agent hashes what its engines actually hold and returns it. Equal means in sync; different raises a `state_drift` event, a full resend, and an alert if it persists.
- A change of credentials only swaps the credential set of a running server; a change of its settings restarts that server alone.
- Nodes receive only what they must verify. For Hysteria2 that is the SHA-256 of each user's token, not the token; for AmneziaWG the public key and preshared key of each device, never its private key.
- The agent keeps the last applied state on disk and runs it when it starts, so a node keeps serving while the panel is unreachable.
- Credentials with an end date are dropped by the agent itself when the date passes, even without the panel.

### Stats and events

Stats batches (traffic per credential, open sessions, host metrics, the health of each server) and events are **reliable messages**: each has a sequence number, and the agent keeps it until the panel acknowledges it. The panel stores each one in a single transaction together with the last sequence number, so a batch resent after a reconnect is never counted twice. While the panel is unreachable the agent queues up to 6 hours of batches (merging batches of the same hour when more than 360 are waiting), and reports a `stats_dropped` event if it had to drop older ones. Traffic is counted in hourly buckets. A user who crosses a quota is cut off right after the batch that crossed it.

Doctor reports are snapshots, not reliable messages: the newest one wins. The agent also answers commands (restart a server, kick sessions, retire, run the doctor, apply a fix, update, roll back, prepare the AmneziaWG kernel module, stream its log) with exactly one result each.

## Updates, SSH installs and backups

- **Node agents** update from the signed bundle in `<data-dir>/dist`. You copy it there, or the panel downloads a newer one from the latest stable GitHub release every 10 minutes when the installation's key (`release.pub`) verifies it. A rollout, an update of one node or a scheduled update sends the agent the signed manifest; the agent fetches the binary from the panel over its own mutual-TLS connection, verifies it with the key compiled into it, puts it next to itself and restarts. The panel's health gate decides, and a failed update rolls back; the systemd unit also puts the previous binary back after a crash loop. See [Updates](../operations/updates.md).
- **The panel** checks the latest stable GitHub release every 10 minutes and installs one only when the owner confirms it (with a step-up) and a panel manifest signed with the key compiled into the running panel names that binary. A helper outside the panel's sandbox, as root (a transient unit when the panel runs as root, else the fixed unit `mistgate-panel-update.service`), downloads and verifies the release again, stops the panel, archives the data directory, swaps the binary and starts it. If the panel does not stay up for 45 seconds, the helper puts back the previous binary and the data.
- **SSH installs** connect with a password the owner enters, as root or as a user with passwordless sudo, and only to the host key the owner confirmed. The panel opens SSH, 80/tcp, 443/tcp and 443/udp in a host firewall that is already active (UFW or firewalld), uploads the agent from the trusted bundle, enrolls it with a fresh token on stdin, starts the unit and waits for the node to connect. The password stays in the database, sealed with the master key, as the node's saved access (password rotation, reveal after a step-up); it outlives a retired node until the owner forgets it. See [Install a node over SSH](../getting-started/ssh-install.md).
- **Backups** run on a schedule (every 1 to 168 hours) or on demand: the panel snapshots the database, packs it with the other data files and the master key, encrypts the archive with age to the owner's public recipient and uploads it to Cloudflare R2. The private identity never reaches the panel; `mistgate backup restore` decrypts offline. See [Backups](../operations/backups.md).

## The panel's HTTP surface

| Listener | Serves |
|:--|:--|
| Public (`--listen`) | The decoy site; the public mounts under the secret subscription prefix (subscriptions, user pages, their self-service calls, the brand logo); the admin when it lives on a secret prefix or a secret host; the agent endpoint for the secret TLS name. |
| Admin (`--admin-listen`, optional) | The admin only, plain HTTP, for the separate-listener mode. |
| Agent (`--agent-listen`, optional) | The agent endpoint only, TLS. |
| ACME HTTP (`--acme-http`, with `--acme-domain`) | Let's Encrypt HTTP-01 and the redirect to HTTPS. |

How the public listener decides, in this order: a TLS connection with the secret agent name goes to the agent endpoint; a request for the secret admin host or under the secret admin prefix goes to the admin; a request under the subscription prefix goes to the subscription handler; everything else gets the decoy site. Non-canonical paths, wrong prefixes and unknown hosts end in the same two answers of the decoy (its page or its 404), and every 404 takes at least 8 ms, so an unknown subscription token (one database read) cannot be told from an unknown path by timing. Even a 404 or 405 produced inside the subscription handler is replaced by the decoy's 404.

The admin is the single-page app plus the Connect API under `<admin>/api/`, the MCP server at `<admin>/mcp` and the server-rendered install manager of SSH installs at `<admin>/nodes/install`. The API sits behind the session check: a session cookie (`__Host-sid`, Secure, HttpOnly, SameSite=Strict) or an API token in the `Authorization: Bearer` header. Admin responses carry a strict Content-Security-Policy, `X-Frame-Options: DENY`, `no-store`, `noindex`, and HSTS over TLS; state-changing requests from other origins are refused.

Each client (an IPv4 address or an IPv6 /64) has a request budget per side: public, admin and agent. Over the budget it gets a 429 in the style of the decoy. The subscription endpoint has its own limits on top: a network that tries 20 unknown tokens within a minute gets only the decoy for 15 minutes, one token may be fetched 60 times an hour, and one link used from more than 8 networks in a day raises a "link shared" event.

## Storage

- **SQLite** through `modernc.org/sqlite` (pure Go, no cgo), in WAL mode, with one writer connection and a pool of four read-only ones. Tables are STRICT; ids are 128-bit random strings with a kind prefix (`nod_`, `usr_`, ...); times are Unix seconds.
- **Migrations** are goose SQL files embedded in the binary (`internal/panel/store/migrations`) and applied on every open. An applied migration is never edited; changes come as new files.
- **Secrets** are sealed with XChaCha20-Poly1305 under the master key, with the row id as associated data, so a sealed value cannot be moved to another row. Passwords are argon2id hashes; API, enrollment and subscription tokens are looked up by their SHA-256. User page passwords are derived from the master key and the link, never stored.
- **Files**: see the data directory layout in [Configuration](configuration.md).

## The protocol plugin contract

A protocol is two halves that share one vocabulary (`internal/plugin`): a spec of a server on a node, a user credential, traffic deltas, sessions, health.

The **panel half** (`internal/panel/protocols`) describes and renders the protocol:

| Method | Purpose |
|:--|:--|
| `ID`, `DisplayName` | `hysteria2` / "Hysteria2", `awg` / "AmneziaWG". |
| `SettingsSchema`, `DefaultSettings`, `Validate`, `Summary` | The JSON Schema the admin renders the profile form from, defaults with generated secrets, per-field validation, the one-line summary on a profile card. |
| `BuildInbound` | Turns profile settings, node and per-node overrides into the spec the node runs. |
| `IssueCredential` | Makes a device's credential: the secret the client presents, and the verifier sent to nodes. |
| `Clients`, `Render` | Which apps can use the protocol and in which formats, and its piece of a subscription in a given format. |
| `Doctor` | Protocol-specific checks for the fleet doctor. |

Optional interfaces cover what only some protocols need: per-server key material (`InboundInitializer`, used by AmneziaWG for the server key pair), one credential per device and profile (`PerDevice`), minimum client versions (`ClientRequirements`) and settings repair before validation (`SettingsNormalizer`). The exact signatures are in `internal/panel/protocols/protocol.go`.

The **node half** (`internal/node/engine`) runs it. An engine applies a spec with its credentials (idempotent; credentials swap without a restart), removes a server, collects traffic deltas and sessions, kicks sessions, reports what it holds (for the state hash) and its health. From the agent it gets certificates, the exits (direct or WARP), the node's DNS and the decoy handler for unauthenticated requests.

Both registries are built into the binaries (`internal/panel/protocols/builtin` and `cmd/mistgate-node/wire.go`); there is no loading of plugins at run time. External protocol plugins, with VLESS REALITY as the first, are **Planned**.

## Security model in brief

The longer version, with sign-in, roles, step-up, sessions and the audit log, is in [Security](../operations/security.md).

**A stolen API token**

- Can call only the procedures on the token allow-list, up to the level of its profile: read-only reads; operator also manages users (create, change, enable, extend, reset traffic, revoke devices) and runs checks; admin also reads the audit log and the saved SSH endpoints and logins of nodes.
- Never receives a subscription link, a device key, a page password or a server password: no procedure open to tokens returns one.
- Cannot pass a step-up, create or revoke tokens, change sign-in settings, manage nodes or profiles. Applying a doctor fix, starting, pausing or cancelling a rollout, scheduling a node update, installing a node over SSH or changing its SSH password works only through MCP and only after the owner approves the plan in the admin.
- Expires (90 days by default, at most 365) and has a rate limit. Its changes are in the audit log under the token's name, its reads at most once a minute, with the address they came from. Revoke it under **Integrations**.

**A stolen subscription link**

- Gives what the person has: the configs of their servers and their user page, with the self-service AmneziaWG keys if those are on. A page password, when on, guards the page and its self-service but not the subscription itself.
- Does not give anything about other users, the admin, or the nodes beyond their public addresses.
- Each app that fetches it shows up among the person's devices, and use from many networks raises a "link shared" event. Issue a **New link** (the old one stops working at once) and delete the devices you do not recognise: their credentials leave every node.

**A compromised node**

- Holds what that node needs: the profile secrets of its servers (obfuscation passwords, the self-signed key, AmneziaWG server keys), its WARP key, and the verifiers of the users who may use it. It sees the Hysteria2 tokens of clients that connect to it.
- Can talk to the panel only as itself, only on the agent endpoint: report traffic, events and health for its own servers (a batch claiming more than 10 Gbit/s on average is dropped). It cannot call the admin API, read other nodes' secrets, or see users who do not use it.
- **Retire from fleet** revokes its certificate at once; then change the secrets of the profiles it ran.

**A compromised panel or its data directory**

- Is the keys to everything: the master key decrypts every stored secret, and the panel decides what every node runs.
- Holds the saved SSH access of the nodes installed over SSH, sealed with the master key: whoever has both can log in to those servers with their passwords, signature or not. Nodes installed with the install command have no saved access.
- Still cannot make a node agent run code you did not sign: agents accept updates only with a signature that verifies under the release public key compiled into them, and the private release key lives offline, never on the panel. Nor can a changed data directory make the root update helper install a panel you did not sign: it verifies with the key compiled into the binary, never with `release.pub`. Commands to nodes are a fixed list; doctor fixes are four safe operations; nodes refuse port-hopping ranges below port 1024 or over their sshd port.
