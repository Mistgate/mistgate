---
title: Install Mistgate with an AI agent
description: "Give a coding or MCP agent a short, safe path to install the panel, enroll new nodes, and rotate SSH passwords."
---

This guide is the short operational contract for AI agents. It points to the maintained install flow and keeps credentials out of plans and logs.

## For coding agents

Start from [`AGENTS.md`](https://github.com/Mistgate/mistgate/blob/main/AGENTS.md) in the repository. It maps the Go services, API sources, generated code, tests and security invariants. Do not hand-edit `gen/` or `web/src/gen/`; change `proto/` and run `go tool buf lint && go tool buf generate`.

For a new panel, follow [Install the panel](install-panel.md). Mistgate has no Docker runtime: build the Linux binaries with `make build`, install `mistgate` on the panel host and run it under systemd.

## For an MCP operator

Connect to `<admin URL>mcp` with an API token whose profile is **Admin**. The token is owner-scoped; the owner still has to approve risky plans in **Integrations → Approvals**. Use the panel's current owner UI and trusted release bundle.

### Install a node

1. Call `node_install_plan` with a public SSH host, port, login, node name, client-facing address, and optional location/provider. The login must be `root` or have non-interactive `sudo -n`.
2. Show the returned SHA-256 host-key fingerprint and node metadata. Ask the user to confirm that exact host key and the install. Wait for the owner to approve the plan in the panel.
3. After explicit confirmation and approval, call `node_install_apply` with the returned `confirm_token`, the exact `confirmed_fingerprint`, and the SSH password. The password is a one-call input: it is not written into the saved plan or tool result.
4. The panel pins the key, installs its currently trusted signed agent bundle, enrolls the node, and waits for the agent connection. Check `node_server_access_list` for public login metadata; it never returns a password.

### Rotate an SSH password

1. Call `node_server_password_rotate_plan` with the node ID or exact name; show the server and login from the plan and wait for user confirmation and owner approval.
2. Call `node_server_password_rotate_apply` with the one-time confirm token and a new password of at least 12 characters. Do not put that password in a plan, reason, issue, chat transcript, or follow-up read.
3. The panel records an encrypted recovery value before changing the host, tests a fresh SSH login with the new password, then commits it. If connectivity is interrupted, repeat the rotation flow; it reconciles the pending login before replacing it.

### Update existing nodes

Use `updates_status` to see the signed bundle and stale nodes. Use `rollout_start_plan` for a canary and staged rollout. The owner approves it; the panel gates batches on node health and rolls back failed updates. Do not reinstall a node just to update its agent.

See [MCP server](../reference/mcp.md) for connection setup, token profiles, plan lifetime, approvals and all tools. See [Add a node](add-node.md) for the manual fallback.
