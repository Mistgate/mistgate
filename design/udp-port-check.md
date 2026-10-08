# UDP port delivery check

Status (2026-10-08): design accepted (§10), round 1 in progress. Read with [`cloudflare-edition/README.md`](cloudflare-edition/README.md)
(principle 3: what a Worker cannot do moves to the nodes), [ADR 0004](adr/0004-worker-gaps-move-to-nodes.md) and
[`cloudflare-edition/AGENT-LINK.md`](cloudflare-edition/AGENT-LINK.md) §4.5 (the admin → agent seam).

## 1. Why

A production node in the Netherlands, measured 2026-10-08:
- Real Hysteria2 probes from the panel host, tcpdump on both ends: port 443 flows lose 0 packets in both directions.
  Port 8443 flows lose ~40 % client → node and 0 node → client.
- Junk UDP, 150 datagrams at 50 pps per (port, size), counted on the node's eth0 (before netfilter): 8443 got
  97/80/94/89/124/77/150/150 of 150. 443, 2053, 2083, 4433, 8444, 14491, 33183 and 40001 got 150/150 every time. Size
  (100 or 1250 bytes) made no difference. The node side was clean: no NIC, softnet or socket drops.
- So the hoster drops one destination port before the VM, and it does so in bursts: two runs of 3 s in eight were clean.

What the panel does today:
- The port picker offers 8443 first (`twinPreferred`, `internal/panel/access/twin_rpc.go:30`). The HY2 WARP twin on
  that node got it that way.
- Health sees a port only when it is dead: `udp_blocked` = a timeout while another port of the node answers
  (`internal/panel/health/eval.go:340`). A port that loses 40 % shows up only as slow QUIC handshakes (p90 750 ms
  against 24 ms on 443) and nobody can tell why.

Goal: before the panel suggests or adds a UDP port for an inbound on a node, it knows whether datagrams to that port
arrive, and it never offers a port that lost them. An admin can check the ports of a node's existing inbounds on demand.

## 2. Design in one paragraph

The node being checked (the **target**) counts and somebody else sends. The target's agent installs a short-lived nft
table of its own. Its chain sits at `prerouting` priority −500, before defrag, conntrack, NAT and any firewall. It has
one named counter per port and matches only datagrams whose UDP payload starts with an 8-byte random **tag**; those are
counted and dropped. Another node's agent (the **sender**) sends 300 tagged datagrams of 1200 bytes per port at 50 pps,
port by port in a round robin, from four ephemeral source ports (6 s, ≤ 2.9 MB). It sends to the target's
`node.address`. When no other node can send, the VPS panel host sends instead. The panel reads the counters (the agent
deletes the table) and turns `got / sent` into a verdict per port: ok, lossy or broken. 443 is in every run, as an
anchor. The verdicts go into the `node_port_check` table, which both editions have. The picker never offers a port that
lost packets in the last 30 days. Saving a new or changed port runs a check unless a fresh "ok" is on record. The node
page and MCP can check a node's ports on demand.

## 3. Mechanism

### 3.1 Counting on the target

```
table inet mistgate_udpcheck {
    counter p443 { }
    counter p8443 { }
    chain pre {
        type filter hook prerouting priority -500; policy accept;
        udp dport 443  @th,64,64 0x9f2c41d07ab35e18 counter name "p443"  drop
        udp dport 8443 @th,64,64 0x9f2c41d07ab35e18 counter name "p8443" drop
    }
}
```

- **Before anything that drops.** Priority −500 runs before defrag (−400), raw (−300), conntrack (−200), the hop DNAT
  (`dstnat`, `internal/node/hostctl/hostctl.go:198`), UFW/firewalld and the AWG input counter. The counter therefore
  says what the network delivered, whether or not the port is bound and opened in the host firewall. That last point
  matters: UFW sync opens only Mistgate's inbound ports, so on a node with UFW a candidate port would look blocked to a
  temporary listener.
- **Tag, not addresses.** The rule never names the sender, so it works when the sender is behind NAT (1:1 cloud NAT,
  a home panel) and when its address is not the one the panel knows. A real client packet matches only if its first 8
  payload bytes equal a fresh random 64-bit value. The codebase already matches raw payload: the WARP table's
  `@th,64,8` / `@th,72,24` rule (`internal/node/warp/nft_render.go:29`) runs on every production node.
- **`drop`.** Test datagrams never reach an engine. They do not show up in hysteria or AWG, raise no ICMP
  port-unreachable towards the sender and create no conntrack entries. Health probes to the same port are untagged and
  pass.
- **Its own table** (`mistgate_udpcheck`). The agent replaces `mistgate_node` as a whole on every `SetPortHops` and
  `ApplyBaseline` (`internal/node/hostctl/linux.go:254`), which would wipe a counter put there. The table goes into
  `OwnNftTable` (`hostctl.go:117`), or the doctor's `foreign_nft` check would flag it.
- **Counted in bytes as well as packets.** quic-go turns UDP GRO on for its socket. A GRO super-packet counts as one
  packet but carries all its bytes, and has one IP+UDP header. So
  `got = (bytes − packets × hdr) / size`, with hdr = 28 for IPv4 and 48 for IPv6, clamped to `[0, sent]`. This is exact
  with and without GRO.
- **Lifetime.** The table is armed with `hold_s` (30, at most 60) and the agent deletes it after that time even if
  nobody reads it. The agent also deletes it at start, before anything runs (covers a re-exec after a self-update),
  in `Cleanup`, and in `cleanup-net`, which the unit's ExecStopPost runs after every stop, a crash included
  (`cmd/mistgate-node/cleanup.go:26`). A table left behind is harmless anyway: it matches only the dead tag. One count
  at a time per node: an arm while one is armed gets `busy`, and a stop with another tag gets `not_armed`.

### 3.2 Sending

| Parameter | Value | Why |
|---|---|---|
| payload | 8-byte tag + random fill, generated once per run | looks like Salamander/AWG on the wire (every production hy2 profile uses Salamander); the tag is what nft matches |
| size | 1200 bytes of payload | the size of a QUIC packet; 1248 bytes with IPv6 headers fits the 1280 minimum MTU, so no fragments. Size made no difference on that node, so one size is enough |
| count, rate | 300 per port, 50 pps per port | about 6 s. The that node losses came in bursts and 3 s runs missed them 2 times in 8, so a longer run catches more |
| ports per run | at most 8, 443 always among them | 443 is the anchor (§3.3) |
| source ports | 4 ephemeral sockets, round robin | a provider that hashes flows over a bad link (ECMP/LAG) loses some flows on every port; with one flow per port that would look like a port problem |
| order | each tick sends one datagram to every port | all ports see the same moments, so a burst of loss hits all of them alike |
| destination | the target's `node.address`, resolved by the sender, IPv4 first | the address clients use; the same rule as `health.nodeIP` (`internal/panel/health/hy2.go:105`) |
| total | ≤ 8 × 300 × 1228 B ≈ 2.9 MB, ≤ 400 pps | negligible for both ends |

`Send` lives in a new package `internal/udpcheck` (one file), used by the agent and by the VPS panel fallback. It
enforces the caps itself, so neither caller can ask for more. `health.nodeIP` moves into it as `Resolve`, so the two
copies cannot drift.

### 3.3 Verdict

For each port, `delivered = got / sent`:

| Verdict | Delivered | Meaning |
|---|---|---|
| ok | ≥ 95 % | loss below 5 % |
| lossy | 70–95 % | 5–30 % lost |
| broken | < 70 % | 30 % or more lost |

- A run counts only if at least one of its ports delivered ≥ 95 %. Otherwise the path from that sender is lossy, or
  UDP does not get through at all, and nothing can be said about any single port. Such a run is **inconclusive**: the
  panel tries one more sender, then reports `inconclusive` and stores nothing.
- The runs on that node, delivering 65/53/63/59/83/51/100/100 %, would have read broken five times, lossy once and ok twice.

**Stickiness.** A conclusive run that finds a port lossy or broken sets `bad_at`. A later clean run updates the verdict
but keeps `bad_at`. The picker avoids a port whose `bad_at` is less than 30 days old. A wrongly avoided port costs
nothing (there are thousands of others), and stickiness is what catches intermittent loss across several runs. An
"ok" is fresh for 24 h: a save within that time is not checked again.

### 3.4 Alternatives considered

| Approach | Verdict |
|---|---|
| nft counter at prerouting −500 with a tag match (chosen) | sees what the network delivered, bound or not; harmless if left behind; no new privileges (the agent already drives nft) |
| a temporary listener bound to the candidate port | sits after the host firewall (UFW drops unopened ports, which looks like loss); cannot test a port an engine holds (SO_REUSEPORT would steal its packets); answers nothing about existing inbounds |
| AF_PACKET / tcpdump-style capture | needs CAP_NET_RAW (not in the hardened unit) and a BPF filter, and copies every packet of a busy NIC to userspace for 6 s |
| nft counter at `input` (like the AWG `udp_<port>` counter, `hostctl/tunnel.go:144`) | sits after the firewall and the hop DNAT: same flaw as the listener |
| `netdev ingress` on the main interface | earlier still, but needs the interface name and misses multi-homed hosts; prerouting already runs before every rule that drops |
| match on the sender's address and port | breaks behind NAT; the panel would have to choose the source port and the sender to bind it (EADDRINUSE, a retry) |
| node checks itself through a public STUN server | sends to a third party, is rate-limited, and checks replies to the node's own flow rather than new inbound flows; not for v1 (open question 2) |
| QUIC-shaped probes | DPI on QUIC happens on the client side (RU), out of reach of any server vantage point; random bytes match what Salamander puts on the wire |

### 3.5 What one sender cannot tell

- **Filtering that depends on the source.** National DPI (RU TSPU sits at the borders of client ISPs), provider rules
  by source country or ASN, and anti-DDoS that triggers only with many sources or high pps. The owner's
  own network reaches two nodes lossily on non-443 UDP, while from the panel host one of them is clean on 8443. A passing check
  means "this node's network delivers UDP to this port from data centre X", not "every client reaches it".
- **Loss on the sender's own side.** Outbound filtering at the sender's provider looks like target loss on that port.
  A second sender tells them apart (v2: confirm a bad verdict with another sender before it becomes sticky).
- **Paths that stay inside one provider.** Two nodes at the same hoster may bypass the edge filter. The sender choice
  prefers another provider, then another country (§4).
- **Ports that are not checked.** Hop-range ports (a hysteria2 hop range is up to 20000 ports; v2 samples a few) and
  IPv6 (v1 sends IPv4 when the address has it).

## 4. Who sends

**Recommendation:** other nodes send, in both editions. The VPS panel host sends only when the fleet has no other
usable node.

| Option | For | Against |
|---|---|---|
| A. Nodes only, both editions | ADR 0004 direction (probes move to nodes in both editions); one code path, exercised on the VPS today, so the edge gets a proven path | a one-node fleet cannot check, in either edition |
| B. Panel host on the VPS, nodes on the edge | panel host = the vantage point of today's health probes | the edge path would go untested in real use until the edge ships; the two editions would behave differently with any fleet size |
| **A + VPS fallback (recommended)** | A, plus one-node VPS fleets (most new installs) can check | the one-node case differs between editions; the reason is physical (no UDP in a Worker) and the UI says so |

**Choosing the sender.** Candidates are the connected nodes, other than the target, that list `udpcheck/1`. Sort order:
first a different non-empty `provider` than the target's, then a different `country_code`, then name. Take the first.
An inconclusive run is retried once with the next one. With no candidate: the VPS edition uses the panel host
(`panelSendsUDP` is a build-tag constant, `true` in `ports_native.go` and `false` in `ports_js.go`, the pattern of
`health/dialers_js.go`); the edge edition returns `no_sender`.

**Panel and target on one host.** If the target's resolved address is one of the panel host's own addresses
(`127.0.0.1` in `scripts/e2e-wsl.sh`, or a node installed next to the panel), packets never leave the machine. The run
still happens and returns its counts, but it is not stored, and the error code is `same_host`. The e2e test relies on
exactly that.

**A one-node fleet on the edge.** No check: `no_sender`. The picker behaves as today and says the port is unchecked.
The UI says a second node can check it. STUN is open question 2.

**The constraint from AGENT-LINK.md §2.** The check is driven by an admin request (later also by cron), never from
inside a session step. On the edge it calls the NodeLink objects of two different nodes, which a step must never do.

## 5. Where it runs

| Place | v1 | Behaviour |
|---|---|---|
| (a) Picker: `TwinProfile` with no port given, dry run or real (`twin_rpc.go:133`) | yes | the first 4 candidates free on all of the profile's nodes and not bad on any are checked, one run per node, up to 4 nodes in parallel, skipped where all 4 are fresh-ok. The first candidate with no bad verdict on any node wins (unchecked is allowed and reported). If none, the next 4 once, then `no_clean_port`. This is where that node got 8443 |
| (a) Picker: `free=` of `port_taken`, `free_port` of `validate_only` (`profile_rpc.go:614`) | yes | **cache only**, never a run. Bad ports are left out; a fresh-ok port comes first, then an unchecked one |
| (b) `validate_only` of CreateInbound / UpdateInbound, UpdateProfile `dry_run` | yes | cache only (stays fast). A port changed to a bad one, or switched back on while bad, is refused with `port_lossy` and a `free=` |
| (b) Saving: CreateInbound, UpdateInbound (port changed or switched on), UpdateProfile (the effective port of an enabled inbound changed), each per node | yes | fresh ok → saved. Otherwise one run of [port, 443, up to 3 picker candidates]. Bad → `port_lossy` with a `free=` that the same run proved, unless `allow_lossy_port`. Cannot check → saved with the warning `port_unchecked{reason}` |
| (c) On demand: "Check ports" on the node page, MCP `node_ports` | yes | the node's enabled inbound ports, 443, then the free preferred ports, up to 8; more inbound ports take several runs |
| (d) Periodic re-check (every 6 h per node) and an alert when an inbound's port turns bad | later (round 5) | the same CheckPorts, driven by the health scheduler on the VPS and by cron on the edge (step 6) |
| A port of an inbound that is not changing | — | never refused: an unrelated edit of that node's HY2 WARP inbound (SNI) keeps working, with a `port_lossy` warning |

## 6. API

### 6.1 Agent protocol (`proto/mistgate/agent/v1/agent.proto`, additive, api_version stays 1)

New section "UDP DELIVERY CHECK" in the header comment, and a `COMMANDS` line. Capability `udpcheck/1`, listed when
the host implements the counter (Linux; `internal/node/agent/l3.go:120`). The panel sends both messages only to agents
that list it; to an older agent it says `agent_too_old`.

```proto
// ConnectResponse.message:  UdpCount udp_count = 25;  UdpSend udp_send = 26;

// Count UDP datagrams that arrive at this node (see "UDP DELIVERY CHECK"). One CommandResult per message.
message UdpCount {
  string request_id = 1;
  // 8 random bytes: only datagrams whose UDP payload starts with them are counted, and dropped.
  bytes tag = 2;
  // Arm: 1-8 distinct ports. Ignored with stop.
  repeated uint32 ports = 3;
  // Arm: the agent removes the table after this many seconds (1-60).
  uint32 hold_s = 4;
  // true: read the counters armed with tag and remove the table.
  bool stop = 5;
}

// Send tagged datagrams to another node of the fleet. One CommandResult after the last datagram.
message UdpSend {
  string request_id = 1;
  // The target node's address (node.address): an IP, or a name this node resolves (IPv4 first).
  string host = 2;
  repeated uint32 ports = 3;  // 1-8
  bytes tag = 4;              // 8 bytes, the start of every datagram
  uint32 count = 5;           // per port, 1-300
  uint32 pps = 6;             // per port, 1-50
  uint32 size = 7;            // UDP payload bytes, 64-1200
}
```

Answers:
- `UdpCount`, arm: `CommandResult{ok}` once the table is installed.
- `UdpCount`, stop: `CommandResult{ok, params{"p<port>": packets, "b<port>": bytes}}`.
- `UdpSend`: `CommandResult{ok, params{family: "4"|"6", sent: "<per port>"}}`.
- Errors: `busy` (a count already armed; 4 sends already running; a bandwidth test running), `not_armed`,
  `bad_params`, `unsupported_host`, `no_route` (resolve or send failed), `failed: <short>`.
- The agent refuses an unspecified, multicast or broadcast destination. It allows loopback (tests).

### 6.2 Admin API

`common.proto`:

```proto
// One port of a node in the UDP delivery check.
message PortCheck {
  string node_id = 1;
  uint32 port = 2;
  // ok | lossy | broken; "" = not checked (reason)
  string verdict = 3;
  uint32 sent = 4;
  uint32 got = 5;
  int64 checked_unix = 6 [jstype = JS_NUMBER];
  // The last run in the last 30 days that lost >= 5 %; 0 = none. The picker never offers such a port.
  int64 bad_unix = 7 [jstype = JS_NUMBER];
  // "panel" or the name of the node that sent.
  string sender = 8;
  // verdict "": node_offline | agent_too_old | no_sender | busy | inconclusive | same_host | no_route | failed
  string reason = 9;
}
```

`node.proto`:

```proto
// Checks that UDP datagrams sent to the node arrive (agents with "udpcheck/1"): about 6 s per 8 ports, a few MB. Ports:
// the given ones (at most 7), else the node's enabled inbound ports and the free ports the picker prefers; 443 is
// always added. Another node sends (both editions); a VPS panel with no other usable node sends itself. Conclusive
// results are stored and shown in GetNodeResponse.port_checks. Owner or helper; API tokens directly (like RunDoctor).
rpc CheckPorts(CheckPortsRequest) returns (CheckPortsResponse);

message CheckPortsRequest  { string node_id = 1; repeated uint32 ports = 2; }
message CheckPortsResponse {
  repeated PortCheck ports = 1;
  // "" = checked. Else PortCheck.reason words; same_host and inconclusive still carry the counts.
  string error_code = 2;
  string sender = 3;
}
// GetNodeResponse: repeated PortCheck port_checks = 12;   // stored results of this node (its address)
```

Every "could not check" outcome is an `error_code`, never an RPC error. This differs from MeasureBandwidth, which
returns RPC errors for offline and too old. The reason: the picker and the UI read one vocabulary.

`profile.proto`:
- New request fields: `CreateInboundRequest.allow_lossy_port = 6`, `UpdateInboundRequest.allow_lossy_port = 6`,
  `UpdateProfileRequest.allow_lossy_port = 6`. They mean "add anyway"; the admin's choice is audited.
- New refusal code `port_lossy: port=8443&node=n1&sent=300&got=190&at=<unix>&sender=n2&free=2053`
  (FAILED_PRECONDITION). `free` is a port the same run proved, else the cache-only picker's choice; 0 = none.
- New warnings: `port_unchecked {node, port, reason}`, and `port_lossy {...}` (saved with `allow_lossy_port`, or the
  port did not change).
- `TwinProfileResponse.port_checks = 8` (repeated PortCheck): the chosen port on each node, plus the candidates skipped
  as bad.
- New error `no_clean_port` (FAILED_PRECONDITION) from TwinProfile.

Policy (`internal/panel/auth/policy.go`): `CheckPorts` is `levelWrite`, next to `RestartInbounds` (line 58).
`policy_tokens.go` gives it `TokenAccessDirect`, like `RunDoctor` (line 60).

### 6.3 Store (both editions; migration `00054_node_port_check`, renumbered if `node_live` lands first)

```sql
CREATE TABLE node_port_check (
    node_id    TEXT    NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    port       INTEGER NOT NULL,
    address    TEXT    NOT NULL,           -- node.address the run went to; rows of an old address are ignored
    sent       INTEGER NOT NULL,
    got        INTEGER NOT NULL,
    verdict    TEXT    NOT NULL,           -- ok | lossy | broken (inconclusive runs are not stored)
    sender     TEXT    NOT NULL,           -- 'panel' or the sending node id
    checked_at INTEGER NOT NULL,
    bad_at     INTEGER NOT NULL DEFAULT 0, -- last run that lost >= 5 %; a clean run keeps it
    PRIMARY KEY (node_id, port)
);
```

- `PutPortChecks(ctx, rows)` writes one multi-row statement, `INSERT … VALUES … ON CONFLICT(node_id, port) DO UPDATE`:
  - `bad_at` = the run's time for a bad row; 0 when the address changed; otherwise the old value.
  - It is a single statement, atomic on SQLite and on D1. No batch, so `TestTransactionCallSitesClassified` is not
    affected.
- `PortChecks(ctx, nodeIDs...)` is one read joined on `node.address = node_port_check.address`.
- `PortCheck.Bad(now)` = `bad_at` less than 30 days old. `PortCheck.Fresh(now)` = ok and less than 24 h old.
- No client data: node, port, counts, sender node.

### 6.4 Events and alerts

- v1: event `port_lossy` (severity 2), raised when a conclusive run finds an enabled inbound's port bad and the stored
  row was not bad before (no spam on repeated clicks). Params: inbound, profile, port, sent, got, sender.
  Audit `node.ports_check` on every run.
- Round 5: alert kind `ALERT_KIND_PORT_LOSSY = 15` (warning), derived in `health` from the latest verdict (not the
  sticky one) of each enabled inbound's current port. It resolves when a re-check is clean or the inbound moves to
  another port. It goes to Telegram like the other alerts. The action is `open_profiles`, as for `udp_blocked`.

### 6.5 MCP (`internal/panel/mcp/tools_read.go`)

`node_ports`, built like `node_doctor` (lines 180-184):
- Readonly profile: the stored results through GetNode.
- Operator profile: an extra `check: true` that calls CheckPorts.
- The view shows, per port: verdict, sent/got, checked, bad, sender, and the inbound/profile listening there.

### 6.6 UI states (texts later, ru/en by the lead)

- **Node page, "UDP ports" block.** The block shows:
  - "never checked", or a per-port row: ok / lossy n % / broken n %, with "lost packets n days ago, clean now", when,
    from where, and which profile listens there;
  - "Check" with a ~6 s progress state;
  - the run errors: node_offline, agent_too_old, no_sender (the edge with one node), busy, inconclusive (with the
    counts), same_host, no_route, failed.
- **Add a profile to a node / change a node's port** (`web/src/screens/node/add-inbound.tsx`, `profiles.tsx`,
  `inbound-check.tsx` refusals):
  - while checking: the `port_lossy` refusal with the auto-filled `free`, as for `port_taken` (`add-inbound.tsx:119`),
    and an "add anyway" choice (`allow_lossy_port`);
  - while saving: "checking UDP to port X…" on the button, up to ~8 s;
  - after saving: the warning `port_unchecked`, with its reason.
- **Quick profile** (`add-inbound.tsx:359`): `port_lossy` is handled like `port_taken` (the profile's port is set to
  `free`, then the inbound is added).
- **Deploy a profile to nodes** (`profiles/deploy.tsx:49`): a `port_lossy` row per node, with `free` offered as a
  port override for that node.
- **Twin** (`profiles/twin.tsx:46`): a "checking UDP on N nodes…" state, the chosen port with a badge per node
  (checked / unchecked: reason), "8443 skipped: lost 37 % on that node", and `no_clean_port`.
- **Profile editor**: a port change refused with `port_lossy` on node X, with "save anyway".
- **Events list**: `port_lossy`.

## 7. Safety

- **Only fleet addresses.** The panel puts only `node.address` of the target row into `UdpSend.host`. No request
  carries an address, and the admin gives only a node id and ports.
- **Hard caps in `udpcheck.Send`.** At most 8 ports, 300 datagrams per port, 50 pps per port, 64-1200 bytes, 10 s, and
  4 concurrent sends per agent. The VPS panel host also allows at most 4 concurrent sends.
  - A compromised panel could make one node send at most ~12 MB in 10 s to an address of its choice. That is 1:1
    random bytes, which amplify nothing. Such a panel can already make the node pull 1.5 GB for a bandwidth test.
- **No client IPs.** The rule matches a tag, not an address. The store keeps node, port, counts and the sender
  node's id. The tag is not stored.
- **Cleanup.** The agent removes the table:
  - after `hold_s`;
  - on stop;
  - at agent start;
  - in `Cleanup`;
  - in `cleanup-net` (ExecStopPost, crash included).
  
  A table left behind drops only datagrams that carry the dead 64-bit tag.
- **Concurrency.**
  - Agent: one armed count per node (`busy`); up to 4 sends; no arm or send while a bandwidth test runs (it would
    fake loss).
  - VPS panel: a per-node lock that respects the request's context. A second caller waits and then reads the fresh
    cache instead of starting another run.
  - Edge: the agent's `busy` is the guard (an isolate's memory is not shared).
- **A request that dies midway** (page closed, panel restart): the stop is sent with
  `context.WithoutCancel(ctx)` and a short timeout. If even that is lost, the hold ends the test.
- **Cost on the edge per check:**
  - 3 asks to 2 NodeLink objects (about 3 billed DO storage writes per frame, AGENT-LINK.md §5);
  - 1 D1 read and 1 D1 write of at most 8 rows;
  - an event row when an inbound's port turns bad.
  
  Round 5's periodic run (4 a day per node) costs nothing that matters.

## 8. Edge edition

- No new edge code in v1. The fleet uses today's `session.roundtrip` (`internal/panel/fleet/agent.go:680`), like
  `measureBandwidth` (`fleet/bandwidth.go:29`).
- Step 4c moves both to `Fleet.command`. That needs:
  - `UdpCount` and `UdpSend` added to the frame allowlist as `command` (AGENT-LINK.md §4.5; amend it in round 2);
  - the sender candidates read from the `node_live` projection and from `node.agent_caps` (step 4b).
- Until 4c, the edge sees no live sessions and returns `node_offline`, as it does for MeasureBandwidth. `panelSendsUDP`
  is false in the js build, so the peer path is the only path there. On the VPS that same path runs whenever the fleet
  has a second node, so the edge gets code that is already proven.

## 9. Rounds

Every round runs the gate: `go test -p 2 ./...`, wasm build, worker and web tests, race (fleet, agent, store), and
bridge budgets unchanged (the feature touches no subscription path). The lead runs `buf generate`, because the Codex
sandbox has no network. Node rounds also run `go test ./internal/node/...` in WSL; the Windows gate and Codex never run
`*_linux_test.go`.

**Round 1. Agent and host (Codex).**

Changes:
- `agent.proto`: the section, `UdpCount` (25), `UdpSend` (26), the capability.
- New `internal/udpcheck/udpcheck.go`: `Send` with its caps.
- `internal/node/hostctl/udpcheck.go` (all OS): `NftUDPCheckTable`, `RenderUDPCount(tag, ports)`, and the
  `UDPCounter` interface (`CountUDP`, `TakeUDPCount`), type-asserted like `TunnelHost`.
- `udpcheck_linux.go`: `readCounters` (`tunnel_linux.go:193`) generalised to a table and a prefix, now returning bytes
  too, plus a stateless `CleanupUDPCount`.
- `other.go`: stubs.
- `OwnNftTable`; `Cleanup` (`linux.go:312`); `cmd/mistgate-node/cleanup.go:26`.
- New `internal/node/agent/udpcheck.go`:
  - `udpCount`: arm/stop, a hold timer, `busy`, the tag check;
  - `udpSend`: at most 4, and `busy` while a bandwidth test runs;
  - the two dispatch cases (`agent.go:851`);
  - the capability (`l3.go:120`);
  - removing a leftover table in `Run` before `ApplyBaseline` (`agent.go:369`).

Proof:
- The render test on any OS: rule order, priority −500, one counter per port, the tag as 16 hex digits, refusal of
  0/9/duplicate ports.
- `udpcheck_linux_test.go` with the fake runner:
  - the script;
  - JSON parsing of packets and bytes;
  - take = read + delete;
  - quiet when the table is absent.
- `udpcheck_lab_linux_test.go`, opt-in `MG_ROOT_TESTS=1`, netns. With the real nft:
  - tagged datagrams to an unbound port and to a bound port are counted and dropped (the bound listener sees none);
  - untagged datagrams reach the listener and are not counted;
  - the table is gone after take and after the hold.
- `udpcheck_test.go` against a local listener: count, the tag, 4 distinct source ports, caps refused, context cancel.
- Agent tests through the fake panel:
  - capability listed;
  - arm ok, then a second arm is `busy`;
  - stop with a foreign tag is `not_armed`;
  - the hold expiry deletes the table;
  - a send reaches a local listener;
  - the fifth concurrent send is `busy`;
  - `measuring` gives `busy`;
  - `bad_params`.
- The doctor treats the table as its own.

Risks: nft syntax differences on older hosts (the lab test runs on WSL's nft; the WARP `@th` rule already runs in
production). It ships with the next node rollout.

**Round 2. Panel check (Codex).**

Changes:
- Migration and `schema.golden`; `store/port_check.go`.
- New `fleet/ports.go`:
  - `CheckPorts(ctx, nodeID, ports) ([]store.PortCheck, string)`, the sender choice, the verdict, the per-node lock;
  - the panel fallback through `udpcheck.Send`, with `ports_native.go` / `ports_js.go`;
  - `same_host`;
  - a 0.5 s settle before the read;
  - a `sendUDP` func field as the test seam.
- `nodeService.CheckPorts`; `GetNode` gets `port_checks` (`admin_node.go:331`).
- proto: `PortCheck`, `CheckPorts`.
- Policy; the `port_lossy` event and the audit.
- MCP `node_ports`.
- `health.nodeIP` moves to `udpcheck.Resolve`.
- Docs: `docs/en/guide/nodes.md`, "UDP ports"; AGENT-LINK.md §4.5 allowlist.

Proof:
- Store tests on SQLite and the fake D1: upsert; sticky `bad_at`; an address change resets it and filters it out;
  cascade.
- Fleet tests on the `bandwidth_test.go` harness with two fake agents:
  - the order arm → send → stop;
  - the threshold table;
  - inconclusive, then a retry with the next sender;
  - sender preference by provider and country;
  - a peer without the capability is skipped;
  - no peer → the panel fallback (fake `sendUDP`), and with the const off → `no_sender`;
  - target offline or too old;
  - `busy`; a failed sender still disarms; context cancel still disarms;
  - only conclusive, non-`same_host` runs are stored;
  - the event fires only on the first bad;
  - bytes → datagrams with GRO-sized counters.
- The policy test; MCP tests (readonly never reaches `CheckPorts`, operator does).

Risks: test churn in the fleet harness. The edge path still waits for 4c.

**Round 3. Picker and save checks (Codex).**

Changes:
- `profile.proto` fields and codes.
- `access.Config.CheckPorts`, a func, wired in `app/panel.go:175`; nil = no checks.
- `freePort` → `freePorts(protocol, n, ok)`.
- `freePortFor` and `portRefusal` take the node's checks.
- `twinPort` runs the checks.
- One `checkPort` helper for CreateInbound, UpdateInbound and UpdateProfile (real calls).
- `allow_lossy_port`, with the audit.
- An e2e step in `scripts/e2e-wsl.sh`: CheckPorts on the local node returns `same_host` with got = sent on every port.

Proof (access tests with a fake checker):
- `validate_only` and the dry runs never call it, and add only one read;
- `free`/`free_port` skip bad ports and prefer fresh-ok ones;
- the twin dry run picks the first clean port, skips a bad one, runs nodes in parallel, ends in `no_clean_port`;
- the real twin call with a port that turned bad is refused;
- CreateInbound with a fresh ok makes no call;
- unchecked → a run; bad → `port_lossy` with a proven `free`;
- `allow` → saved, with a warning and an audit;
- `node_offline` → saved with `port_unchecked`;
- UpdateInbound with an unchanged port on a bad inbound is not refused;
- an UpdateProfile port change on two nodes is refused per node.

Risks: a save now takes up to ~8 s; deploying to several nodes is sequential in the UI (see open question 3).

**Round 4. UI (Claude, sonnet-high).**

Changes: the §6.6 states, dev-kit fixtures for each, i18n keys (texts by the lead).

Proof: web tests for the refusal → `free` auto-fill, "add anyway", the twin badges, the node block states; dev-kit
screenshots.

Risks: three dialogs share the `port_taken` handling; extend that shared handling once rather than in each.

**Round 5. Later (Codex).**

Changes: periodic re-check (6 h per node, VPS health scheduler; edge cron in step 6); `ALERT_KIND_PORT_LOSSY` and
Telegram; v2 options (a second sender to confirm a bad verdict, sampled hop-range ports, IPv6).

Proof: health derive tests (open and resolve on re-check or on a port move), Telegram texts, a scheduler test.

Risks: alert noise from bursty loss; the alert uses the latest verdict, not the sticky one.

Deployment: rounds 1-3 need the new agent on the target and on at least one other node, so a release and a rollout to
all four nodes. Then check that node from the node page. Expected: 8443 broken, every other port ok. Move that node's HY2 WARP inbound
to the proven `free` port with a port override.

## 10. Decisions (owner, 2026-10-08)

1. **Who sends.** Other nodes in both editions; the VPS panel host only when there is no other usable node.
2. **The edge with a single node.** No check (`no_sender`); no third-party STUN.
3. **Suggestions in the add/edit form** come from the cache (a port that lost packets is never offered); the port is
   proven when saving (up to ~8 s), with a proven alternative when it fails. The twin dialog proves its port before it
   shows it.
4. **Thresholds and memory.** ok >= 95 % delivered, broken < 70 %; a port that lost packets is not offered for 30 days.
5. **"Add anyway"** for a lossy port, audited.
6. **Periodic re-check every 6 h and the alert** come in round 5, after rounds 1-4.
7. **Who may run a check**: owner and helper, and API tokens/MCP operator directly, like the doctor.