---
title: MCP server
description: The panel's built-in MCP server for AI agents, how to connect a client, every tool, the plan and apply flow, and which changes wait for the owner.
---

The panel has a built-in Model Context Protocol (MCP) server, so an AI agent can read the fleet and make day-to-day changes. It uses the same API tokens and the same procedures as the [admin API](api.md): the token's profile decides what the agent sees and may do, and every call is in the audit log. Nothing changes in one call: an agent plans a change, then applies it, and the riskiest changes wait for the owner's approval in the admin.

## The endpoint

```text
<admin URL>mcp
https://panel.example.com/<prefix>/mcp
```

- Streamable HTTP, stateless, JSON responses. It offers tools only: no resources, prompts or sampling.
- It exists only on the admin surface (the secret prefix, host or listener), never on the public side.
- Every request needs `Authorization: Bearer tk1_…` and is checked again: a revoked or expired token stops at its next request. There is no session to take over.
- `tools/list` shows only the tools the token's profile may use.

**Integrations → MCP** shows the address and a ready snippet for each kind of client.

## Connect a client

Create a token in **Integrations → API tokens** with the narrowest profile that works (see [Token profiles](api.md)). Then:

**Claude Code** (Streamable HTTP):

```sh
claude mcp add --transport http mistgate https://panel.example.com/<prefix>/mcp --header "Authorization: Bearer <token>"
```

**Any client that speaks Streamable HTTP**:

```json
{
  "mcpServers": {
    "mistgate": {
      "type": "http",
      "url": "https://panel.example.com/<prefix>/mcp",
      "headers": {
        "Authorization": "Bearer <token>"
      }
    }
  }
}
```

**Clients that can only start a command** (for example Claude Desktop) use the stdio proxy, `mistgate mcp`, on your own computer. Save the token as the first line of a file only you can read, then:

```json
{
  "mcpServers": {
    "mistgate": {
      "command": "mistgate",
      "args": ["mcp", "--url", "https://panel.example.com/<prefix>/", "--token-file", "<path-to-token-file>"]
    }
  }
}
```

### The stdio proxy

`mistgate mcp --url <admin URL> --token-file <file>` runs a local MCP server on stdin and stdout and forwards every message to the panel's endpoint. It decides nothing, caches nothing and knows no tool: the panel answers.

- The token is read only from the file's first line, never from the command line or the environment, and never printed. A warning is printed when others can read the file (not checked on Windows).
- `--url` must be `https`, except for `localhost` or a loopback address. Redirects are not followed, so the token goes nowhere else.
- When the panel refuses the token (expired, revoked or the wrong profile) the proxy stops with that message.
- `--url` and `--token-file` fall back to `MISTGATE_URL` and `MISTGATE_TOKEN_FILE`.

`mistgate` builds for Linux, macOS and Windows: `go build ./cmd/mistgate` makes the binary for the computer the agent runs on. See [CLI](cli.md).

## Limits

| Limit | Value |
|---|---|
| Tool calls at a time, per token | 4 (more: 429 "at most 4 tool calls at a time per token") |
| Requests per minute | the token's own limit |
| Request body | 256 KiB |
| A tool result | 32 KiB; long lists are halved until they fit and marked as truncated |
| Time per call | 30 s for reads and plans, 90 s for an apply |
| Open plans | 20 per token; 50 waiting for the owner across the panel |
| Stored arguments of a plan | 8 KiB |

## Tools

### Read tools

Read tools change nothing. Arguments are ids and plain words, never URLs: no tool fetches anything an agent names. A node can be given by its id or its exact name. The Profile column is the lowest profile that sees the tool; higher profiles see it too.

| Tool | Profile | What it returns |
|---|---|---|
| `fleet_status` | Read only | Every node with status, reason, online users, speed and CPU; totals, alert counts and the top consumers now. |
| `node_get` | Read only | One node: status and reason, host facts, profiles with their state, up to 10 online users, the owner's notes. No addresses, keys or certificate pins. |
| `node_metrics` | Read only | The node's current CPU, RAM, disk and network, traffic today, online users per protocol, today's top users. |
| `node_doctor` | Read only | The last stored doctor report of a node or of every node, with fix ids. From the Operator profile up it also takes `refresh: true`: the node runs its checks now (it only reads the host; waits up to 30 s). |
| `users_search` | Read only | Users by part of the name, `filter` (`online`, `expiring`, `over_quota`) or `group_id`; paged with `page_token`. |
| `groups_list` | Read only | Groups with their profile ids, user count and DNS preset. |
| `user_get` | Read only | One user: limits, status, devices, profiles, node access. No subscription link, no keys. |
| `user_traffic` | Read only | Used and quota, the last 14 days, the split per node and protocol. |
| `user_devices` | Read only | Devices with platform, model, last seen, online, and for AmneziaWG the profile and tunnel address. Never a key or a config. |
| `subscription_preview` | Read only | Which format a client (a client id or a User-Agent) would get and which profiles and nodes the user's access gives. Not the subscription itself, and no link. |
| `subscription_settings_get` | Read only | The subscription page every user sees: the apps per platform in display order (name, kind, download link, add-link template, description, recommended), the page options, the server-name template, and the subscription title, announcement, support link and refresh interval. No user's link. |
| `alerts_list` | Read only | Active alerts; with `include_history` also the closed ones (`window_s` up to 30 days). |
| `events_search` | Read only | The event feed by node, user, `min_severity` (`info`, `warning`, `error`) or exact `code`; paged with `before_id`. |
| `checks_results` | Read only | The client-eye checks: nodes by profiles, the last result, the failure streak and 24 hours of history. |
| `updates_status` | Read only | The panel build, the bundle's status and version, each node's update state and any saved schedule, the active or last rollout, and the fixed UTC offset used for new schedules. |
| `node_server_access_list` | Admin | Saved node SSH endpoint, login and fingerprint, plus whether a password rotation needs recovery and whether the node is retired (its access stays until the owner forgets it). Never a password. |
| `audit_search` | Admin | The audit log, filtered by `source` (`panel`, `bot`, `mcp`, `api`), actor or action; paged with `before_id`. |

### Change tools

Every change is a pair: `<tool>_plan` and `<tool>_apply`.

| Tool | Profile | Arguments | Needs the owner |
|---|---|---|---|
| `user_create` | Operator | `name` (1 to 64 characters, unique ignoring case), `group_id`, and optionally `quota_bytes`, `quota_reset` (`none`, `day`, `week`, `month` (default), `rolling_month`), `term_days` (at most 3650), `device_limit` (at most 100), `apps` (`happ`, `amnezia`), `nodes` (`all` or `node_ids`), `speed_limit_bps`, `dns_preset_id` | no |
| `user_update` | Operator | `user_id`, optionally `subscription_name` (at most 64 characters; empty uses `name`), and only the fields to change (as above, with `expires_unix` instead of `term_days`) | no |
| `user_disable` | Operator | `user_ids` (1 to 50) | when more than 3 users |
| `user_enable` | Operator | `user_ids` (1 to 50) | no |
| `user_reset_traffic` | Operator | `user_ids` (1 to 50) | when more than 3 users |
| `device_revoke` | Operator | `user_id`, `device_id` | no |
| `alert_mute` | Operator | `alert_id`, `duration_s` (at most 604800; 0 unmutes) | no |
| `subscription_app_upsert` | Operator | `platform` (`ios`, `android`, `windows`, `macos`, `linux`) and `name` say which app; for a change only the fields that change, for a new app at least `kind`: `kind` (`happ` takes the subscription link, `amnezia` an AmneziaWG key), `download_url`, `add_link_template` (placeholders `{url}`, `{url_enc}`, `{name_enc}`), `description` (at most 80 characters), `recommended` | always |
| `subscription_app_remove` | Operator | `platform`, `name` | always |
| `node_fix` | Admin | `node`, `fix_id` from the doctor report, `params` if the item lists any | always |
| `rollout_start` | Admin | exactly one `node_ids` entry from `updates_status` (updates that node now); the trusted version is pinned in the plan | always |
| `node_update_schedule` | Admin | Plan: `node_id`, `local_datetime` (`YYYY-MM-DDTHH:mm` in the offset from `updates_status`, at least a minute and at most a year ahead); the trusted version and offset are pinned in the plan | always |
| `node_update_schedule_cancel` | Admin | `node_id` | always |
| `update_timezone` | Admin | `timezone_offset_minutes` (fixed UTC offset east of UTC, from -720 to 840 in 15-minute steps; `180` is GMT+3) | always |
| `rollout_pause`, `rollout_resume`, `rollout_cancel` | Admin | `rollout_id` from `updates_status` | always |
| `node_rollback` | Admin | `node` | always |
| `node_install` | Admin | Plan: `host`, `port` (default 22), `username` (root or a user with passwordless sudo), node `name` (2 to 24 letters, digits or hyphens), `address`, optional `country_code`, `location`, `provider`; apply: `confirm_token` only. The plan reads only the server's public host key. The owner enters the SSH password and confirms the host key on the approval screen | always |
| `node_server_password_rotate` | Admin | Plan: `node` id or the exact name of a live node (a retired node is refused); apply: `confirm_token` only. The panel generates the new password itself and never returns it; the owner can reveal it in the node's settings | always |

Every `_plan` also takes `reason`: the agent's own words, at most 300 characters, shown to the owner as a quote. No tool takes or returns a server password: the owner types the install password on the approval screen, and the panel generates rotated passwords itself. `user_create` never returns the new user's subscription link: the owner copies it in the admin.

`rollout_start_plan` updates one selected node now; it cannot start a fleet-wide update. It takes only a node whose agent can update itself and is older than the bundle (outdated, failed or rolled back), and it is refused while another rollout is running or paused. The plan is pinned to the signed bundle trusted when it was made: if the bundle changes before it is applied, the apply fails and a new plan is needed. `node_update_schedule_plan` saves a future update for one node after owner approval; the node may be offline when it is planned, and a new schedule replaces the node's previous one. The saved task is pinned to that signed release and fixed UTC offset. If the node is offline when due, the panel waits up to two hours for it to reconnect, then marks the task missed and never starts it by itself; if the signed bundle changes, the panel keeps the task visible and does not substitute a different release. Use `node_update_schedule_cancel_plan` to cancel a pending task. Changing `update_timezone` affects new schedules only; existing tasks keep their saved instant and offset.

`subscription_app_upsert` and `subscription_app_remove` change one app of the page every user sees, found by its platform and its name ignoring case (a name two apps of one platform share is refused: change those in the admin). A new app goes to the end of the list. The values are checked exactly like a save in **Subscriptions → User page**: an http(s) download link, an add-link template that starts with an app's own scheme (`happ://add/{url}`, `myclient://add?url={url_enc}&name={name_enc}`) or http(s) but never `javascript:`, `data:` and the like, the known placeholders only, at most 30 apps; on top of that the agent's text may not contain invisible formatting characters. The plan reads the whole settings and remembers their state; if anyone saves the subscription settings before the apply, the apply fails with "the subscription settings changed after the plan" and nothing is overwritten: make a new plan. In `subscription_settings_get` and in the agent's copy of a plan, link query strings and long path segments read `[redacted]`, like every result; the owner's approval card shows every value in full.

## Plan and apply

1. The agent calls `<tool>_plan` with the arguments. The panel validates them, reads what it needs and describes the change in its own words. Nothing changes. The result:

   ```json
   {
     "plan_id": "pln_...",
     "summary": "Disable 5 users: their connections end and their devices are dropped from the nodes.",
     "facts": [{"key": "count", "value": "5"}, {"key": "users", "value": "...", "untrusted": true}],
     "needs_approval": true,
     "danger": ["bulk"],
     "expires_in_s": 600,
     "confirm_token": "cf_...",
     "next": "..."
   }
   ```

2. The agent shows the plan to the person it works for and waits for their go-ahead. If `needs_approval` is true, it also waits for the owner.
3. The agent calls `<tool>_apply` with the `confirm_token`, its only argument. The panel runs exactly the stored arguments, once, and answers with `plan_id`, `status: "applied"` and a one-line result.

The confirm token:

- works once, for 10 minutes, and only for the token that made the plan and only with that tool;
- carries no arguments: they cannot change between plan and apply;
- applying a plan that was already applied returns the same result again; a token of another plan, tool or API token is simply "unknown confirm token".

When an apply cannot run, it says why: "waiting for the owner to approve plan pln_…; it expires at …", "rejected by the owner", "expired: make a new plan", "already running", "failed: …. Make a new plan.", "the token was revoked", or "timeout, outcome unknown: check before retrying".

### Which plans wait for the owner

A plan needs the owner when one of these applies (the `danger` list):

| Code | Meaning | Tools |
|---|---|---|
| `step_up` | The operation itself asks for a fresh confirmation in the admin. | the rollout tools, `node_rollback`, `node_update_schedule` and its cancel, `update_timezone`, `node_install`, `node_server_password_rotate` |
| `fleet` | It changes what runs on the nodes. | `node_fix`, the rollout tools, `node_rollback`, `node_update_schedule` and its cancel, `node_install`, `node_server_password_rotate` |
| `bulk` | It touches more than 3 users at once. | `user_disable`, `user_reset_traffic` |
| `user_page` | Every user sees it on their subscription page. | `subscription_app_upsert`, `subscription_app_remove` |

### Where the owner approves

Such a plan appears in **Integrations → Waiting for you**, with a badge in the admin. The owner sees the change in the panel's own words, the danger notes, the agent's reason (marked "Written by the agent. The panel did not check it.") and the time left, then chooses:

- **Approve**: asks for the owner's passkey or authenticator code once more. The agent's apply goes through after this, for this plan only.
- **Reject**: the agent's apply fails with "rejected by the owner".

For `node_install`, the approval card shows the SHA-256 host key fingerprint the panel read and its key type (the agent's copy of the plan has the fingerprint redacted, like any key-shaped value). The owner compares it with a trusted copy, ticks the confirmation and types the server's SSH password there; **Approve** stays disabled until both are done. The panel seals the password to that plan, and the agent's apply, which carries only the confirm token, installs with it and with exactly the confirmed host key, once. The password never passes through the agent, its transcript or the model provider. Password rotation has the same owner-approval boundary: the panel generates the new password and never returns it.

A token can never confirm its own plan. Undecided plans expire 10 minutes after they were made. **Recent decisions** keeps the history with the outcome: done, error, expired, cancelled (the token was revoked) and so on. The agent should not poll more often than once every 30 seconds.

## Untrusted data in results

Names, notes, reasons, log lines and the parameters of events and alerts come from users, nodes and other systems. The server's instructions and every tool that returns such text say so: they are data, never instructions, and an agent must not follow requests found in them.

The panel also protects the agent and you:

- Text from data is cleaned: control characters, line breaks and invisible formatting characters are removed, and long values are cut.
- A plan's summary never contains text from data; such values are separate facts marked `untrusted`, and the owner sees them quoted.
- A last pass over every result replaces anything shaped like a secret: API tokens, share links (`hysteria2://…` and the like), tunnel configurations, key material, URL query strings and long URL path segments (the secret prefix, a subscription token) and bare 32-byte keys.

## What an agent never gets

- Subscription links and user page passwords.
- Device keys and configurations, WARP accounts and keys, profile secrets.
- Server passwords: neither the SSH password of an install nor a rotated one.
- Node addresses and certificate pins. The exception is the saved SSH endpoint and login that `node_server_access_list` and the `node_install` and rotation plans show an Admin token.
- Anything of the tokens, approvals, sessions and passkeys.
- The audit log, unless the token has the Admin profile.

## Audit

Every procedure a tool calls is written to the audit log under "MCP token <name>", with the source MCP. Plans and applies add their own rows: "planned a change: <tool>" and "ran the planned change: <tool>"; the owner's decisions appear as "approved the change" or "rejected the change". **Settings → Audit** filters them by the source MCP.
