# 0017. An agent-operable fleet: what agents read, run, decide and change, and how it is tested

Status: proposed, 2026-11-07.

## Context

The owner wants an assistant (ADR 0014) and outside MCP agents to run the fleet: find which people cannot reach which
servers and why (an address banned, a port blocked, TCP throttled, UDP cut, a whitelist in force), tell AmneziaWG,
VLESS and Hysteria2 apart, recommend and prepare the fix (another port, another protocol, a relay, a CDN profile, a new
node), check the result, and do it again on a schedule. They also want to know what each node and the panel cost in
resources and where the fleet runs out of room. "A reliable agent that manages the whole fleet", also over MCP for
an outside agent.

This grew in phases on several branches (agents data, insight views, batch reads, probes, observability, access
report, resources). This ADR puts them in one frame so that every new piece answers the same five questions: what
can an agent read, what can it run, what can it decide, what can it change, and how do we know it works.

## Decision

### 1. Read: data an agent can trust

Every read is an MCP tool over the panel's own RPCs (the assistant uses the same tools in process). Results pass the
scrubbers (`ScrubJSON`/`ScrubText`; no client addresses, no credentials, no tokens, no admin prefix), keep node
addresses out unless the tool's contract says otherwise, and are capped at 32 KiB with a note when cut.

| Layer | Tools | Source |
| --- | --- | --- |
| Fleet state | `fleet_digest`, `nodes_get`, `node_runtime`, all-node reads | live projection, desired against applied state |
| History | `timeline`, `events_search hours`, `node_metrics hours`, host history | events, journal, `node_host_hour` |
| People | `user_diagnose`, `access_report` | traffic, sessions, link counters, country checks, kl!ck |
| Paths | `path_loss_matrix`, `block_diagnosis` | UDP port checks, TSPU walks, path probes |
| Resources | `node_metrics` (live), `capacity_report`, `panel_resources` | HostMetrics 1 to 20, `node_host_hour`, the panel process |
| The panel | `panel_logs`, audit filters, retention | log ring, audit |

Rules: a read says what it does not know (an agent too old, a counter that does not exist, a sample cut) instead of a
zero; windows are bounded (hours 1 to 720); fan-out over the fleet is bounded and reports partial failures.

### 2. Run: tests an agent may start

`checks_run_now`, `path_probe` (UDP, TCP, TLS between two fleet nodes, MTU sweep), `node_trace`, `node_dns_check`,
`path_speed` (throughput, owner-approved), node debug capture (bounded, scrubbed on the node). Targets are fleet nodes
only, never an address the caller names; each is rate-limited per node and fleet-wide; each needs an agent capability
and answers "agent too old" otherwise; results are stored for the read tools and pruned by retention.

### 3. Decide: diagnosis with confidence

`block_diagnosis` classifies one node and country into `ip_blocked`, `port_blocked`, `tcp_throttled`, `udp_blocked`,
`protocol_fingerprinted`, `whitelist_mode`, `operator_specific`, `regional`, `not_blocked_but_broken` or `unknown`,
each with a confidence, the evidence it rests on and what is not known. `access_report` turns the classes of a group
or a person into findings and recommendations (`add_relay`, `move_port`, `prefer_protocol`, `add_cdn_profile`,
`enable_ipv6_entry`, `remove_from_subscriptions`, `new_node_or_ip`, ...), each naming the plan tool that makes it, or
the read or probe to run first when the evidence is thin. `capacity_report` does the same for load: the bottleneck,
the headroom, and where to move people.

Where the counters cannot tell two causes apart the class says so with low confidence; the counters that would decide
it are the next work item (section 7), not guessed.

### 4. Change: plans the owner approves

Every change is plan then apply: the plan tool validates and describes the change in words, the owner approves it on
the panel (the inline card in the assistant's chat, or the approvals page), apply takes only the confirm token, and
`tokenProcedures` (fail-closed, tested) ties the token to the one procedure approved. A secret the change needs (an
SSH password, a provider key, a bot token, a WARP licence) is typed by the owner into a secure field that submits
straight to the panel; the model and the MCP client never see it, the transcript keeps a hint only.

### 5. Repeat: scheduled tasks (next)

Two kinds, both owner-created through a plan: a **check task** (a set of read and run tools on a cron, no model; it
raises an alert when a result crosses a threshold) and an **assistant task** (a prompt on a cron, run by the assistant
with its tools and the operator profile; its answer lands in a conversation and, if it found something, in an alert
or Telegram). Tools `tasks_list`, `task_get`, `task_run_now`, and create, update and delete as plans. A task that fails
says so; provider errors retry with a fallback provider when one is set. VPS edition first.

### 6. Safety model

- Profiles: readonly (reads, no probes), operator (reads, probes, plans), admin (everything, still plan/apply for
  risky changes). New tools state their profile in the registry; a test pins the counts per profile.
- Data: the scrubbers and the cap above; probes never take an address from the caller; secrets never pass through
  a tool argument, a plan, an audit field or a log.
- Load: rate limits on every run tool; bounded fan-out; the edge edition keeps VPS-only services (ProbeService,
  OpsService) out of its Workers, whose size limits are tracked in `scripts/check-edge-sizes.sh`.

### 7. Testing

1. Unit and table tests per class, recommendation and threshold, including the ambiguous cases and old agents.
2. MCP end to end (`scripts/e2e-mcp.sh`, `scripts/e2e/mcpe2e`): every tool through the real MCP layer with a
   no-secrets scan of every result.
3. Censorship simulation in Linux network namespaces: an entry, a node and a "country" namespace with nftables rules
   for each block type (address drop, port drop, UDP drop, TCP freeze after about 16 KB, a whitelist of addresses,
   one operator only), asserting what `block_diagnosis` and `access_report` say for each.
4. A dev fleet in WSL (panel plus agents in namespaces) driven by an MCP client the way an outside agent would, before
   each release that touches these tools.
5. `scripts/ci-local.sh` before every push; the race job for fleet and agent changes.

### 8. Edition boundaries

Probes, debug capture, panel logs and resources, capacity and scheduled tasks are VPS-only (`//go:build !js` with
stubs that answer Unimplemented). Reads over data both editions store (host history, events, audit) work on both
when the admin Worker has room; otherwise they move to a VPS-only service and the UI hides them on edge.

## Status (2026-11-08)

| Piece | State |
| --- | --- |
| Reads: digest, timeline, diagnose, runtime, loss matrix, batch reads | merged |
| Probes: path_probe, node_trace, node_dns_check | merged |
| Panel logs, request codes, retention, export, /metrics | merged |
| access_report, block_diagnosis | merged |
| path_speed, engine benchmarks, dev fleet | merged |
| Node debug capture | merged |
| Resources: HostMetrics 11 to 20, host hours, capacity, panel resources, load checks, UI | merged |
| Handshake failure reasons in LinkCounters (VLESS attempts, TLS alerts, AWG initiations without reply, QUIC failures) | merged (`links/3`; the kernel AWG backend cannot tell initiations from responses and says so) |
| Scheduled tasks, provider fallback | merged |
| Censorship simulation and dev-fleet MCP runs | next |
| Coarse network (ASN) bucket per hour, no addresses | to decide with the owner (privacy) |

## Consequences

- One frame for every new agent feature: it names its layer, its profile, its limits, what it does not know, and its
  test. A tool that cannot say what it does not know is not merged.
- More data per node and per hour: retention covers each new table; the edge D1 call counts are pinned by tests.
- The diagnosis is only as good as the counters; section 7's simulation is what keeps a class honest.
