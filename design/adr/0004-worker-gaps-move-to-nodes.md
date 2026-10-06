# 0004. What a Worker cannot do moves to the nodes, in both editions

Status: accepted, 2026-10-06.

## Context

Some panel features need things a Worker does not have: UDP (the tunnel health probes dial Hysteria2 and AmneziaWG like a
real client), control over the TLS ClientHello (WARP registration imitates the Android client because Cloudflare answered
429 to Go's default handshake), and long raw sockets. Keeping them only in the VPS edition would break feature parity.

## Decision

- **Tunnel health probes run on the nodes**: the agent embeds the client dialers from `internal/panel/health` (they are
  already injectable). Each node probes itself and the other nodes; the panel schedules the rounds and stores the results
  in the same tables. Cross-node probes also show the path quality between data centres, which a single panel host never
  could.
- **WARP registration runs on the node** (`internal/node/warp/cfapi` already exists); the panel keeps owning the desired
  state and the encrypted account.
- **SSH installation of a node** stays in the panel: on the edge it uses an adapter over `cloudflare:sockets connect()`
  (outbound TCP works; Cloudflare's own IPs are refused, so a server behind Cloudflare is installed with the one-line
  command instead).
- The panel's own self-update, ACME and listeners are VPS-only by nature and have edge equivalents (upload through the
  Cloudflare API, Cloudflare's certificates, the Worker's fetch handler); they are not features a user loses.

## Consequences

- The VPS edition changes too: probes and WARP registration leave the panel process. The panel host no longer needs UDP
  egress or the uTLS dependency for these.
- Cloudflare Containers are not needed for the core; they stay an option.
