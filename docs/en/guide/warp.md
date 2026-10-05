---
title: WARP
description: How to send a profile's traffic out through Cloudflare WARP, how to get an account for a node, what fail-closed means, and how to read and fix the WARP link.
---

By default a profile's traffic leaves a node from the node's own address. With WARP it leaves through Cloudflare's network instead: sites see a Cloudflare address, not your hoster's. A node has at most one WARP account, and every profile with the WARP exit on that node goes through it. Both Hysteria2 and AmneziaWG profiles can use it.

## How it fits together

- **The exit is a profile setting.** **Exit** (in **Advanced** for Hysteria2, in **Basics** for AmneziaWG) is **Direct** or **WARP**.
- **The account belongs to a node.** It is set up on the WARP card of the node: node → **Settings**, the first card. The WARP chip in the node's header leads there too.
- **A profile with the WARP exit needs an account on every node it runs on.** The editor lists the nodes of the profile that have no working WARP, each a link to its WARP card.

To offer people both exits, make a twin of the profile with the other exit ("WARP copy"): see [Profiles](profiles.md).

## Fail-closed

A profile with the WARP exit never leaves directly. If WARP is not there, the profile does not carry traffic, instead of silently showing people's traffic from the node's own address:

| Situation | What happens to WARP-exit profiles on the node |
|:--|:--|
| The node has no WARP account | They do not start ("There is no working WARP on the node"). |
| WARP is paused | They stop passing traffic until it is resumed. |
| The tunnel is down | They stop passing traffic until it comes back; if it is only slow, they work slowly. |

The add-profile dialog warns before the click when the node has no WARP, has it paused, or its tunnel is down. The doctor's **WARP exit** check and the client-eye checks show the same from the other side: a node whose direct profile works while its WARP profile fails gets an alert that names the WARP exit. See [Health](../operations/health.md).

## Getting an account

The card of a node without an account says **Not set up** and offers two ways. Both need the owner and a fresh confirmation of the sign-in (step-up), and both need an agent that supports WARP; an older agent gets a link to **Updates**.

### Enable WARP (register)

**Enable WARP** registers a new Cloudflare WARP account for this node and stores it encrypted. The window says what the click does:

- it is an anonymous device account: no e-mail, no password;
- the request leaves from the panel's address, not from the node;
- it accepts the Cloudflare WARP terms on your behalf, for this one account. The window links the terms, and **Enable WARP** stays off until you tick "I have read the Cloudflare WARP terms and accept them for this account".

It takes a few seconds; then the node brings the tunnel up. If Cloudflare refuses new accounts from the panel's address ("Cloudflare refuses new accounts from the panel's address right now"), the window offers **Import wgcf profile**.

### Import a wgcf profile

Use this when registration from the panel does not work. Make the files with the `wgcf` tool on a computer where it works, then paste them or open them with **Open file**:

| File | Needed | What it gives |
|:--|:--|:--|
| `wgcf-profile.conf` | yes | The private key and the peer. It must have `[Interface]` with a `PrivateKey` and a `[Peer]`. |
| `wgcf-account.toml` | no | The access token. Without it, **Read from Cloudflare again** does not work and **Delete account** removes the account only here, not at Cloudflare. |

## The WARP card

Next to the title the card shows where the account came from (**Registered here** or **Imported**), the account type (`free` or `plus`) and the Cloudflare data centre of the last probe.

### States

| State | Meaning |
|:--|:--|
| **Not set up** | No account on this node. |
| **Waiting for the node** | The account exists, the node has not reported yet or is offline. |
| **No backend** | The host has neither a WireGuard kernel module nor `/dev/net/tun`. |
| **Starting** | The tunnel is coming up. "Starting: check failing" when the checks keep failing. |
| **Working** | Handshake and probes pass. "Working: last check failed" when the latest check did not. |
| **Not working** | The handshake or the probes fail. |
| **Paused** | You paused it. |

### Health of the link

- **Handshake**: when the tunnel last completed a handshake with Cloudflare.
- **Endpoint**: the Cloudflare address and port in use.
- **Traffic**: bytes in and out through the tunnel.
- **Cloudflare** and **Other site**: two probes through the tunnel, one to Cloudflare and one to a site outside it. Each reads **ok** with its time, **slow** (over 2 seconds), or **failed**. A probe times out after 20 seconds; HTTP failures show their status.
- "Checked N s ago". The node checks every 30 seconds, every 5 while the tunnel is starting. A run of failures shows as "3 failed checks in a row".
- **Details**: the WireGuard backend, Cloudflare's own flag (`warp=on`, `warp=plus` or `warp=off`) and whether the account's reserved bytes are stamped on packets.

When a check fails, the card says what happened and what it means, in words: a probe failed, traffic does not go through WARP (`warp=off`), the handshake is stale or missing, the interface is down, or the host has no WireGuard backend.

### Recovering by itself

The health panel's Doctor offers **Reconnect WARP** when it confirms that the tunnel is down. Review and confirm the plan first. It rebuilds the current tunnel and reapplies its routes without changing the account. Profiles using WARP may pause briefly. A slow but successful probe does not trigger a reconnect.

While the tunnel is down, the node climbs a ladder, one step per failed check:

1. it sets the routes, rules and device up again;
2. it tries the other Cloudflare addresses and ports: ports 2408, 500, 1701 and 4500, the IPv4 endpoint first, then the IPv6 one if the account has it;
3. it asks the panel to read the account from Cloudflare again (this needs the access token and creates nothing);
4. it gives up and the card says **Needs your attention**.

After the last step it rests for 10 minutes, still trying the next address after each failed check, and then starts over. The card shows the step it is on ("Trying to recover: switched the address and port to …").

### Buttons

| Button | When | What it does |
|:--|:--|:--|
| **Restart WARP** | The tunnel is down | Pauses and resumes in one step: the node tears the tunnel down and brings it up again with the ladder from the start. If the node did not confirm the pause in time, the resume goes out anyway: "WARP is on again, but the node did not confirm the pause: look again in a minute". |
| **Register again** | Cloudflare no longer knows the account (revoked) | Registers a new account and puts it in the old one's place in one step; the old device is deleted at Cloudflare. Same terms window as **Enable WARP**. |
| **Read from Cloudflare again** | The account has an access token | Re-reads the endpoint, addresses and client id from Cloudflare. Creates nothing. |
| **Pause** / **Resume** | Always | Pause tears the tunnel down. The window names the profiles that stop and how many connections go through them now. A paused account is resumed, not restarted. |
| **Delete account** | Always | Type the node name to confirm; needs a step-up. The node loses WARP; WARP-exit profiles stop. The device is deleted at Cloudflare too when a token is stored. You can register or import another account afterwards. |

Some problems need you, not a button. The card names them:

- the routing table WARP uses (51820) holds another program's routes, often `wg-quick`: free the table or stop the other tunnel;
- another program uses the routing-rule priorities WARP needs (90 and 110);
- a network interface named `mgwarp` already exists and is not WARP's;
- the node could not apply the account: the reason is in the node's events.

## IPv4 and IPv6

- The account stores Cloudflare's endpoint as IP addresses, never as a name. The node tries the IPv4 endpoint on every port first, then the IPv6 one, if the account has it.
- New accounts often have no IPv6 address inside the tunnel. Then the WARP exit dials IPv4 only, and IPv6-only destinations are not reachable through WARP.
- A node without IPv6 is fine for WARP: the doctor notes that WARP goes through its IPv4 address.
- Names are resolved with the node's **DNS resolvers for this node**, the same as for the direct exit. See [DNS](dns.md).

## What the host needs

A WireGuard kernel module, or `/dev/net/tun` for the userspace one. A node installed with an old service unit may hide `/dev/net/tun` from the agent; run `mistgate-node install` once with the current binary to rewrite the unit.

## Registration settings

Two things about registration are not in the admin, only in the API (`WarpService.GetWarpRegistrationParams` and `UpdateWarpRegistrationParams`, owner only):

- the values the registration request is built from (the API version, the user agent, the client version and the TLS fingerprint). Cloudflare changes what it accepts from time to time. An empty value falls back to the built-in one, and the read call returns the built-in values next to yours;
- **automatic re-registration**, off by default. When it is on and Cloudflare revokes a registered account, the panel registers a new one by itself, at most once an hour per node. Turning it on accepts the Cloudflare terms for those future accounts too.

See [API](../reference/api.md).

> **Note:** each node uses one free consumer WARP account, and the traffic of everyone on that node goes through it. Cloudflare may limit or suspend such an account; then the WARP-exit profiles stay down until you register or import another one. Do not rely on WARP for anything that must not fail.
