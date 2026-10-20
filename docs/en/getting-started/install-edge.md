---
title: Install the Cloudflare edition
description: "Run the panel on Cloudflare Workers instead of a server with mistgate edge deploy: what you need, the API token, the key file you must back up, the first sign-in and what is not available yet. A preview."
---

The Cloudflare edition is the same panel, built for WebAssembly and run by six Cloudflare Workers with a D1 database, instead of a binary on your own Linux server. One command, `mistgate edge deploy`, creates everything on your Cloudflare account. The nodes are still your servers, added from the admin as usual. This page takes you from a Cloudflare account to the owner account in the admin. The examples use `panel.example.com`.

> **Warning:** this edition is a preview. `mistgate edge deploy` and the panel have been tested against a simulated Cloudflare, not yet on a real account (that is the next round of work). The edge bundle is in the releases since v0.1.36. Do not move a panel that people depend on here. Some features are not available yet: see "Not available in this edition yet" below. If you want a panel you can rely on today, use [Install the panel](install-panel.md).

## Requirements

| What | Requirement |
|:--|:--|
| Cloudflare plan | **Workers Paid.** On Workers Free a request may use only 10 ms of CPU and make 50 subrequests, and the panel's cold start alone (loading the WebAssembly panel) costs about 125 to 254 ms, so it does not fit. D1 and Durable Objects are billed by Cloudflare by use; what a panel costs on a real account has not been measured yet. |
| Domain | A host name, for example `panel.example.com`, inside a zone that is **on Cloudflare DNS** and active. The command looks for the zone of the host in the accounts your token can see, and stops before it creates anything if there is none, or if the zone is not active yet (its nameservers must already point at Cloudflare). Use a name with no DNS record yet: Cloudflare adds the record for a Worker custom domain itself, and what it does with an existing record has not been tried. |
| The command | `mistgate`, the same binary as the panel, on any computer with internet access to `api.cloudflare.com` and `github.com`. The official release binaries are for Linux (amd64 and arm64); on macOS or Windows build it from source (see [Requirements](requirements.md)). |
| A release with the edge bundle | The command downloads `mistgate-edge.tar.gz` and its signed manifest from the GitHub release of its own version and checks them against the release key compiled into the binary, before anything is uploaded. The edge bundle has shipped in releases since v0.1.36. A development build must be told which bundle to use with `--version` or `--bundle`. See [Releases and signing](../operations/releases.md). |
| Nodes | The same as for any panel: see [Requirements](requirements.md). Agents connect to `panel.example.com` over HTTPS (a WebSocket), so a node needs only outbound TCP 443 to it. |

## Create the API token

The command talks to the Cloudflare API with a token you create. In the Cloudflare dashboard open **My Profile → API Tokens → Create Token → Create Custom Token** and give it these permissions:

| Scope | Permission | What the command uses it for |
|:--|:--|:--|
| Account | Account Settings: Read | `GET /accounts`, the first call: it lists the accounts the token sees and so also checks the token. |
| Zone | Zone: Read | `GET /zones`: finds the zone of the host. |
| Account | Workers Scripts: Edit | Lists the scripts and reads their deployments, uploads all six with the static files, sets the cron schedule, turns the `workers.dev` name off and, on a rollback, puts the earlier versions back (`/workers/scripts/...`). One permission covers all six scripts. |
| Account | D1: Edit | Finds or creates the database (`/d1/database`). |
| Zone | Workers Routes: Edit | Attaches the host to the front Worker as a custom domain (`PUT /workers/domains`). |

Under **Account Resources** pick your account, and under **Zone Resources** the one zone of the host.

> **Important:** this list is worked out from the API calls the command makes. It is not confirmed on a real account; the exact minimum is known only after the first real deploy. If the command stops with a permission error, the message says at which call. The most likely gap is the custom domain, which may also need **Zone: DNS: Edit**: add the permission and run the command again with `--resume`.

Give the token to the command only through the environment variable `CLOUDFLARE_API_TOKEN`. There is no flag for it, so it stays out of the process list, and the command never writes it to the key file or to Cloudflare; it is replaced by `[REDACTED]` in error messages. In bash the line below also keeps it out of the history; in PowerShell use `$env:CLOUDFLARE_API_TOKEN = "..."`:

```sh
read -rs CLOUDFLARE_API_TOKEN && export CLOUDFLARE_API_TOKEN
```

Only the command on your computer uses the token; the deployed panel never receives it. Delete the token in the dashboard when you are done, and create a new one for the next deploy.

## Deploy

Look at the plan first. `--dry-run` only reads from Cloudflare (the token, the zone, existing scripts and databases), checks the bundle and prints what a real run would do. It creates no file and nothing on Cloudflare:

```sh
mistgate edge deploy --host panel.example.com --dry-run
```

Then run it for real:

```sh
mistgate edge deploy --host panel.example.com
```

| Flag | Default | Meaning |
|:--|:--|:--|
| `--host` | none, required | The public name of the panel, without scheme, port or path. |
| `--name` | `mistgate-edge` | The name of the front Worker script and of the D1 database; the other five are `<name>-link`, `<name>-public`, `<name>-admin`, `<name>-jobs` and `<name>-provision`. Give another name to run a second panel in the same account. |
| `--d1-location` | `weur` | Where D1 should keep the database: `wnam`, `enam`, `weur`, `eeur`, `apac` or `oc`. A hint to Cloudflare; choose the region nearest to you. |
| `--version` | the binary's own release | The edge release to download, `vX.Y.Z`. |
| `--bundle` | none | A directory with an edge bundle already on disk (`mistgate-edge.tar.gz`, `edge-manifest.json`, `edge-manifest.sig`) instead of the download. Not together with `--version`. |
| `--resume` | off | Continue a run that stopped, from the key file. See below. |
| `--dry-run` | off | Print the planned calls (the names of the bindings, no values); read from Cloudflare, change nothing. |
| `--rollback` | off | Put every Worker back on the version it had before the last update. See "Roll back" below. |
| `--insecure-skip-signature` | off | For a development bundle you built yourself, with `--bundle` only: skips the signature check and prints a warning. Never use it on a bundle you did not build. |

The panel is six Workers. Only the first has the domain; the others have no address of their own and are reached through it:

- `<name>`, the front Worker: a small router with no WebAssembly. It sends each request to one of the others by its path, and owns the Durable Objects that the nodes' agents connect to.
- `<name>-public`: subscriptions, the user page and the decoy site.
- `<name>-link`: the panel's side of the agent link.
- `<name>-admin`: the admin and its API. It is the only one that creates the panel on an empty database.
- `<name>-jobs`: the job that the cron trigger starts every minute.
- `<name>-provision`: SSH installs of nodes.

All six use the one D1 database.

A run does these steps, in this order, and stops at the first failure:

1. Downloads the bundle and verifies its signature.
2. Checks the token and finds the account and the zone of the host.
3. Refuses to go on if a Worker script or a D1 database with that name already exists while the key file is missing: it never adopts or replaces what it cannot open with its own keys.
4. Writes the key file (see below), **before** it creates anything.
5. Creates the D1 database.
6. Uploads the six scripts in this order: link, provision, public, admin, jobs, and the front Worker last, so that a script is there before the one that binds it. The very first time the front Worker goes up before all of them, with no bindings to the others, because the link Worker binds its Durable Objects; it goes up again at the end with them. The admin's static files go up with public and admin. The secrets go up with each script: `MASTER_KEY` in the five scripts that run the panel, and the secret admin path, the subscription path and the hash of the setup token in admin only. A script whose content is the same as at the last run is not uploaded again.
7. Turns off the `workers.dev` name and the preview URLs of each script right after its upload, sets a cron trigger that runs every minute on the jobs script, and attaches `panel.example.com` to the front Worker as a custom domain. Cloudflare creates the DNS record and the certificate.
8. Waits up to five minutes for the decoy page at `https://panel.example.com/` to answer, then prints the links.

Workers Logs are switched off on all six scripts.

## The key file

The command writes `mistgate-edge-<name>.json` in the current directory (`mistgate-edge-mistgate-edge.json` with the default name) and prints its full path. It holds everything that cannot be read back from Cloudflare: the **master key** of the panel, the secret admin path and subscription path, the setup token, and the ids of your account, zone and database. After an update it also holds the version each script had before it, for a rollback.

> **Warning:** back this file up as soon as the command prints its path: keep a copy in your password manager or on an encrypted drive. Cloudflare does not show a secret again after it is set. If the file is lost, so is the master key, and with it everything the panel sealed in its database (the saved SSH passwords, device keys, the fleet's CA); a later `deploy` also refuses to touch the scripts and the database it created, because it no longer has their keys. Anyone who gets the file can open what the panel seals, so never put it in a repository or a shared folder.

On Linux and macOS the file is created with mode 0600. On Windows the mode is not enforced: the file inherits the folder's permissions, so keep it in a private folder (the command warns about this). `--resume` needs the file in the folder you run the command from, and `setup-link` takes its path.

If a run stops half way (a network error, Ctrl+C, a Cloudflare error), nothing is rolled back and the key file stays. Fix the cause and run the same command again with `--resume`: it reads the key file and the bundle version recorded in it, and continues with the same keys and the same setup token; scripts that were uploaded already are not uploaded again. Without `--resume`, a run that finds the key file counts as an update (see below).

## After the deploy

The command prints where the key file is, the admin address (`https://panel.example.com/` plus a secret path of 24 random characters) and the one-time setup link:

```text
Admin URL: https://panel.example.com/<secret path>/
Setup link (one-time; the token is valid for 30 minutes from the panel's first start): https://panel.example.com/<secret path>/setup#<token>
```

The admin address is secret on purpose, as in the secret path mode of a VPS panel: anyone else who opens the host sees only the decoy site. Keep the address; it is also in the key file.

Open the setup link in a browser and create the owner account, as in [Install the panel](install-panel.md) (step 8, "Sign in for the first time"). The panel starts for the first time at the first request or the first cron tick, in practice within a minute of the upload, and the token is valid for **30 minutes from that moment**. The part after `#` never reaches a server.

If the link has expired, or you closed the page before the account existed:

- `mistgate edge setup-link --file mistgate-edge-mistgate-edge.json` prints the stored link again. It carries the same token, so it helps with a lost link but not with an expired one.
- For a new token, run `mistgate edge deploy --host panel.example.com` again without `--resume`: it uploads the same bundle again and makes a new setup token. This works only while no admin exists; the link of a panel that has an owner does nothing.

If the last line of the output says that the host did not answer HTTP 200 yet, the custom domain may take a few minutes to resolve, and Cloudflare may still be issuing its certificate. The deploy itself is finished: open the link in a few minutes.

## Locked out

Set `CLOUDFLARE_API_TOKEN` to a token with Workers Scripts Edit, then run `mistgate edge reset-login --file mistgate-edge-mistgate-edge.json` (add `--admin LOGIN` to prefill a login). It uploads a one-time token hash and prints a recovery link; open it within 30 minutes. The new password and authenticator QR appear once, all sessions end, and passkeys stay registered. Remove a lost passkey later in **Settings → Security**. `--dry-run` shows the upload plan without uploading a secret or creating a link. The token is never saved in the key file.

## Add nodes

Add nodes in **Nodes → Add node** as on a VPS panel; see [Add a node](add-node.md). Installing over SSH works on this edition too, with these differences (the details are in [Install a node over SSH](ssh-install.md)):

- The panel connects to the new server from Cloudflare's network, so SSH on the server must accept connections from the internet for the time of the install, not only from your own address.
- The panel refuses addresses that belong to Cloudflare or sit behind its proxy, private networks and servers reachable only through a VPN. The wizard says "The panel's network refused this address, or the server cannot be reached from it". Use the server's own public address or a name that is DNS-only.
- Use the manual install command whenever SSH does not work this way, or the server accepts only SSH keys. The wizard offers it next to the error. The agent connects out to the panel, so it works from any network that can reach `panel.example.com`.

## Not available in this edition yet

The admin shows "Not available in the Cloudflare edition yet" in the places below, and a call that would start such work is refused with the same words. Everything else works as on a VPS panel: nodes, profiles, users, subscriptions and the user page, the node doctor and the SSH install.

| Feature | What is missing |
|:--|:--|
| Backups | No encrypted backups and no restore. Cloudflare's own D1 recovery (Time Travel) can restore the database, but it has not been tried with the panel. Keep the key file. |
| Alerts and Telegram | Health alerts are evaluated and shown in the panel. Telegram messages are not sent yet. |
| MCP and API tokens | The MCP server is not served. Tokens you already have are listed and can be revoked; new ones cannot be created. |
| Tunnel checks | The panel does not run client-eye checks of the servers' tunnels. |
| Updating nodes | The panel does not update node agents: no rollouts, no scheduled updates. |
| Node logs | The live log of a node's agent is not shown. |
| Updating the panel | There is no self-update: update by running the command again (below). |
| Own decoy site | A node cannot get the **Own site** decoy: this edition does not store uploaded bundles. |
| TSPU probe | The [TSPU probe](../operations/reachability.md#tspu-probe) of the country check does not run: a run takes minutes and the scheduled jobs of this edition have seconds. The card says it is unavailable. Globalping and the block lists work. |

Limits of this edition that matter for devices:

- Each device has its own keys on each node, and the panel remembers what it sent to a node in one D1 row, which D1 caps at 2 MB. That is about 38,000 credentials per node, roughly 7,500 people with two link protocols each at 2.5 devices on average (about 19,000 people before devices had keys of their own). The panel writes a warning to its log, once per node, when the row passes 1.5 MB.
- The hourly cleanup makes one more D1 call (it removes old deleted devices).

Registering a WARP account from the panel is not available either and fails with "WARP registration is unavailable in this edition"; the admin does not mark it yet.

## Update

Download a newer `mistgate` (or pass `--version vX.Y.Z`) and run the command again with the same `--host` and `--name`, from the folder with the key file, without `--resume`:

```sh
mistgate edge deploy --host panel.example.com
```

It reads the key file, so the keys, the database and the secret paths stay. It uploads the scripts whose content changed (a release that leaves the front Worker alone does not restart the agents' connections), applies the new Durable Object migrations and makes a new setup token, which you can ignore (it is why admin is uploaded every time). A bundle that is older than what is deployed is refused before anything is uploaded. While the front Worker is replaced, connected agents reconnect on their own and an SSH install in progress is retried (its third interruption fails it). The key file is rewritten with the new bundle version; the master key stays, so your earlier backup of the file still opens everything. Read the release notes first: there is no backup of the database to go back to, and a rollback does not touch it.

## Roll back

If an update misbehaves, run this from the folder with the key file:

```sh
mistgate edge deploy --host panel.example.com --rollback
```

Before an update uploads anything, the command notes in the key file the active version of each of the six scripts and its cron schedule. `--rollback` puts them back through Cloudflare's deployments, the front Worker first and then the others in the reverse order of the upload, and restores the schedules. It uses the same token, takes no bundle and uploads nothing; with `--dry-run` it only lists what it would restore. A script that the last update created has no earlier version and stays as it is.

- The D1 database is not rolled back. A release is meant to change its schema only by adding to it, so the version before the update still runs on it.
- It goes back one update: the key file keeps the versions before the last new bundle. After the first deploy there is nothing to go back to.
- The next `mistgate edge deploy` uploads all six scripts again.

## Moving between a VPS panel and the edge

Not supported yet. There is no way to carry users, nodes and subscription links from a VPS panel to the edge or back: a backup archive of a VPS panel cannot be restored on the edge, and nodes cannot be told to follow a new panel address. Choose the edition for a new fleet, or plan to re-create the users and enrol every node again. A move in both directions, with subscription links that keep working, is planned but not built.

## When the command stops

| The command says | What to do |
|:--|:--|
| CLOUDFLARE_API_TOKEN is required | Export the variable in the same shell. |
| list Cloudflare accounts: ... | The token is wrong, expired or lacks **Account Settings: Read**. Create it again as above. |
| no Cloudflare DNS zone matches host panel.example.com; the host's zone must be on Cloudflare DNS | Add the domain to Cloudflare, point its nameservers there and use a host inside that zone. |
| the Cloudflare zone example.com has status "pending", not "active" | Finish pointing the domain's nameservers at Cloudflare, then run the command again. |
| a Worker script with this name already exists but its local key file is missing; refusing to replace its secrets | The key file is not in this folder. Find it, or pick another `--name` (the old scripts and database stay in your account; remove them in the dashboard if you do not need them). A D1 database with the name gives the same refusal. |
| this is a development build with no release version: pass --version vX.Y.Z or --bundle DIR | Tell the command which bundle to use. |
| the deployed script is at migration ..., which this bundle does not know | The bundle is older than what is deployed. Use a newer release. |
| --resume requires the local deployment file ... | There is no key file for this `--name` in the current folder. |
| nothing to roll back: the local deployment file records no earlier version | Nothing was updated since the first deploy, so no earlier version was noted. A rollback needs an update to go back from. |

## Next

[Add a node](add-node.md), then [First users](first-users.md). Every other page of the guide applies, except for the features listed above. How the parts fit together: [Architecture](../reference/architecture.md).
